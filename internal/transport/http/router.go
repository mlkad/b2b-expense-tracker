// Package http assembles the handler tree.
package http

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/expense"
	"github.com/mlkad/b2b-expense-tracker/internal/transport/http/handler"
	"github.com/mlkad/b2b-expense-tracker/internal/transport/http/middleware"
)

// RouterConfig carries the deadlines and policies the routes are mounted with.
type RouterConfig struct {
	APITimeout time.Duration

	// ExportTimeout bounds a streaming report. It is minutes rather than
	// seconds, and it is the reason the export routes are their own group: a
	// ten-minute deadline on the whole API would mean a stuck request holds a
	// pool slot for ten minutes.
	ExportTimeout time.Duration

	// RelayTimeout bounds a billing delivery. Longer than the API timeout
	// because a 5xx makes the gateway redeliver, so converting a slow-but-
	// succeeding delivery into a timeout builds a retry backlog.
	RelayTimeout time.Duration

	CORS           middleware.CORSConfig
	Tokens         middleware.TokenParser
	AuthRateLimit  *middleware.RateLimiter
	WriteRateLimit *middleware.RateLimiter
	TrustedProxies int
}

func (c RouterConfig) withDefaults() RouterConfig {
	if c.APITimeout <= 0 {
		c.APITimeout = 10 * time.Second
	}
	if c.ExportTimeout <= 0 {
		c.ExportTimeout = 10 * time.Minute
	}
	if c.RelayTimeout <= 0 {
		c.RelayTimeout = 25 * time.Second
	}
	return c
}

type Handlers struct {
	Auth     *handler.AuthHandler
	Expenses *handler.ExpenseHandler
	Exports  *handler.ExportHandler
	Billing  *handler.BillingHandler
	Org      *handler.OrgHandler
	Files    *handler.AttachmentHandler
	Account  *handler.AccountHandler
	Health   *handler.HealthHandler
}

// NewRouter assembles the tree. Dependencies arrive as arguments; nothing is
// read from package scope.
//
// The global chain is ordered outermost first, and the order is load-bearing:
//
//   - RequestID is outermost, so every record produced by anything below it -
//     including a panic report - carries the correlation id.
//   - AccessLog sits outside Recoverer. It reads the status after the inner
//     handler returns, so a panic unwinding through it would be logged as 200
//     or not at all. With Recoverer inside, the recovered 500 is already
//     written by the time AccessLog looks.
//   - SecurityHeaders is next, so the headers are set even on a 404 or a
//     recovered panic.
//   - CORS goes inside Recoverer but outside routing, so a preflight for an
//     unmatched path still gets its headers rather than a bare 404 that the
//     browser reports as a CORS failure.
//
// Timeout is applied per route group rather than globally, because the three
// classes of endpoint here need deadlines that differ by two orders of
// magnitude.
func NewRouter(h Handlers, cfg RouterConfig, log *slog.Logger) http.Handler {
	cfg = cfg.withDefaults()

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.AccessLog(log))
	r.Use(middleware.Recoverer(log))
	r.Use(middleware.SecurityHeaders)

	if len(cfg.CORS.AllowedOrigins) > 0 {
		r.Use(middleware.CORS(cfg.CORS))
	}

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		handler.WriteNotFound(w, r)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		handler.WriteMethodNotAllowed(w, r)
	})

	// Probes carry their own short deadline and no authentication: a readiness
	// endpoint that needs a token is one the orchestrator cannot use.
	r.Group(func(r chi.Router) {
		r.Use(middleware.Timeout(3 * time.Second))
		r.Get("/livez", h.Health.Live)
		r.Get("/readyz", h.Health.Ready)
	})

	// The billing relay. Outside /api/v1 because it is not part of the public
	// API and is authenticated by an HMAC rather than by a bearer token, and
	// deliberately not rate limited: a 429 makes the gateway redeliver, so
	// throttling it builds a backlog instead of shedding load.
	r.Group(func(r chi.Router) {
		r.Use(middleware.Timeout(cfg.RelayTimeout))
		r.Post("/internal/billing/relay", h.Billing.HandleRelay)
	})

	r.Route("/api/v1", func(r chi.Router) {
		// Public credential endpoints.
		//
		// Rate limited because they are the only routes an anonymous caller
		// can make do expensive work - a bcrypt comparison per request - and
		// the only ones where unlimited attempts mean unlimited password
		// guesses.
		r.Group(func(r chi.Router) {
			r.Use(middleware.Timeout(cfg.APITimeout))
			if cfg.AuthRateLimit != nil {
				r.Use(cfg.AuthRateLimit.Middleware(middleware.ClientKeyFunc(cfg.TrustedProxies)))
			}
			r.Post("/auth/register", h.Auth.Register)
			r.Post("/auth/login", h.Auth.Login)
			r.Post("/auth/refresh", h.Auth.Refresh)
		})

		// Logout takes no access token: a session whose access token has
		// already expired must still be endable.
		r.With(middleware.Timeout(cfg.APITimeout)).Post("/auth/logout", h.Auth.Logout)

		// Everything below requires a valid bearer token. RequireAuth is
		// applied to the group rather than per route, so a new endpoint is
		// protected by default rather than by somebody remembering.
		r.Group(func(r chi.Router) {
			r.Use(middleware.Timeout(cfg.APITimeout))
			r.Use(middleware.RequireAuth(cfg.Tokens, log))

			r.Get("/me", h.Account.Me)
			r.Get("/tenant", h.Account.GetOrganisation)
			r.Get("/auth/tenants", h.Auth.Tenants)
			r.Post("/auth/switch-tenant", h.Auth.SwitchTenant)

			r.Get("/expenses", h.Expenses.List)
			r.Get("/expenses/pending", h.Expenses.PendingQueue)
			r.Get("/expenses/{id}", h.Expenses.Get)
			r.Get("/expenses/{id}/history", h.Expenses.History)
			r.Get("/expenses/{id}/attachments", h.Files.List)
			// A redirect to a signed URL rather than a proxied download: the
			// bytes never come through this service.
			r.Get("/attachments/{id}/download", h.Files.Download)

			r.Get("/billing/entitlement", h.Billing.Entitlement)

			// Departments are readable by any active member, because a member
			// filing a claim has to pick one - putting the list behind the
			// management permission would make the create form unusable.
			r.Get("/departments", h.Org.ListDepartments)
			r.Get("/budgets", h.Org.ListBudgets)
			r.Get("/budgets/consumption", h.Org.BudgetConsumption)
			r.Get("/summary", h.Org.Summary)
			r.Get("/vendor-subscriptions", h.Org.ListVendorSubscriptions)
			r.Get("/members", h.Org.ListMembers)

			// Writes carry a second, tenant-keyed limit. Keyed on the tenant
			// rather than the IP so an office behind one NAT is not throttled
			// as a single client.
			r.Group(func(r chi.Router) {
				if cfg.WriteRateLimit != nil {
					r.Use(cfg.WriteRateLimit.Middleware(
						middleware.TenantKeyFunc(middleware.ClientKeyFunc(cfg.TrustedProxies))))
				}

				r.Post("/expenses", h.Expenses.Create)
				r.Patch("/expenses/{id}", h.Expenses.Update)
				r.Delete("/expenses/{id}", h.Expenses.Delete)

				// One route per action rather than a PATCH that takes a
				// status. The URL says what is being attempted, which is what
				// the state machine takes as input and what the audit ledger
				// records.
				r.Post("/expenses/{id}/submit", h.Expenses.Transition(expense.ActionSubmit))
				r.Post("/expenses/{id}/approve", h.Expenses.Transition(expense.ActionApprove))
				r.Post("/expenses/{id}/reject", h.Expenses.Transition(expense.ActionReject))
				r.Post("/expenses/{id}/withdraw", h.Expenses.Transition(expense.ActionWithdraw))
				r.Post("/expenses/{id}/revise", h.Expenses.Transition(expense.ActionRevise))
				r.Post("/expenses/{id}/pay", h.Expenses.Transition(expense.ActionPay))

				r.Post("/billing/checkout", h.Billing.StartCheckout)
				r.Post("/billing/portal", h.Billing.OpenPortal)

				r.Post("/departments", h.Org.CreateDepartment)
				r.Patch("/departments/{id}", h.Org.UpdateDepartment)
				// DELETE archives rather than deletes: the foreign keys are
				// ON DELETE RESTRICT, so a department with any history could
				// not be removed anyway, and archiving keeps historical claims
				// attributable.
				r.Delete("/departments/{id}", h.Org.ArchiveDepartment)

				r.Post("/budgets", h.Org.CreateBudget)
				r.Patch("/budgets/{id}", h.Org.UpdateBudget)
				r.Delete("/budgets/{id}", h.Org.DeleteBudget)

				r.Post("/vendor-subscriptions", h.Org.CreateVendorSubscription)
				r.Patch("/vendor-subscriptions/{id}", h.Org.UpdateVendorSubscription)

				r.Post("/expenses/{id}/attachments/presign", h.Files.PrepareUpload)
				r.Post("/expenses/{id}/attachments", h.Files.ConfirmUpload)
				r.Delete("/attachments/{id}", h.Files.Delete)

				r.Patch("/tenant", h.Account.UpdateOrganisation)
				// Ends every session, including this one, so it sits with the
				// credential endpoints in spirit even though it needs a token.
				r.Post("/auth/password", h.Account.ChangePassword)

				r.Post("/members", h.Org.InviteMember)
				r.Patch("/members/{id}", h.Org.UpdateMember)
			})
		})

		// Exports get their own group so the long deadline applies to nothing
		// else. The handler reads its own deadline from the same value, so the
		// socket write deadline and the context deadline cannot disagree.
		r.Group(func(r chi.Router) {
			r.Use(middleware.Timeout(cfg.ExportTimeout))
			r.Use(middleware.RequireAuth(cfg.Tokens, log))
			r.Get("/reports/expenses/export", h.Exports.HandleExport)
		})
	})

	return r
}
