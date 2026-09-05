// Package billing decides what a tenant is entitled to.
//
// It holds no network code and no database code: the subscription state
// arrives as a value, and this package answers questions about it. That is
// what makes the entitlement matrix testable without a Stripe key and without
// standing up the payment gateway.
//
// The subscription itself is owned by the Stripe Payment & Subscription
// Gateway (project #1). This service keeps a local projection of it and never
// calls that service on the request path - the same argument the gateway makes
// for not calling Stripe on its own request path. A billing outage must not be
// able to lock every customer out of their expense data.
package billing

import (
	"time"
)

// PlanCode identifies a product tier. The values are the price nicknames
// configured in the gateway; they arrive in the relayed event and are matched
// here.
type PlanCode string

const (
	PlanFree       PlanCode = "free"
	PlanStarter    PlanCode = "starter"
	PlanGrowth     PlanCode = "growth"
	PlanEnterprise PlanCode = "enterprise"
)

// Status mirrors the gateway's subscription_status, which mirrors Stripe's.
//
// It is a string rather than an enum with a closed Valid() set, for the same
// reason the column is TEXT with a CHECK: the gateway owns this vocabulary,
// and a value it adds must not make this service reject deliveries until it
// is redeployed.
type Status string

const (
	StatusIncomplete        Status = "incomplete"
	StatusIncompleteExpired Status = "incomplete_expired"
	StatusTrialing          Status = "trialing"
	StatusActive            Status = "active"
	StatusPastDue           Status = "past_due"
	StatusCanceled          Status = "canceled"
	StatusUnpaid            Status = "unpaid"
	StatusPaused            Status = "paused"
)

// IsLive reports whether the subscription should grant its plan's
// entitlements.
//
// past_due is included. Stripe is still retrying the payment during dunning,
// and revoking a finance team's access to their own expense records because a
// card expired is how a recoverable billing problem becomes a cancellation.
// The gateway makes the same call for the same reason.
func (s Status) IsLive() bool {
	switch s {
	case StatusTrialing, StatusActive, StatusPastDue:
		return true
	}
	return false
}

// Feature is a capability a plan may or may not include.
type Feature string

const (
	FeatureStreamingExport   Feature = "streaming_export"
	FeatureScheduledReports  Feature = "scheduled_reports"
	FeatureVendorSubTracking Feature = "vendor_subscription_tracking"
	FeatureDepartmentBudgets Feature = "department_budgets"
	FeatureApprovalChains    Feature = "approval_chains"
	FeatureAuditExport       Feature = "audit_export"
	FeatureAPIAccess         Feature = "api_access"
	FeatureSSO               Feature = "sso"
)

// Limits are the countable caps. A negative value means unlimited, which is
// checked with a helper rather than by every call site remembering the
// convention.
type Limits struct {
	Seats               int
	Departments         int
	VendorSubscriptions int

	// ExportRows caps a single synchronous export. Beyond it the request is
	// handed to the worker and delivered as a link, because a browser holding
	// a connection open for six minutes is a request that a load balancer
	// eventually kills half-written.
	ExportRows int
}

const Unlimited = -1

func Within(limit, count int) bool { return limit == Unlimited || count <= limit }

// plan is one row of the product matrix.
type plan struct {
	Limits   Limits
	Features map[Feature]struct{}
}

// plans is the product, written down.
//
// The free tier is not a marketing decision here so much as a safety one: it
// is what every tenant falls back to when their subscription lapses, so it has
// to be a tier in which their existing data stays readable and exportable.
// Locking a customer out of records they are legally required to keep, because
// a card expired, is not a lever any product should pull.
var plans = map[PlanCode]plan{
	PlanFree: {
		Limits: Limits{Seats: 3, Departments: 1, VendorSubscriptions: 5, ExportRows: 1_000},
		Features: features(
			FeatureStreamingExport,
		),
	},
	PlanStarter: {
		Limits: Limits{Seats: 10, Departments: 5, VendorSubscriptions: 50, ExportRows: 25_000},
		Features: features(
			FeatureStreamingExport, FeatureVendorSubTracking, FeatureDepartmentBudgets,
		),
	},
	PlanGrowth: {
		Limits: Limits{Seats: 50, Departments: 25, VendorSubscriptions: 500, ExportRows: 250_000},
		Features: features(
			FeatureStreamingExport, FeatureVendorSubTracking, FeatureDepartmentBudgets,
			FeatureScheduledReports, FeatureApprovalChains, FeatureAuditExport, FeatureAPIAccess,
		),
	},
	PlanEnterprise: {
		Limits: Limits{Seats: Unlimited, Departments: Unlimited, VendorSubscriptions: Unlimited, ExportRows: Unlimited},
		Features: features(
			FeatureStreamingExport, FeatureVendorSubTracking, FeatureDepartmentBudgets,
			FeatureScheduledReports, FeatureApprovalChains, FeatureAuditExport, FeatureAPIAccess,
			FeatureSSO,
		),
	},
}

func features(fs ...Feature) map[Feature]struct{} {
	m := make(map[Feature]struct{}, len(fs))
	for _, f := range fs {
		m[f] = struct{}{}
	}
	return m
}

// Entitlement is the local projection of a tenant's subscription, as relayed
// from the gateway.
type Entitlement struct {
	Plan   PlanCode
	Status Status

	// Seats is what the customer is paying for, which can be fewer than the
	// plan's ceiling. The effective limit is the smaller of the two.
	Seats int

	CurrentPeriodEnd  time.Time
	CancelAtPeriodEnd bool
	TrialEnd          *time.Time

	// Known is false when no subscription row exists at all - a tenant that
	// signed up and never started a checkout. It is not the same as a lapsed
	// subscription, and the difference matters to the dashboard, which should
	// offer a first checkout rather than a payment update.
	Known bool
}

// FreeEntitlement is what an unknown or lapsed tenant gets.
func FreeEntitlement() Entitlement {
	return Entitlement{Plan: PlanFree, Status: StatusCanceled, Seats: plans[PlanFree].Limits.Seats}
}

// EffectivePlan resolves what the tenant may actually use right now.
//
// A subscription that is not live degrades to free rather than to nothing.
// The customer keeps their history, keeps read access, and keeps the ability
// to export it - and loses the seats and the features they stopped paying for.
func (e Entitlement) EffectivePlan() PlanCode {
	if !e.Known || !e.Status.IsLive() {
		return PlanFree
	}
	if _, ok := plans[e.Plan]; !ok {
		// The gateway sent a plan code this build does not know. Falling back
		// to free would punish a paying customer for a deployment ordering
		// problem; starter is the lowest paid tier and the safe assumption for
		// someone whose payment is live.
		return PlanStarter
	}
	return e.Plan
}

// Allows reports whether a feature is included.
func (e Entitlement) Allows(f Feature) bool {
	_, ok := plans[e.EffectivePlan()].Features[f]
	return ok
}

// Limits returns the effective caps, with the purchased seat count applied.
func (e Entitlement) Limits() Limits {
	l := plans[e.EffectivePlan()].Limits
	if e.Status.IsLive() && e.Seats > 0 && l.Seats != Unlimited && e.Seats < l.Seats {
		l.Seats = e.Seats
	}
	if e.Status.IsLive() && e.Seats > 0 && l.Seats == Unlimited {
		l.Seats = e.Seats
	}
	return l
}

// InGracePeriod reports whether the subscription is live but failing to
// collect. The dashboard shows a banner; nothing is restricted yet.
func (e Entitlement) InGracePeriod() bool { return e.Status == StatusPastDue }

// NeedsCheckout reports whether the tenant has never subscribed, as distinct
// from having lapsed.
func (e Entitlement) NeedsCheckout() bool { return !e.Known }
