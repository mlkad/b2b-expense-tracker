package tenant

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
)

func TestTenantValidate(t *testing.T) {
	base := func() *Tenant {
		return &Tenant{Slug: "acme", Name: "Acme Ltd", DefaultCurrency: "USD"}
	}

	t.Run("a well-formed tenant passes", func(t *testing.T) {
		if err := base().Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})

	// Normalisation happens in place. Validating a trimmed copy would leave
	// " acme" to be persisted verbatim and caught one round trip later by the
	// CHECK constraint, as a constraint violation rather than a field error.
	t.Run("input is normalised in place, not on a copy", func(t *testing.T) {
		org := &Tenant{Slug: "  ACME  ", Name: "  Acme Ltd  ", DefaultCurrency: "USD"}
		if err := org.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if org.Slug != "acme" {
			t.Errorf("slug = %q, want the trimmed lowercase form", org.Slug)
		}
		if org.Name != "Acme Ltd" {
			t.Errorf("name = %q, want it trimmed", org.Name)
		}
	})

	// The same rules as tenants_slug_format_chk. Checking here saves a round
	// trip; the database remains the authority.
	t.Run("slugs", func(t *testing.T) {
		valid := []string{"acme", "a-b", "acme-ltd-2026", "abc", strings.Repeat("a", 40)}
		for _, slug := range valid {
			org := base()
			org.Slug = slug
			if err := org.Validate(); err != nil {
				t.Errorf("rejected valid slug %q: %v", slug, err)
			}
		}

		invalid := map[string]string{
			"too short":       "ab",
			"too long":        strings.Repeat("a", 41),
			"leading hyphen":  "-acme",
			"trailing hyphen": "acme-",
			"underscore":      "acme_ltd",
			"space":           "acme ltd",
			"slash":           "acme/ltd",
			"empty":           "",
			"unicode":         "acmé",
		}
		for name, slug := range invalid {
			t.Run(name, func(t *testing.T) {
				org := base()
				org.Slug = slug
				if err := org.Validate(); !errors.Is(err, shared.ErrValidation) {
					t.Fatalf("accepted %q", slug)
				}
			})
		}
	})

	t.Run("name and currency", func(t *testing.T) {
		cases := map[string]func(*Tenant){
			"blank name":         func(o *Tenant) { o.Name = "   " },
			"overlong name":      func(o *Tenant) { o.Name = strings.Repeat("a", 201) },
			"lowercase currency": func(o *Tenant) { o.DefaultCurrency = "usd" },
			"two letters":        func(o *Tenant) { o.DefaultCurrency = "US" },
			"missing currency":   func(o *Tenant) { o.DefaultCurrency = "" },
		}
		for name, mutate := range cases {
			t.Run(name, func(t *testing.T) {
				org := base()
				mutate(org)
				if err := org.Validate(); !errors.Is(err, shared.ErrValidation) {
					t.Fatal("accepted invalid input")
				}
			})
		}
	})
}

func TestIsOperational(t *testing.T) {
	for status, want := range map[Status]bool{
		StatusActive:    true,
		StatusSuspended: false,
		StatusCancelled: false,
	} {
		org := &Tenant{Status: status}
		if got := org.IsOperational(); got != want {
			t.Errorf("status %s: IsOperational = %v, want %v", status, got, want)
		}
	}
}

// "Your own claim" is decided by this everywhere, so it has to compare the
// membership rather than the user: the same person in two tenants is two
// different submitters.
func TestSameMembership(t *testing.T) {
	mine := uuid.New()
	actor := Actor{MembershipID: mine, UserID: uuid.New()}

	if !actor.SameMembership(mine) {
		t.Error("an actor did not recognise their own membership")
	}
	if actor.SameMembership(uuid.New()) {
		t.Error("an actor claimed someone else's membership")
	}
	if actor.SameMembership(actor.UserID) {
		t.Error("the comparison used the user id; the same person in two tenants would share claims")
	}
}

func TestPermissionsListingMatchesTheMatrix(t *testing.T) {
	for _, role := range AllRoles {
		listed := role.Permissions()
		for _, perm := range listed {
			if !role.Allows(perm) {
				t.Errorf("%s lists %s but Allows says no; the dashboard would render a button that 403s", role, perm)
			}
		}
		if len(listed) != len(permissions[role]) {
			t.Errorf("%s lists %d permissions, matrix holds %d", role, len(listed), len(permissions[role]))
		}
	}
}
