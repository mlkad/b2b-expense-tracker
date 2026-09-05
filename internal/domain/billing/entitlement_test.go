package billing

import (
	"testing"
	"time"
)

// past_due must still grant the plan. Revoking access while Stripe is retrying
// the payment is how a recoverable billing problem becomes a cancellation.
func TestLiveStatuses(t *testing.T) {
	live := map[Status]bool{
		StatusTrialing: true, StatusActive: true, StatusPastDue: true,
		StatusIncomplete: false, StatusIncompleteExpired: false,
		StatusCanceled: false, StatusUnpaid: false, StatusPaused: false,
	}
	for status, want := range live {
		if got := status.IsLive(); got != want {
			t.Errorf("%s.IsLive() = %v, want %v", status, got, want)
		}
	}
}

// A lapsed subscription degrades to free rather than to nothing. Locking a
// company out of records it is legally required to retain, because a card
// expired, is not a lever this product pulls.
func TestLapsedDegradesToFreeRatherThanLockout(t *testing.T) {
	for _, dead := range []Status{StatusCanceled, StatusUnpaid, StatusIncompleteExpired, StatusPaused} {
		e := Entitlement{Plan: PlanEnterprise, Status: dead, Seats: 500, Known: true}

		if got := e.EffectivePlan(); got != PlanFree {
			t.Errorf("status %s resolved to plan %s, want free", dead, got)
		}
		// Read and export must survive, or the customer cannot get their own
		// data out.
		if !e.Allows(FeatureStreamingExport) {
			t.Errorf("status %s lost export; a customer must be able to retrieve their records", dead)
		}
		if e.Allows(FeatureSSO) || e.Allows(FeatureAPIAccess) {
			t.Errorf("status %s kept a paid feature", dead)
		}
	}
}

func TestUnknownTenantIsFreeAndNeedsCheckout(t *testing.T) {
	e := FreeEntitlement()
	if e.Known {
		t.Error("the fallback must not claim to know a subscription")
	}
	if !e.NeedsCheckout() {
		t.Error("a tenant that never subscribed must be offered a first checkout, not a payment update")
	}
	if e.EffectivePlan() != PlanFree {
		t.Errorf("plan = %s, want free", e.EffectivePlan())
	}
}

// A plan code this build does not recognise must not punish a paying customer
// for a deployment ordering problem.
func TestUnknownPlanFromTheGatewayFallsBackToTheLowestPaidTier(t *testing.T) {
	e := Entitlement{Plan: PlanCode("platinum-2027"), Status: StatusActive, Seats: 5, Known: true}
	if got := e.EffectivePlan(); got != PlanStarter {
		t.Fatalf("unknown plan resolved to %s, want starter - a live payment must not degrade to free", got)
	}
}

func TestSeatsAreTheSmallerOfPurchasedAndPlanCeiling(t *testing.T) {
	t.Run("purchased fewer than the ceiling", func(t *testing.T) {
		e := Entitlement{Plan: PlanGrowth, Status: StatusActive, Seats: 7, Known: true}
		if got := e.Limits().Seats; got != 7 {
			t.Fatalf("seats = %d, want the purchased 7", got)
		}
	})

	t.Run("purchased seats apply even on an unlimited plan", func(t *testing.T) {
		e := Entitlement{Plan: PlanEnterprise, Status: StatusActive, Seats: 250, Known: true}
		if got := e.Limits().Seats; got != 250 {
			t.Fatalf("seats = %d, want the purchased 250; billing for 250 must not grant unlimited", got)
		}
	})

	t.Run("a lapsed subscription drops to the free ceiling", func(t *testing.T) {
		e := Entitlement{Plan: PlanEnterprise, Status: StatusCanceled, Seats: 250, Known: true}
		if got := e.Limits().Seats; got != plans[PlanFree].Limits.Seats {
			t.Fatalf("seats = %d, want the free tier ceiling", got)
		}
	})
}

func TestFeatureMatrixIsCumulative(t *testing.T) {
	tiers := []PlanCode{PlanFree, PlanStarter, PlanGrowth, PlanEnterprise}

	for i := 1; i < len(tiers); i++ {
		lower, higher := tiers[i-1], tiers[i]
		for feature := range plans[lower].Features {
			if _, ok := plans[higher].Features[feature]; !ok {
				t.Errorf("%s includes %s but %s does not; upgrading must never remove a feature",
					lower, feature, higher)
			}
		}
	}
}

func TestLimitsGrowWithThePlan(t *testing.T) {
	tiers := []PlanCode{PlanFree, PlanStarter, PlanGrowth, PlanEnterprise}

	atLeast := func(higher, lower int) bool {
		if higher == Unlimited {
			return true
		}
		if lower == Unlimited {
			return false
		}
		return higher >= lower
	}

	for i := 1; i < len(tiers); i++ {
		lo, hi := plans[tiers[i-1]].Limits, plans[tiers[i]].Limits
		if !atLeast(hi.Seats, lo.Seats) ||
			!atLeast(hi.Departments, lo.Departments) ||
			!atLeast(hi.VendorSubscriptions, lo.VendorSubscriptions) ||
			!atLeast(hi.ExportRows, lo.ExportRows) {
			t.Errorf("%s has a smaller limit than %s: %+v vs %+v", tiers[i], tiers[i-1], hi, lo)
		}
	}
}

func TestWithin(t *testing.T) {
	if !Within(Unlimited, 1_000_000) {
		t.Error("Unlimited must accept any count")
	}
	if !Within(3, 3) {
		t.Error("a count exactly at the limit must be allowed")
	}
	if Within(3, 4) {
		t.Error("a count above the limit must be refused")
	}
}

func TestGracePeriodIsReportedButNotRestricted(t *testing.T) {
	e := Entitlement{Plan: PlanGrowth, Status: StatusPastDue, Seats: 10, Known: true,
		CurrentPeriodEnd: time.Now().Add(24 * time.Hour)}

	if !e.InGracePeriod() {
		t.Error("past_due must be reported as a grace period so the dashboard can warn")
	}
	if e.EffectivePlan() != PlanGrowth {
		t.Error("past_due must keep the plan while dunning runs")
	}
	if !e.Allows(FeatureAPIAccess) {
		t.Error("past_due must not restrict features yet")
	}
}
