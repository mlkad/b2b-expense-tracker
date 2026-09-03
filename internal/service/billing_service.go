package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/auth"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/billing"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/tenant"
	"github.com/mlkad/b2b-expense-tracker/internal/gateway"
	"github.com/mlkad/b2b-expense-tracker/internal/platform/postgres"
	repo "github.com/mlkad/b2b-expense-tracker/internal/repository/postgres"
	"github.com/mlkad/b2b-expense-tracker/internal/repository/postgres/gen"
)

// BillingService owns the local subscription projection and the two calls that
// genuinely need the payment gateway.
type BillingService struct {
	scope   *Scope
	billing *repo.BillingRepository
	tenancy *repo.TenancyRepository
	client  *gateway.Client
	log     *slog.Logger
}

func NewBillingService(
	scope *Scope,
	billingRepo *repo.BillingRepository,
	tenancy *repo.TenancyRepository,
	client *gateway.Client,
	log *slog.Logger,
) *BillingService {
	return &BillingService{scope: scope, billing: billingRepo, tenancy: tenancy, client: client, log: log}
}

// Entitlement answers what the caller's tenant may use.
func (s *BillingService) Entitlement(ctx context.Context, subject auth.Subject) (billing.Entitlement, error) {
	var e billing.Entitlement
	err := s.scope.Read(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, _ tenant.Actor) error {
		var err error
		e, err = s.billing.GetEntitlement(ctx, tc)
		return err
	})
	return e, err
}

// StartCheckout opens a Stripe Checkout session through the gateway.
func (s *BillingService) StartCheckout(ctx context.Context, subject auth.Subject, req gateway.CheckoutRequest) (*gateway.CheckoutSession, error) {
	if s.client == nil {
		return nil, fmt.Errorf("%w: billing is not configured on this deployment", gateway.ErrUnavailable)
	}

	var (
		customerRef string
		tenantID    uuid.UUID
	)

	// The gateway call is made outside the transaction, deliberately.
	//
	// Holding a database transaction open across a network call to another
	// service means a pool slot is pinned for the duration of that service's
	// latency - and during an incident, for the duration of its timeout. A few
	// hundred concurrent checkouts against a slow gateway would exhaust the
	// pool and take down every endpoint, including the ones with no billing
	// involvement at all.
	err := s.scope.Read(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		if err := Require(actor, tenant.PermBillingManage); err != nil {
			return err
		}
		t, err := s.tenancy.GetTenant(ctx, tc)
		if err != nil {
			return err
		}
		tenantID = t.ID
		if t.BillingCustomerRef != nil {
			customerRef = *t.BillingCustomerRef
		} else {
			// First checkout. The tenant id doubles as the gateway's customer
			// reference, which makes the mapping derivable rather than stored
			// twice and keeps a relayed event resolvable even if the link
			// write below were ever lost.
			customerRef = t.ID.String()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	session, err := s.client.StartCheckout(ctx, tenantID, customerRef, req)
	if err != nil {
		return nil, err
	}

	// Record the link only after the gateway has accepted, so a tenant is
	// never pointed at a customer reference the gateway does not know.
	if err := s.scope.Write(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, _ tenant.Actor) error {
		return s.billing.LinkBillingCustomer(ctx, tc, customerRef)
	}); err != nil {
		// The session is live and the user can pay. Failing the request now
		// would send them round again for a session they already have. The
		// relayed event carries the customer reference, so the link is
		// recovered when the subscription arrives.
		s.log.WarnContext(ctx, "checkout started but the customer link was not recorded",
			slog.String("tenant_id", tenantID.String()),
			slog.String("error", err.Error()))
	}

	return session, nil
}

// OpenPortal returns a Stripe Customer Portal link.
func (s *BillingService) OpenPortal(ctx context.Context, subject auth.Subject, returnURL string) (*gateway.PortalSession, error) {
	if s.client == nil {
		return nil, fmt.Errorf("%w: billing is not configured on this deployment", gateway.ErrUnavailable)
	}

	var (
		customerRef string
		tenantID    uuid.UUID
	)
	err := s.scope.Read(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		if err := Require(actor, tenant.PermBillingManage); err != nil {
			return err
		}
		t, err := s.tenancy.GetTenant(ctx, tc)
		if err != nil {
			return err
		}
		if t.BillingCustomerRef == nil {
			return fmt.Errorf("%w: this organisation has no subscription to manage", shared.ErrNotFound)
		}
		tenantID, customerRef = t.ID, *t.BillingCustomerRef
		return nil
	})
	if err != nil {
		return nil, err
	}

	return s.client.OpenPortal(ctx, tenantID, customerRef, returnURL)
}

// -----------------------------------------------------------------------------
// Relay ingestion
// -----------------------------------------------------------------------------

// RelayOutcome is what the receiver tells the gateway.
type RelayOutcome string

const (
	RelayApplied   RelayOutcome = "applied"
	RelayDuplicate RelayOutcome = "duplicate"
	RelaySkipped   RelayOutcome = "skipped"
)

// IngestRelayedEvent applies a verified event to the local projection.
//
// The caller has already verified the HMAC. The order of what follows is the
// security property, and it is the same lesson the payment gateway learned
// against Stripe: verify, then claim, then process. Claiming an unverified
// event id lets anyone POST a guess, plant a settled row, and have the genuine
// delivery answered 200 and dropped as a duplicate.
//
// Every branch that returns nil is a branch where the gateway should stop
// redelivering. A duplicate, a stale event and an unrecognised type are all
// successful outcomes: the alternative is a redelivery backlog that grows
// until someone notices.
func (s *BillingService) IngestRelayedEvent(ctx context.Context, event *gateway.Event, rawBody []byte) (RelayOutcome, error) {
	db := s.scope.DB()

	if err := s.billing.ClaimDelivery(ctx, db, event.ID, event.Type, rawBody); err != nil {
		if errors.Is(err, repo.ErrDuplicateDelivery) {
			return RelayDuplicate, nil
		}
		return "", err
	}

	outcome, err := s.applyEvent(ctx, event)

	status := gen.BillingEventStatusSucceeded
	switch {
	case err != nil:
		status = gen.BillingEventStatusFailed
	case outcome == RelaySkipped:
		status = gen.BillingEventStatusSkipped
	}

	var tenantID *uuid.UUID
	if id, resolveErr := s.billing.ResolveTenantByBillingRef(ctx, db, event.TenantRef); resolveErr == nil {
		tenantID = &id
	}

	// Settling uses a context detached from the request's cancellation. If the
	// gateway hangs up after the projection was written, the ledger row must
	// still leave 'processing' - otherwise the sweeper reclaims an event that
	// was in fact applied, and applies it twice.
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if settleErr := s.billing.SettleDelivery(settleCtx, db, event.ID, tenantID, status, err); settleErr != nil {
		s.log.ErrorContext(ctx, "billing delivery applied but not settled in the ledger",
			slog.String("event_id", event.ID),
			slog.String("error", settleErr.Error()))
	}

	return outcome, err
}

func (s *BillingService) applyEvent(ctx context.Context, event *gateway.Event) (RelayOutcome, error) {
	switch event.Type {
	case gateway.EventSubscriptionCreated,
		gateway.EventSubscriptionUpdated,
		gateway.EventSubscriptionDeleted,
		gateway.EventPaymentFailed,
		gateway.EventPaymentSucceeded:
	default:
		// Acknowledged, not rejected. An unknown type this build does not
		// handle is a deployment-ordering fact, not a delivery failure, and
		// answering non-2xx would make the gateway retry it for three days.
		s.log.InfoContext(ctx, "ignoring unrecognised billing event type",
			slog.String("type", event.Type), slog.String("event_id", event.ID))
		return RelaySkipped, nil
	}

	if event.Subscription == nil {
		return RelaySkipped, nil
	}

	db := s.scope.DB()

	tenantID, err := s.billing.ResolveTenantByBillingRef(ctx, db, event.TenantRef)
	if err != nil {
		if errors.Is(err, shared.ErrNotFound) {
			// A subscription for a tenant this service does not have. It
			// happens when the gateway serves more than one product, and it is
			// not an error here.
			s.log.InfoContext(ctx, "billing event for an unknown tenant reference",
				slog.String("tenant_ref", event.TenantRef), slog.String("event_id", event.ID))
			return RelaySkipped, nil
		}
		return "", err
	}

	sub := event.Subscription
	state := repo.SubscriptionState{
		GatewaySubscriptionID: sub.SubscriptionID,
		GatewayCustomerRef:    event.TenantRef,
		PlanCode:              sub.PlanCode,
		Status:                sub.Status,
		Seats:                 int32(max(sub.Seats, 1)),
		CurrentPeriodStart:    sub.CurrentPeriodStart,
		CurrentPeriodEnd:      sub.CurrentPeriodEnd,
		CancelAtPeriodEnd:     sub.CancelAtPeriodEnd,
		TrialEnd:              sub.TrialEnd,
		EventID:               event.ID,
		EventAt:               event.CreatedAt,
	}

	if err := s.billing.ApplySubscription(ctx, db, tenantID, state); err != nil {
		if errors.Is(err, repo.ErrStaleEvent) {
			// The expected outcome of an unordered stream. Acknowledge.
			return RelaySkipped, nil
		}
		return "", err
	}

	s.log.InfoContext(ctx, "subscription projection updated",
		slog.String("tenant_id", tenantID.String()),
		slog.String("status", sub.Status),
		slog.String("plan", sub.PlanCode))

	return RelayApplied, nil
}

// Reconcile repairs drift between the projection and the gateway.
//
// It exists because the relay is at-least-once but not guaranteed: a delivery
// during a deployment window, or one that failed every retry, leaves this
// service's projection behind. Nothing on the request path notices - the
// entitlement read is local and looks perfectly healthy - so the only way that
// error is ever found is by asking. The nightly worker calls this per tenant.
func (s *BillingService) Reconcile(ctx context.Context, tenantID uuid.UUID, customerRef string) error {
	if s.client == nil {
		return nil
	}

	sub, err := s.client.GetSubscription(ctx, tenantID, customerRef)
	if err != nil {
		if errors.Is(err, gateway.ErrNotFound) {
			return nil
		}
		return err
	}

	// Stamped with now rather than with an event time. Reconciliation is by
	// definition the most recent information available, so it must win the
	// out-of-order comparison against anything already applied.
	return s.billing.ApplySubscription(ctx, s.scope.DB(), tenantID, repo.SubscriptionState{
		GatewaySubscriptionID: sub.SubscriptionID,
		GatewayCustomerRef:    customerRef,
		PlanCode:              sub.PlanCode,
		Status:                sub.Status,
		Seats:                 int32(max(sub.Seats, 1)),
		CurrentPeriodStart:    sub.CurrentPeriodStart,
		CurrentPeriodEnd:      sub.CurrentPeriodEnd,
		CancelAtPeriodEnd:     sub.CancelAtPeriodEnd,
		TrialEnd:              sub.TrialEnd,
		EventID:               "reconcile:" + time.Now().UTC().Format(time.RFC3339),
		EventAt:               time.Now().UTC(),
	})
}
