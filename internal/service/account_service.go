package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/mlkad/b2b-expense-tracker/internal/auth"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/tenant"
	"github.com/mlkad/b2b-expense-tracker/internal/platform/postgres"
	repo "github.com/mlkad/b2b-expense-tracker/internal/repository/postgres"
)

// AccountService covers the two things a signed-in person changes about
// themselves and their organisation.
type AccountService struct {
	scope   *Scope
	tenancy *repo.TenancyRepository
	log     *slog.Logger
}

func NewAccountService(scope *Scope, tenancy *repo.TenancyRepository, log *slog.Logger) *AccountService {
	return &AccountService{scope: scope, tenancy: tenancy, log: log}
}

// Profile is what the dashboard needs to render its chrome: who the caller is,
// where they stand, and what they may do.
type Profile struct {
	UserID   string `json:"user_id"`
	Email    string `json:"email"`
	FullName string `json:"full_name,omitempty"`

	TenantID        string          `json:"tenant_id"`
	TenantSlug      string          `json:"tenant_slug"`
	TenantName      string          `json:"tenant_name"`
	DefaultCurrency shared.Currency `json:"default_currency"`

	MembershipID string  `json:"membership_id"`
	Role         string  `json:"role"`
	Status       string  `json:"status"`
	DepartmentID *string `json:"department_id,omitempty"`

	// ApprovalLimitMinor is the effective ceiling, resolved from the
	// membership override or the role default. Negative means unlimited, and
	// the field is named in minor units so the client formats it with the
	// tenant's currency rather than guessing.
	ApprovalLimitMinor int64 `json:"approval_limit_minor"`

	// Permissions is what the dashboard hides controls by. A convenience,
	// never an enforcement point: the client is free to lie about what it hid,
	// and every endpoint checks for itself.
	Permissions []tenant.Permission `json:"permissions"`
}

// Me resolves the caller.
func (s *AccountService) Me(ctx context.Context, subject auth.Subject) (Profile, error) {
	var out Profile

	err := s.scope.Read(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		org, err := s.tenancy.GetTenant(ctx, tc)
		if err != nil {
			return err
		}
		contact, err := s.tenancy.Contact(ctx, tc, actor.MembershipID)
		if err != nil {
			return err
		}

		out = Profile{
			UserID:             actor.UserID.String(),
			Email:              contact.Email,
			TenantID:           org.ID.String(),
			TenantSlug:         org.Slug,
			TenantName:         org.Name,
			DefaultCurrency:    org.DefaultCurrency,
			MembershipID:       actor.MembershipID.String(),
			Role:               string(actor.Role),
			Status:             string(actor.Status),
			ApprovalLimitMinor: actor.ApprovalCeilingMinor(),
			Permissions:        actor.Role.Permissions(),
		}
		if contact.FullName != nil {
			out.FullName = *contact.FullName
		}
		if actor.DepartmentID != nil {
			id := actor.DepartmentID.String()
			out.DepartmentID = &id
		}
		return nil
	})

	return out, err
}

// Organisation returns the caller's organisation.
func (s *AccountService) Organisation(ctx context.Context, subject auth.Subject) (tenant.Tenant, error) {
	var out tenant.Tenant

	err := s.scope.Read(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, _ tenant.Actor) error {
		var err error
		out, err = s.tenancy.GetTenant(ctx, tc)
		return err
	})

	return out, err
}

// UpdateOrganisation renames it or changes the default currency.
//
// The slug is deliberately not changeable. It appears in links people have
// bookmarked and in the sign-in form, and renaming it silently breaks both
// with no redirect to fix them - a rename would need an alias table and a
// deprecation window, which is a larger feature than a settings field.
func (s *AccountService) UpdateOrganisation(ctx context.Context, subject auth.Subject, name, currency string) (tenant.Tenant, error) {
	var out tenant.Tenant

	err := s.scope.Write(ctx, subject, func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error {
		if err := Require(actor, tenant.PermTenantManage); err != nil {
			return err
		}

		existing, err := s.tenancy.GetTenant(ctx, tc)
		if err != nil {
			return err
		}

		// Validated through the entity, so the rules are the ones the database
		// enforces rather than a second copy of them.
		candidate := tenant.Tenant{
			Slug:            existing.Slug,
			Name:            name,
			DefaultCurrency: shared.Currency(strings.ToUpper(strings.TrimSpace(currency))),
		}
		if candidate.DefaultCurrency == "" {
			candidate.DefaultCurrency = existing.DefaultCurrency
		}
		if err := candidate.Validate(); err != nil {
			return err
		}

		// Changing the currency after claims exist would leave a mix of
		// currencies being summed into one total, which produces a number that
		// looks authoritative and means nothing. Existing claims keep the
		// currency they were captured in, so the change is refused once there
		// are any.
		if candidate.DefaultCurrency != existing.DefaultCurrency {
			var claims int
			if err := tc.QueryRow(ctx, `SELECT count(*) FROM expenses`).Scan(&claims); err != nil {
				return err
			}
			if claims > 0 {
				return shared.FieldError{
					Field: "default_currency",
					Detail: fmt.Sprintf(
						"cannot be changed once claims exist (%d recorded); totals would sum mixed currencies", claims),
				}
			}
		}

		out, err = s.tenancy.UpdateTenant(ctx, tc, candidate.Name, candidate.DefaultCurrency)
		return err
	})

	return out, err
}

// ChangePassword replaces the caller's own credential.
//
// The current password is required. Without it, a stolen access token - which
// lives fifteen minutes - could be turned into a permanent takeover in one
// request, and the fifteen-minute lifetime would be protecting nothing.
//
// Every session is then revoked, including the caller's own. Keeping other
// sessions alive means somebody who changed their password because they
// believed it was compromised has done nothing about the attacker's live
// session, which is the situation the change was meant to resolve.
func (s *AccountService) ChangePassword(ctx context.Context, subject auth.Subject, current, next string) error {
	user, err := s.tenancy.GetUserByID(ctx, s.scope.DB(), subject.UserID)
	if err != nil {
		return err
	}

	if err := auth.ComparePassword(user.PasswordHash, current); err != nil {
		// The same error the login endpoint gives, and deliberately not
		// "your current password is wrong" with a different shape - this
		// endpoint is reachable with a stolen token, and a distinct response
		// would let the holder confirm guesses at the real password.
		return ErrCredentials
	}
	if err := auth.ValidatePassword(next); err != nil {
		return shared.FieldError{Field: "new_password", Detail: err.Error()}
	}
	if current == next {
		return shared.FieldError{Field: "new_password", Detail: "must differ from the current password"}
	}

	hash, err := auth.HashPassword(next)
	if err != nil {
		return err
	}
	if err := s.tenancy.SetUserPassword(ctx, s.scope.DB(), subject.UserID, hash); err != nil {
		return err
	}

	revoked, err := s.tenancy.RevokeAllSessions(ctx, s.scope.DB(), subject.UserID)
	if err != nil {
		// The password is already changed. Failing now would tell the user to
		// retry something that has happened, so it is logged at error - a
		// session that outlives a password change is worth investigating.
		s.log.ErrorContext(ctx, "password changed but sessions were not revoked",
			slog.String("user_id", subject.UserID.String()),
			slog.String("error", err.Error()))
		return nil
	}

	s.log.InfoContext(ctx, "password changed",
		slog.String("user_id", subject.UserID.String()),
		slog.Int64("sessions_revoked", revoked))
	return nil
}
