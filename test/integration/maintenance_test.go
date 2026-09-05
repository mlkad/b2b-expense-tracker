//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/gateway"
	"github.com/mlkad/b2b-expense-tracker/internal/logger"
	repo "github.com/mlkad/b2b-expense-tracker/internal/repository/postgres"
	"github.com/mlkad/b2b-expense-tracker/internal/service"
)

// billingServiceForTest builds the service with no gateway client. Only the
// paths that read the local projection are exercised here; the ones that call
// the gateway are not reachable without a client and are covered by the
// contract tests in internal/gateway.
func billingServiceForTest(t *testing.T) *service.BillingService {
	t.Helper()
	tenancy := repo.NewTenancyRepository()
	return service.NewBillingService(
		service.NewScope(app, tenancy),
		repo.NewBillingRepository(),
		tenancy,
		nil,
		logger.New(logger.ParseLevel("error"), logger.FormatText, "integration", "test"),
	)
}

func ownerDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", ownerDSN)
	if err != nil {
		t.Fatalf("open owner connection: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func signedEventBody(t *testing.T, eventID, tenantRef, status string, at time.Time) []byte {
	t.Helper()
	body, err := json.Marshal(gateway.Event{
		ID:        eventID,
		Type:      gateway.EventSubscriptionUpdated,
		CreatedAt: at,
		TenantRef: tenantRef,
		Subscription: &gateway.Subscription{
			SubscriptionID:     "sub_" + eventID,
			CustomerRef:        tenantRef,
			PlanCode:           "growth",
			Status:             status,
			Seats:              10,
			CurrentPeriodStart: at,
			CurrentPeriodEnd:   at.Add(30 * 24 * time.Hour),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// The failure this sweep exists for.
//
// The receiver claims an event id before processing it, because claiming after
// verification is what stops a forged id from getting a genuine delivery
// discarded as a duplicate. The cost of that ordering is this: a process that
// dies between the claim and the settle leaves a row that no redelivery can
// get past, since the gateway's retry now looks like a duplicate. The
// subscription silently stops updating and nothing reports it.
func TestRelaySweepRecoversADeliveryThatWasNeverSettled(t *testing.T) {
	o := seedOrg(t, "sweep-recover")
	linkBilling(t, o.TenantID, o.TenantID.String())

	billingRepo := repo.NewBillingRepository()
	svc := billingServiceForTest(t)
	ctx := context.Background()

	eventID := "evt_" + uuid.NewString()
	at := time.Now().UTC()
	body := signedEventBody(t, eventID, o.TenantID.String(), "active", at)

	// Claim it and then stop, exactly as a process dying mid-handler would.
	if err := billingRepo.ClaimDelivery(ctx, app, eventID, gateway.EventSubscriptionUpdated, body); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// The gateway's redelivery is now discarded - this is the trap.
	if err := billingRepo.ClaimDelivery(ctx, app, eventID, gateway.EventSubscriptionUpdated, body); err == nil {
		t.Fatal("a redelivery was accepted; the claim is not acting as an idempotency gate")
	}
	if n := countAsOwner(t, `SELECT count(*) FROM tenant_subscriptions WHERE tenant_id = $1`, o.TenantID); n != 0 {
		t.Fatal("the projection was written despite the handler never completing")
	}

	// Age the row past the staleness window.
	if _, err := ownerDB(t).Exec(
		`UPDATE billing_events SET received_at = now() - interval '1 hour' WHERE event_id = $1`,
		eventID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	stuck, err := svc.ReclaimStuckDeliveries(ctx, 5*time.Minute, 50)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(stuck) != 1 || stuck[0].EventID != eventID {
		t.Fatalf("reclaimed %d deliveries, want the one that was stuck", len(stuck))
	}
	if stuck[0].Attempts != 2 {
		t.Errorf("attempts = %d after one reclaim, want 2", stuck[0].Attempts)
	}

	outcome, err := svc.ReapplyStuck(ctx, stuck[0])
	if err != nil {
		t.Fatalf("reapply: %v", err)
	}
	if outcome != service.RelayApplied {
		t.Fatalf("outcome = %s, want applied", outcome)
	}

	// The projection caught up, and the ledger row left 'processing' so the
	// next sweep does not pick it up and apply it a second time.
	var status string
	if err := ownerDB(t).QueryRow(
		`SELECT status FROM tenant_subscriptions WHERE tenant_id = $1`, o.TenantID).Scan(&status); err != nil {
		t.Fatalf("read projection: %v", err)
	}
	if status != "active" {
		t.Fatalf("projection status = %q, want active", status)
	}

	var ledgerStatus string
	if err := ownerDB(t).QueryRow(
		`SELECT status FROM billing_events WHERE event_id = $1`, eventID).Scan(&ledgerStatus); err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if ledgerStatus != "succeeded" {
		t.Fatalf("ledger status = %q, want succeeded", ledgerStatus)
	}

	// And a second sweep finds nothing, because the row is settled.
	again, err := svc.ReclaimStuckDeliveries(ctx, 5*time.Minute, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range again {
		if d.EventID == eventID {
			t.Fatal("a settled delivery was reclaimed again; it would be applied twice")
		}
	}
}

// A delivery that fails every time must eventually be abandoned. Retrying it
// forever keeps a row in 'processing', which is indistinguishable from a
// genuinely stuck one and hides the next real failure behind it.
func TestRelaySweepGivesUpAfterTooManyAttempts(t *testing.T) {
	o := seedOrg(t, "sweep-giveup")
	linkBilling(t, o.TenantID, o.TenantID.String())

	billingRepo := repo.NewBillingRepository()
	svc := billingServiceForTest(t)
	ctx := context.Background()

	eventID := "evt_" + uuid.NewString()
	body := signedEventBody(t, eventID, o.TenantID.String(), "active", time.Now().UTC())

	if err := billingRepo.ClaimDelivery(ctx, app, eventID, gateway.EventSubscriptionUpdated, body); err != nil {
		t.Fatalf("claim: %v", err)
	}

	outcome, err := svc.ReapplyStuck(ctx, repo.StuckDelivery{
		EventID:   eventID,
		EventType: gateway.EventSubscriptionUpdated,
		Payload:   body,
		Attempts:  service.MaxRelayAttempts + 1,
	})
	if err != nil {
		t.Fatalf("reapply: %v", err)
	}
	if outcome != service.RelaySkipped {
		t.Fatalf("outcome = %s, want skipped", outcome)
	}

	var status string
	var detail *string
	if err := ownerDB(t).QueryRow(
		`SELECT status, error_detail FROM billing_events WHERE event_id = $1`, eventID,
	).Scan(&status, &detail); err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if status != "failed" {
		t.Fatalf("status = %q, want failed", status)
	}
	if detail == nil || *detail == "" {
		t.Error("an abandoned delivery must record why, or nobody can tell it from a transient failure")
	}
}

// A stored payload that cannot be decoded must be abandoned rather than retried
// forever - it will not become decodable.
func TestRelaySweepAbandonsAnUnreadablePayload(t *testing.T) {
	billingRepo := repo.NewBillingRepository()
	svc := billingServiceForTest(t)
	ctx := context.Background()

	eventID := "evt_" + uuid.NewString()
	// Valid JSON for the column's jsonb type, but not an event.
	payload := []byte(`{"not":"an event"}`)
	if err := billingRepo.ClaimDelivery(ctx, app, eventID, "subscription.updated", payload); err != nil {
		t.Fatalf("claim: %v", err)
	}

	outcome, err := svc.ReapplyStuck(ctx, repo.StuckDelivery{
		EventID:   eventID,
		EventType: "subscription.updated",
		Payload:   []byte(`{"id": [broken`),
		Attempts:  1,
	})
	if err != nil {
		t.Fatalf("reapply: %v", err)
	}
	if outcome != service.RelaySkipped {
		t.Fatalf("outcome = %s, want skipped", outcome)
	}

	var status string
	if err := ownerDB(t).QueryRow(
		`SELECT status FROM billing_events WHERE event_id = $1`, eventID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("status = %q, want failed", status)
	}
}

// The reconciliation sweep has to find every tenant that could have drifted,
// and no others.
func TestTenantsToReconcileCoversLinkedTenantsOnly(t *testing.T) {
	linked := seedOrg(t, "reconcile-linked")
	unlinked := seedOrg(t, "reconcile-unlinked")
	linkBilling(t, linked.TenantID, linked.TenantID.String())

	tenants, err := billingServiceForTest(t).TenantsToReconcile(context.Background())
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}

	if ref, ok := tenants[linked.TenantID]; !ok || ref != linked.TenantID.String() {
		t.Errorf("a tenant with a billing reference was not scheduled for reconciliation (ref=%q ok=%v)", ref, ok)
	}
	if _, ok := tenants[unlinked.TenantID]; ok {
		t.Error("a tenant that never started a checkout was scheduled for reconciliation")
	}
}

// Refresh tokens are written on every login and, until the cleanup job existed,
// nothing ever removed one - so the table grew with the product's entire
// history of sign-ins rather than with its live sessions.
func TestSessionCleanupPurgesOnlyBeyondTheGrace(t *testing.T) {
	tenancy := repo.NewTenancyRepository()
	ctx := context.Background()
	db := ownerDB(t)

	var userID uuid.UUID
	if err := db.QueryRow(
		`INSERT INTO users (email) VALUES ($1) RETURNING id`,
		"purge-"+uuid.NewString()+"@example.test").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM users WHERE id = $1`, userID) })

	grace := 30 * 24 * time.Hour
	family := uuid.New()

	insert := func(label string, expiredAgo time.Duration) {
		t.Helper()
		digest := make([]byte, 32)
		copy(digest, uuid.New().String())
		if _, err := db.Exec(
			`INSERT INTO refresh_tokens (user_id, token_hash, family_id, issued_at, expires_at)
			 VALUES ($1, $2, $3, now() - $4::interval - interval '1 day', now() - $4::interval)`,
			userID, digest, family, expiredAgo.String()); err != nil {
			t.Fatalf("seed token (%s): %v", label, err)
		}
	}

	insert("just expired", time.Hour)                  // inside the grace: keep
	insert("expired inside grace", grace-24*time.Hour) // inside: keep
	insert("long expired", grace+24*time.Hour)         // outside: purge

	before := countAsOwner(t, `SELECT count(*) FROM refresh_tokens WHERE user_id = $1`, userID)
	if before != 3 {
		t.Fatalf("seeded %d tokens, want 3", before)
	}

	deleted, err := tenancy.PurgeExpiredRefreshTokens(ctx, app, grace)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if deleted < 1 {
		t.Fatalf("purged %d tokens, want at least the one outside the grace", deleted)
	}

	after := countAsOwner(t, `SELECT count(*) FROM refresh_tokens WHERE user_id = $1`, userID)
	if after != 2 {
		t.Fatalf("%d tokens survived, want 2: the grace period must keep recently expired rows "+
			"so an investigation can tell a revoked token from an expired one", after)
	}
}
