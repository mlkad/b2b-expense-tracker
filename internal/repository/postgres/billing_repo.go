package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/billing"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
	"github.com/mlkad/b2b-expense-tracker/internal/platform/postgres"
	"github.com/mlkad/b2b-expense-tracker/internal/repository/postgres/gen"
)

// ErrDuplicateDelivery means this relay event has already been claimed. It is
// the expected outcome of an at-least-once stream, not a fault: the caller
// acknowledges with 200 and does nothing.
var ErrDuplicateDelivery = errors.New("billing event has already been received")

// ErrStaleEvent means the delivery is older than the state already applied.
// Also expected, also acknowledged.
var ErrStaleEvent = errors.New("billing event is older than the recorded state")

type BillingRepository struct{}

func NewBillingRepository() *BillingRepository { return &BillingRepository{} }

// GetEntitlement reads the tenant's local subscription projection.
//
// This is the query on every gated request, answered from
// tenant_subscriptions_live_idx. A missing row is not an error: a tenant that
// never started a checkout is on the free tier, and reporting ErrNotFound
// would make every handler write the same fallback.
func (r *BillingRepository) GetEntitlement(ctx context.Context, tc *postgres.TenantConn) (billing.Entitlement, error) {
	row, err := gen.New(tc).GetTenantEntitlement(ctx, tc.TenantID())
	if err != nil {
		if translate(err) == shared.ErrNotFound {
			return billing.FreeEntitlement(), nil
		}
		return billing.Entitlement{}, translate(err)
	}

	return billing.Entitlement{
		Plan:              billing.PlanCode(row.PlanCode),
		Status:            billing.Status(row.Status),
		Seats:             int(row.Seats),
		CurrentPeriodEnd:  row.CurrentPeriodEnd,
		CancelAtPeriodEnd: row.CancelAtPeriodEnd,
		TrialEnd:          row.TrialEnd,
		Known:             true,
	}, nil
}

// ClaimDelivery records a relay event, returning ErrDuplicateDelivery if it
// has been seen before.
//
// The caller MUST have verified the signature before calling this. event_id
// comes from the request body and is unauthenticated until then; claiming
// first would let anyone plant a settled row under a guessed id and have the
// genuine delivery discarded as a duplicate - answered 200, never processed.
//
// It runs in a system transaction because the tenant is not yet known: working
// out which tenant an event belongs to is the next step.
func (r *BillingRepository) ClaimDelivery(ctx context.Context, db *postgres.DB, eventID, eventType string, payload []byte) error {
	return db.WithSystemTx(ctx, postgres.Binding{}, "claim a verified billing relay delivery",
		func(ctx context.Context, tc *postgres.TenantConn) error {
			_, err := gen.New(tc).ClaimBillingEvent(ctx, gen.ClaimBillingEventParams{
				EventID:   eventID,
				EventType: eventType,
				Payload:   payload,
			})
			if err != nil {
				if translate(err) == shared.ErrNotFound {
					// ON CONFLICT DO NOTHING returned no row.
					return ErrDuplicateDelivery
				}
				return translate(err)
			}
			return nil
		})
}

// SettleDelivery closes out a claimed event.
func (r *BillingRepository) SettleDelivery(ctx context.Context, db *postgres.DB, eventID string, tenantID *uuid.UUID, status gen.BillingEventStatus, cause error) error {
	var detail *string
	if cause != nil {
		msg := cause.Error()
		detail = &msg
	}

	return db.WithSystemTx(ctx, postgres.Binding{}, "settle a billing relay delivery",
		func(ctx context.Context, tc *postgres.TenantConn) error {
			_, err := gen.New(tc).SettleBillingEvent(ctx, gen.SettleBillingEventParams{
				EventID:     eventID,
				Status:      status,
				ErrorDetail: detail,
				TenantID:    tenantID,
			})
			return translate(err)
		})
}

// ResolveTenantByBillingRef maps a gateway customer reference to a tenant.
//
// System transaction: the relay has no session and no token, only a signed
// payload naming a customer reference. The lookup is by unique index, so the
// widening reaches exactly one row.
func (r *BillingRepository) ResolveTenantByBillingRef(ctx context.Context, db *postgres.DB, ref string) (uuid.UUID, error) {
	var id uuid.UUID

	err := db.WithSystemTx(ctx, postgres.Binding{ReadOnly: true}, "resolve a tenant from a billing customer reference",
		func(ctx context.Context, tc *postgres.TenantConn) error {
			return tc.QueryRow(ctx,
				`SELECT id FROM tenants WHERE billing_customer_ref = $1`, ref,
			).Scan(&id)
		})
	if err != nil {
		return uuid.Nil, translate(err)
	}
	return id, nil
}

// SubscriptionState is what a relayed event or a reconciliation read carries.
type SubscriptionState struct {
	GatewaySubscriptionID string
	GatewayCustomerRef    string
	PlanCode              string
	Status                string
	Seats                 int32
	CurrentPeriodStart    time.Time
	CurrentPeriodEnd      time.Time
	CancelAtPeriodEnd     bool
	TrialEnd              *time.Time
	EventID               string
	EventAt               time.Time
}

// ApplySubscription writes the projection for one tenant.
//
// The out-of-order guard is in the SQL: the DO UPDATE branch has a WHERE that
// refuses an event older than the one already applied. Zero rows returned
// means this delivery lost that comparison, which is the expected outcome of
// an unordered at-least-once stream - a redelivered `updated` from an hour ago
// must not overwrite the `canceled` that arrived since.
//
// The transaction is a system one bound to the tenant, so both the widening
// clause and the ordinary tenant clause of the isolation policy are satisfied.
// tenant_subscriptions has no tenant-writable policy at all: a tenant that
// could write this row could grant itself the enterprise plan.
func (r *BillingRepository) ApplySubscription(ctx context.Context, db *postgres.DB, tenantID uuid.UUID, s SubscriptionState) error {
	return db.WithSystemTx(ctx, postgres.Binding{TenantID: tenantID}, "apply a relayed subscription change",
		func(ctx context.Context, tc *postgres.TenantConn) error {
			_, err := gen.New(tc).UpsertTenantSubscription(ctx, gen.UpsertTenantSubscriptionParams{
				TenantID:              tenantID,
				GatewaySubscriptionID: s.GatewaySubscriptionID,
				GatewayCustomerRef:    s.GatewayCustomerRef,
				PlanCode:              s.PlanCode,
				Status:                s.Status,
				Seats:                 s.Seats,
				CurrentPeriodStart:    s.CurrentPeriodStart,
				CurrentPeriodEnd:      s.CurrentPeriodEnd,
				CancelAtPeriodEnd:     s.CancelAtPeriodEnd,
				TrialEnd:              s.TrialEnd,
				LastEventID:           &s.EventID,
				LastEventAt:           &s.EventAt,
			})
			if err != nil {
				if translate(err) == shared.ErrNotFound {
					return ErrStaleEvent
				}
				return translate(err)
			}
			return nil
		})
}

// LinkBillingCustomer records the gateway reference on first checkout.
func (r *BillingRepository) LinkBillingCustomer(ctx context.Context, tc *postgres.TenantConn, ref string) error {
	n, err := gen.New(tc).SetTenantBillingRef(ctx, gen.SetTenantBillingRefParams{
		ID:                 tc.TenantID(),
		BillingCustomerRef: &ref,
	})
	if err != nil {
		return translate(err)
	}
	if n == 0 {
		return shared.ErrNotFound
	}
	return nil
}

// StuckDelivery is a relay event reclaimed by the sweeper: it was claimed but
// never settled, so the process handling it died mid-flight.
type StuckDelivery struct {
	EventID   string
	EventType string
	Payload   []byte
	Attempts  int32
}

// ReclaimStuck takes a batch of deliveries left in 'processing' past the
// handler's deadline.
//
// The receiver claims an event before it processes it, so a process that dies
// between those two steps leaves a row that will never settle - and because
// the claim is the idempotency gate, the gateway's redelivery of that same
// event is then discarded as a duplicate. Without this sweep, one crash at the
// wrong moment silently drops a subscription change forever.
//
// FOR UPDATE SKIP LOCKED inside the statement means two sweepers take disjoint
// batches, and bumping attempts is what lets the caller give up on an event
// that fails every time rather than retrying it until the end of the world.
func (r *BillingRepository) ReclaimStuck(ctx context.Context, db *postgres.DB, staleAfter time.Duration, batch int32) ([]StuckDelivery, error) {
	var out []StuckDelivery

	err := db.WithSystemTx(ctx, postgres.Binding{}, "reclaim stuck billing relay deliveries",
		func(ctx context.Context, tc *postgres.TenantConn) error {
			rows, err := gen.New(tc).ReclaimStuckBillingEvents(ctx, gen.ReclaimStuckBillingEventsParams{
				StaleAfter: interval(staleAfter),
				BatchSize:  batch,
			})
			if err != nil {
				return translate(err)
			}
			out = make([]StuckDelivery, len(rows))
			for i, row := range rows {
				out[i] = StuckDelivery{
					EventID:   row.EventID,
					EventType: row.EventType,
					Payload:   row.Payload,
					Attempts:  row.Attempts,
				}
			}
			return nil
		})
	return out, err
}

// TenantsWithBilling lists tenants that have a gateway customer reference.
//
// It lives here rather than on the budget repository - where it was originally
// written - because it is a billing concern and its only caller is the
// reconciliation sweep.
func (r *BillingRepository) TenantsWithBilling(ctx context.Context, db *postgres.DB) (map[uuid.UUID]string, error) {
	out := make(map[uuid.UUID]string)

	err := db.WithSystemTx(ctx, postgres.Binding{ReadOnly: true}, "enumerate tenants for billing reconciliation",
		func(ctx context.Context, tc *postgres.TenantConn) error {
			rows, err := tc.Query(ctx,
				`SELECT id, billing_customer_ref FROM tenants
				  WHERE billing_customer_ref IS NOT NULL AND status <> 'cancelled'`)
			if err != nil {
				return translate(err)
			}
			defer rows.Close()

			for rows.Next() {
				var (
					id  uuid.UUID
					ref string
				)
				if err := rows.Scan(&id, &ref); err != nil {
					return translate(err)
				}
				out[id] = ref
			}
			return translate(rows.Err())
		})
	return out, err
}

// interval converts a Go duration into the pgtype the generated code expects.
// Microseconds is PostgreSQL's own resolution for the type, so nothing is lost.
func interval(d time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: d.Microseconds(), Valid: true}
}
