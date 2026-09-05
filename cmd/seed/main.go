// Command seed fills a development database with a plausible organisation.
//
// An empty dashboard shows nothing about whether the product works, and
// clicking twenty claims into existence by hand to find out is not a thing
// anybody does twice. This creates one organisation with people in several
// roles, departments, budgets, recurring vendor charges, and claims spread
// across every state the machine can reach.
//
// It refuses to touch a database whose name does not look like development,
// because its first act is to remove whatever is already there.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mlkad/b2b-expense-tracker/internal/auth"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/tenant"
)

const (
	defaultSlug     = "acme"
	defaultPassword = "correct-horse-battery"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "seed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		dsn   = flag.String("dsn", os.Getenv("SEED_DATABASE_URL"), "owner connection string")
		slug  = flag.String("slug", defaultSlug, "organisation slug")
		force = flag.Bool("force", false, "seed a database whose name does not look like development")
	)
	flag.Parse()

	if *dsn == "" {
		*dsn = os.Getenv("GOOSE_DBSTRING")
	}
	if *dsn == "" {
		return errors.New("set SEED_DATABASE_URL (or GOOSE_DBSTRING) to the owner connection string")
	}
	if err := refuseUnlessDevelopment(*dsn, *force); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// The owner connection, not the runtime role. Seeding writes across every
	// tenant table before any tenant exists to bind a session to, which is
	// exactly what row-level security is there to prevent.
	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping: %w", err)
	}

	s := &seeder{pool: pool, ctx: ctx, slug: *slug}
	if err := s.reset(); err != nil {
		return err
	}
	if err := s.build(); err != nil {
		return err
	}

	fmt.Printf(`
Seeded "%s".

  Sign in at http://localhost:5173

    owner     ada@%s.test      %s
    manager   grace@%s.test    %s   (Engineering, approves)
    finance   katherine@%s.test %s  (settles payments)
    member    margaret@%s.test %s   (files claims)

  There are claims waiting in every state, so the approver queue, the budget
  bars and the audit ledger all have something to show.
`, s.slug, s.slug, defaultPassword, s.slug, defaultPassword, s.slug, defaultPassword, s.slug, defaultPassword)

	return nil
}

// refuseUnlessDevelopment is the guard before the first DELETE.
//
// The name has to say it is development or a test. That is a blunt rule and it
// is deliberately blunt: the alternative is a flag somebody sets once in a
// shell that is still configured for staging three days later.
func refuseUnlessDevelopment(dsn string, force bool) error {
	if force {
		return nil
	}

	lower := strings.ToLower(dsn)
	for _, marker := range []string{"/expenses?", "/expenses_dev", "/expenses_test", "localhost", "127.0.0.1"} {
		if strings.Contains(lower, marker) {
			return nil
		}
	}
	return fmt.Errorf(
		"refusing to seed %q: it does not look like a local database, and seeding deletes what is there.\n"+
			"Pass -force if you are certain", redact(dsn))
}

func redact(dsn string) string {
	at := strings.LastIndex(dsn, "@")
	scheme := strings.Index(dsn, "://")
	if at < 0 || scheme < 0 || at < scheme {
		return "the configured database"
	}
	return dsn[:scheme+3] + "[redacted]" + dsn[at:]
}

type seeder struct {
	pool *pgxpool.Pool
	ctx  context.Context
	slug string

	tenantID uuid.UUID
	members  map[string]uuid.UUID
	depts    map[string]uuid.UUID
}

// reset removes a previous run of this organisation.
//
// Only this organisation: a developer may have other data they care about, and
// truncating the schema to save one WHERE clause is how somebody loses an
// afternoon's work.
func (s *seeder) reset() error {
	var id uuid.UUID
	err := s.pool.QueryRow(s.ctx, `SELECT id FROM tenants WHERE slug = $1`, s.slug).Scan(&id)
	if err != nil {
		return nil // nothing to remove
	}

	var userIDs []uuid.UUID
	rows, err := s.pool.Query(s.ctx, `SELECT user_id FROM memberships WHERE tenant_id = $1`, id)
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}
	for rows.Next() {
		var u uuid.UUID
		if err := rows.Scan(&u); err != nil {
			rows.Close()
			return err
		}
		userIDs = append(userIDs, u)
	}
	rows.Close()

	// The tenant first: memberships, departments, budgets and claims cascade
	// from it, and expenses_submitter_fk is ON DELETE RESTRICT - so removing
	// the users first is refused by the very constraint that stops a hard user
	// delete from destroying billing history.
	if _, err := s.pool.Exec(s.ctx, `DELETE FROM tenants WHERE id = $1`, id); err != nil {
		return fmt.Errorf("remove previous organisation: %w", err)
	}
	for _, u := range userIDs {
		if _, err := s.pool.Exec(s.ctx, `DELETE FROM users WHERE id = $1`, u); err != nil {
			return fmt.Errorf("remove previous users: %w", err)
		}
	}
	return nil
}

func (s *seeder) build() error {
	hash, err := auth.HashPassword(defaultPassword)
	if err != nil {
		return err
	}

	if err := s.pool.QueryRow(s.ctx,
		`INSERT INTO tenants (slug, name, default_currency) VALUES ($1, $2, 'USD') RETURNING id`,
		s.slug, strings.ToUpper(s.slug[:1])+s.slug[1:]+" Ltd",
	).Scan(&s.tenantID); err != nil {
		return fmt.Errorf("create organisation: %w", err)
	}

	// A live subscription, because the free tier excludes budgets and vendor
	// tracking - and a seeded database that cannot show them is not much of a
	// demonstration.
	if _, err := s.pool.Exec(s.ctx, `
		INSERT INTO tenant_subscriptions
		  (tenant_id, gateway_subscription_id, gateway_customer_ref, plan_code, status, seats,
		   current_period_start, current_period_end, last_event_at)
		VALUES ($1, 'sub_seed', $2, 'growth', 'active', 25,
		        now() - interval '10 days', now() + interval '20 days', now())`,
		// The id is passed twice rather than cast in place: a parameter used
		// as both a uuid and as text makes PostgreSQL deduce inconsistent
		// types for it and refuse the statement (42P08).
		s.tenantID, s.tenantID.String()); err != nil {
		return fmt.Errorf("grant plan: %w", err)
	}

	s.depts = map[string]uuid.UUID{}
	for _, name := range []string{"Engineering", "Marketing", "Operations"} {
		var id uuid.UUID
		if err := s.pool.QueryRow(s.ctx,
			`INSERT INTO departments (tenant_id, name) VALUES ($1, $2) RETURNING id`,
			s.tenantID, name).Scan(&id); err != nil {
			return fmt.Errorf("create department %s: %w", name, err)
		}
		s.depts[name] = id
	}

	people := []struct {
		local, name string
		role        tenant.Role
		dept        string
	}{
		{"ada", "Ada Lovelace", tenant.RoleOwner, ""},
		{"grace", "Grace Hopper", tenant.RoleManager, "Engineering"},
		{"katherine", "Katherine Johnson", tenant.RoleFinance, ""},
		{"margaret", "Margaret Hamilton", tenant.RoleMember, "Engineering"},
		{"joan", "Joan Clarke", tenant.RoleMember, "Marketing"},
	}

	s.members = map[string]uuid.UUID{}
	for _, p := range people {
		var userID uuid.UUID
		if err := s.pool.QueryRow(s.ctx,
			`INSERT INTO users (email, password_hash, full_name) VALUES ($1, $2, $3) RETURNING id`,
			fmt.Sprintf("%s@%s.test", p.local, s.slug), hash, p.name).Scan(&userID); err != nil {
			return fmt.Errorf("create user %s: %w", p.local, err)
		}

		var dept *uuid.UUID
		if p.dept != "" {
			id := s.depts[p.dept]
			dept = &id
		}

		var membershipID uuid.UUID
		if err := s.pool.QueryRow(s.ctx,
			`INSERT INTO memberships (tenant_id, user_id, role, status, department_id)
			 VALUES ($1, $2, $3, 'active', $4) RETURNING id`,
			s.tenantID, userID, string(p.role), dept).Scan(&membershipID); err != nil {
			return fmt.Errorf("create membership %s: %w", p.local, err)
		}
		s.members[p.local] = membershipID
	}

	if err := s.budgets(); err != nil {
		return err
	}
	if err := s.vendorSubscriptions(); err != nil {
		return err
	}
	return s.claims()
}

func (s *seeder) budgets() error {
	year := time.Now().UTC().Year()
	envelopes := []struct {
		dept   string
		amount int64
	}{
		{"Engineering", 4_000_000},
		{"Marketing", 1_500_000},
		{"Operations", 800_000},
	}

	for _, e := range envelopes {
		id := s.depts[e.dept]
		if _, err := s.pool.Exec(s.ctx, `
			INSERT INTO budgets (tenant_id, department_id, period_start, period_end,
			                     amount_minor, currency, alert_threshold_bps)
			VALUES ($1, $2, make_date($3, 1, 1), make_date($3, 12, 31), $4, 'USD', 8000)`,
			s.tenantID, id, year, e.amount); err != nil {
			return fmt.Errorf("create budget for %s: %w", e.dept, err)
		}
	}
	return nil
}

func (s *seeder) vendorSubscriptions() error {
	subs := []struct {
		vendor, plan, cadence, dept string
		amount                      int64
		inDays                      int
	}{
		{"Figma", "Organisation", "monthly", "Engineering", 24_000, 6},
		{"AWS", "Production", "monthly", "Engineering", 189_050, 2},
		{"Notion", "Business", "annual", "Operations", 480_000, 45},
		{"HubSpot", "Starter", "monthly", "Marketing", 45_000, 12},
	}

	for _, v := range subs {
		dept := s.depts[v.dept]
		if _, err := s.pool.Exec(s.ctx, `
			INSERT INTO vendor_subscriptions
			  (tenant_id, vendor, plan_name, department_id, owner_id, amount_minor,
			   currency, cadence, next_charge_on, auto_create_expense)
			VALUES ($1, $2, $3, $4, $5, $6, 'USD', $7::billing_cadence, CURRENT_DATE + $8::int, TRUE)`,
			s.tenantID, v.vendor, v.plan, dept, s.members["ada"], v.amount, v.cadence, v.inDays); err != nil {
			return fmt.Errorf("create vendor subscription %s: %w", v.vendor, err)
		}
	}
	return nil
}

// claims puts something in every state, so every screen has something to show:
// drafts to edit, a queue to approve from, approved claims for finance to
// settle, a rejection with a reason, and paid history for the budget bars.
func (s *seeder) claims() error {
	type claim struct {
		merchant, category, status, dept, submitter string
		amount                                      int64
		daysAgo                                     int
	}

	claims := []claim{
		{"Trainline", "travel", "draft", "Engineering", "margaret", 8_940, 2},
		{"Pret A Manger", "meals", "draft", "Engineering", "margaret", 1_250, 1},
		{"Figma", "software", "pending_approval", "Engineering", "margaret", 24_000, 5},
		{"Dell", "hardware", "pending_approval", "Engineering", "margaret", 189_900, 7},
		{"Google Ads", "marketing", "pending_approval", "Marketing", "joan", 320_000, 3},
		{"Hilton Munich", "accommodation", "approved", "Engineering", "margaret", 47_800, 21},
		{"O'Reilly", "training", "approved", "Engineering", "margaret", 29_900, 18},
		{"Canva", "marketing", "rejected", "Marketing", "joan", 12_000, 14},
		{"AWS", "software", "paid", "Engineering", "margaret", 189_050, 40},
		{"WeWork", "office", "paid", "Operations", "joan", 250_000, 55},
		{"Uber", "travel", "paid", "Engineering", "margaret", 4_320, 62},
		{"Slack", "software", "paid", "Engineering", "margaret", 96_000, 70},
	}

	rng := rand.New(rand.NewSource(1))

	for _, c := range claims {
		dept := s.depts[c.dept]
		submitter := s.members[c.submitter]

		var submitted, decided, paid *time.Time
		var decidedBy *uuid.UUID
		var note, ref *string

		spent := time.Now().UTC().AddDate(0, 0, -c.daysAgo)
		at := spent.Add(time.Duration(rng.Intn(48)) * time.Hour)

		switch c.status {
		case "pending_approval":
			submitted = &at
		case "approved", "rejected":
			d := at.Add(24 * time.Hour)
			submitted, decided, decidedBy = &at, &d, ptr(s.members["grace"])
			if c.status == "rejected" {
				note = ptr("No receipt attached - please add one and resubmit.")
			}
		case "paid":
			d := at.Add(24 * time.Hour)
			p := d.Add(72 * time.Hour)
			submitted, decided, decidedBy, paid = &at, &d, ptr(s.members["grace"]), &p
			ref = ptr(fmt.Sprintf("BACS-%s-%04d", p.Format("2006-01-02"), rng.Intn(9999)))
		}

		var claimID uuid.UUID
		if err := s.pool.QueryRow(s.ctx, `
			INSERT INTO expenses
			  (tenant_id, submitter_id, department_id, status, category, amount_minor,
			   currency, merchant, spent_at, submitted_at, decided_at, decided_by,
			   decision_note, paid_at, payment_ref)
			VALUES ($1,$2,$3,$4::expense_status,$5::expense_category,$6,'USD',$7,$8,$9,$10,$11,$12,$13,$14)
			RETURNING id`,
			s.tenantID, submitter, dept, c.status, c.category, c.amount, c.merchant,
			spent, submitted, decided, decidedBy, note, paid, ref,
		).Scan(&claimID); err != nil {
			return fmt.Errorf("create claim %s: %w", c.merchant, err)
		}

		// The ledger, so the history panel is not empty. Written here rather
		// than left to the state machine because the seed constructs the final
		// state directly - the transitions it would have taken are what these
		// rows record.
		if err := s.ledger(claimID, c.status, c.amount, submitter, submitted, decided, paid, note); err != nil {
			return err
		}
	}
	return nil
}

func (s *seeder) ledger(
	claimID uuid.UUID, status string, amount int64, submitter uuid.UUID,
	submitted, decided, paid *time.Time, note *string,
) error {
	type entry struct {
		action, from, to string
		actor            uuid.UUID
		at               *time.Time
		reason           *string
	}

	created := time.Now().UTC().AddDate(0, 0, -90)
	entries := []entry{{"created", "", "draft", submitter, &created, nil}}

	if submitted != nil {
		entries = append(entries, entry{"submitted", "draft", "pending_approval", submitter, submitted, nil})
	}
	switch status {
	case "approved", "paid":
		entries = append(entries, entry{"approved", "pending_approval", "approved", s.members["grace"], decided, nil})
	case "rejected":
		entries = append(entries, entry{"rejected", "pending_approval", "rejected", s.members["grace"], decided, note})
	}
	if status == "paid" {
		entries = append(entries, entry{"paid", "approved", "paid", s.members["katherine"], paid, nil})
	}

	for _, e := range entries {
		var from any
		if e.from != "" {
			from = e.from
		}
		if _, err := s.pool.Exec(s.ctx, `
			INSERT INTO expense_events
			  (tenant_id, expense_id, action, from_status, to_status, actor_id,
			   reason, amount_minor, currency, revision, occurred_at)
			VALUES ($1,$2,$3::expense_action,$4::expense_status,$5::expense_status,$6,$7,$8,'USD',1,$9)`,
			s.tenantID, claimID, e.action, from, e.to, e.actor, e.reason, amount, e.at); err != nil {
			return fmt.Errorf("write ledger entry %s: %w", e.action, err)
		}
	}
	return nil
}

func ptr[T any](v T) *T { return &v }
