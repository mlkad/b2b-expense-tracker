// Package postgres implements the repositories over sqlc-generated queries.
//
// Its job at the boundary is translation in both directions: generated row
// structs become domain entities, and driver errors become the sentinels in
// internal/domain/shared. Nothing above this package imports pgx, which is
// what lets the services be tested without a database and what stops a pgx
// error code from leaking into an HTTP response.
package postgres

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
)

// ErrPolicyViolation means PostgreSQL refused a write because a row-level
// security policy rejected it.
//
// This is never a normal outcome. Reaching it means either that a service
// tried to write a row into a tenant other than the one its transaction is
// bound to - a bug of the most serious kind - or that the binding is wrong.
// It is deliberately not mapped onto ErrForbidden: a 403 would make it look
// like an ordinary permission decision and it would be triaged as one. It
// surfaces as a 500 and it should page someone.
var ErrPolicyViolation = errors.New("row-level security refused the write")

// constraintErrors maps constraint names to the field error a user should see.
//
// A table rather than a switch on the code, because the useful information in
// a 23505 is which constraint fired, and the mapping from constraint to
// message is the only place that knowledge belongs. A constraint not listed
// here still produces a sensible generic error - it just says less.
var constraintErrors = map[string]shared.FieldError{
	"users_email_live_key":                   {Field: "email", Detail: "is already registered"},
	"tenants_slug_key":                       {Field: "slug", Detail: "is already taken"},
	"tenants_slug_format_chk":                {Field: "slug", Detail: "must be lowercase letters, digits or hyphens"},
	"memberships_tenant_user_key":            {Field: "user_id", Detail: "is already a member of this organisation"},
	"memberships_single_owner_key":           {Field: "role", Detail: "an organisation may have only one owner; transfer ownership instead"},
	"departments_tenant_name_live_key":       {Field: "name", Detail: "a department with this name already exists"},
	"budgets_no_overlap":                     {Field: "period_start", Detail: "overlaps an existing budget for this department"},
	"budgets_period_chk":                     {Field: "period_end", Detail: "must not be before the start of the period"},
	"expenses_amount_chk":                    {Field: "amount_minor", Detail: "must be greater than zero"},
	"expenses_spent_at_chk":                  {Field: "spent_at", Detail: "must not be in the future"},
	"expenses_status_timestamps_chk":         {Field: "status", Detail: "the claim's timestamps do not match its status"},
	"expenses_recurring_once_per_charge_key": {Field: "source_subscription_id", Detail: "a claim for this charge already exists"},
	"expense_attachments_size_chk":           {Field: "size_bytes", Detail: "must be between 1 byte and 25 MiB"},
	"tenant_subscriptions_gateway_id_key":    {Field: "gateway_subscription_id", Detail: "is already linked to another organisation"},
}

// translate converts a driver error into something the layers above can branch
// on. Every repository method funnels its error through it.
func translate(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return shared.ErrNotFound
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}

	switch pgErr.Code {
	case "23505": // unique_violation
		if fe, ok := constraintErrors[pgErr.ConstraintName]; ok {
			return fe
		}
		return fmt.Errorf("%w: %s", shared.ErrConflict, pgErr.ConstraintName)

	case "23P01": // exclusion_violation
		if fe, ok := constraintErrors[pgErr.ConstraintName]; ok {
			return fe
		}
		return fmt.Errorf("%w: %s", shared.ErrConflict, pgErr.ConstraintName)

	case "23514": // check_violation
		if fe, ok := constraintErrors[pgErr.ConstraintName]; ok {
			return fe
		}
		return fmt.Errorf("%w: %s", shared.ErrValidation, pgErr.ConstraintName)

	case "23503": // foreign_key_violation
		// The referenced row is missing, or it is in another tenant. RLS makes
		// those indistinguishable from here, and that is the correct answer to
		// give a client: confirming that an id exists in some other tenant is
		// itself a leak.
		return shared.FieldError{
			Field:  fkField(pgErr.ConstraintName),
			Detail: "does not refer to anything in this organisation",
		}

	case "42501": // insufficient_privilege
		// Two different failures share this code. A WITH CHECK refusal names
		// the policy in the message; a missing GRANT does not.
		if strings.Contains(pgErr.Message, "row-level security") {
			return fmt.Errorf("%w: %s", ErrPolicyViolation, pgErr.Message)
		}
		return fmt.Errorf("database privileges are misconfigured: %s", pgErr.Message)

	case "55P03": // lock_not_available, from FOR UPDATE NOWAIT
		// Someone else holds the row. From the caller's point of view this is
		// the same situation as a failed compare-and-swap: reload and decide
		// again against what is actually there now.
		return fmt.Errorf("%w: the claim is being changed by someone else", shared.ErrConflict)

	case "22P02": // invalid_text_representation
		return fmt.Errorf("%w: %s", shared.ErrValidation, pgErr.Message)

	case "25006": // read_only_sql_transaction
		// A write attempted inside a Binding{ReadOnly: true} transaction. A
		// wiring mistake, and one worth an unmistakable message: the
		// alternative is a puzzling 500 on an endpoint that reads fine.
		return fmt.Errorf("write attempted in a read-only transaction: %s", pgErr.Message)

	default:
		return err
	}
}

// fkField turns a foreign key constraint name into the request field it
// corresponds to. The naming convention across the migrations is
// <table>_<column>_fk, so the middle is the field.
func fkField(constraint string) string {
	name := strings.TrimSuffix(constraint, "_fk")
	for _, table := range []string{
		"expenses_", "expense_attachments_", "expense_events_", "memberships_",
		"departments_", "budgets_", "vendor_subscriptions_", "tenant_subscriptions_",
	} {
		if strings.HasPrefix(name, table) {
			return strings.TrimPrefix(name, table)
		}
	}
	if name == "" {
		return "request"
	}
	return name
}
