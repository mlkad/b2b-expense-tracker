package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/auth"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/tenant"
	repo "github.com/mlkad/b2b-expense-tracker/internal/repository/postgres"
)

// ErrCredentials is the single answer to every failed authentication.
//
// One error for "no such user", "no password set" and "wrong password" is not
// vagueness for its own sake: distinguishing them turns the login endpoint
// into a user enumeration oracle, which is the first step of a credential
// stuffing run against every other service those users have accounts on.
var ErrCredentials = errors.New("email or password is incorrect")

// ErrTokenReuse means a refresh token that had already been exchanged came
// back. It is not a validation failure but a signal of theft.
var ErrTokenReuse = errors.New("refresh token was already used")

type AuthService struct {
	scope   *Scope
	tenancy *repo.TenancyRepository
	tokens  *auth.TokenService
	log     *slog.Logger

	refreshTTL time.Duration
}

func NewAuthService(scope *Scope, tenancy *repo.TenancyRepository, tokens *auth.TokenService, refreshTTL time.Duration, log *slog.Logger) *AuthService {
	if refreshTTL <= 0 {
		refreshTTL = 30 * 24 * time.Hour
	}
	return &AuthService{scope: scope, tenancy: tenancy, tokens: tokens, refreshTTL: refreshTTL, log: log}
}

// Session is what a successful authentication returns.
type Session struct {
	AccessToken  string    `json:"access_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	RefreshToken string    `json:"refresh_token"`
	TenantID     uuid.UUID `json:"tenant_id"`
	TenantSlug   string    `json:"tenant_slug"`
	Role         string    `json:"role"`
}

// ClientInfo is what is recorded against a session for the "active sessions"
// list a user can review.
type ClientInfo struct {
	UserAgent *string
	IP        *netip.Addr
}

// Register creates a user and their first organisation in one call.
//
// Both, and in that order, because a user with no membership can authenticate
// and then do nothing: every endpoint resolves an actor first, so they would
// hold a token that is rejected everywhere. Signing up is signing up an
// organisation.
func (s *AuthService) Register(ctx context.Context, email, password, fullName, orgName, orgSlug, currency string, client ClientInfo) (*Session, error) {
	email = strings.TrimSpace(strings.ToLower(email))

	if err := auth.ValidatePassword(password); err != nil {
		return nil, shared.FieldError{Field: "password", Detail: err.Error()}
	}

	org := &tenant.Tenant{Name: orgName, Slug: orgSlug, DefaultCurrency: shared.Currency(strings.ToUpper(currency))}
	if org.DefaultCurrency == "" {
		org.DefaultCurrency = "USD"
	}
	if err := org.Validate(); err != nil {
		return nil, err
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return nil, err
	}

	var name *string
	if trimmed := strings.TrimSpace(fullName); trimmed != "" {
		name = &trimmed
	}

	user, err := s.tenancy.CreateUser(ctx, s.scope.DB(), email, &hash, name)
	if err != nil {
		return nil, err
	}

	// The organisation is created in a second transaction. A failure here
	// leaves a user with no membership, who can log in and reach nothing -
	// recoverable by creating an organisation, and a great deal simpler than
	// spanning identity and tenancy in one transaction when the two live under
	// different privilege models.
	if _, err := s.tenancy.CreateTenantWithOwner(ctx, s.scope.DB(), org, user.ID); err != nil {
		return nil, err
	}

	return s.issue(ctx, user.ID, org.ID, org.Slug, string(tenant.RoleOwner), email, uuid.New(), client)
}

// Login exchanges credentials for a session in one of the user's
// organisations.
func (s *AuthService) Login(ctx context.Context, email, password string, tenantSlug string, client ClientInfo) (*Session, error) {
	email = strings.TrimSpace(strings.ToLower(email))

	user, err := s.tenancy.GetUserByEmail(ctx, s.scope.DB(), email)
	if err != nil {
		if errors.Is(err, shared.ErrNotFound) {
			// A full bcrypt comparison against a placeholder, so an unknown
			// address costs the same wall-clock time as a known one. Returning
			// early here is what makes login timing a user enumeration oracle.
			_ = auth.ComparePassword(nil, password)
			return nil, ErrCredentials
		}
		return nil, err
	}

	if err := auth.ComparePassword(user.PasswordHash, password); err != nil {
		return nil, ErrCredentials
	}

	memberships, err := s.tenancy.ListTenantsForUser(ctx, s.scope.DB(), user.ID)
	if err != nil {
		return nil, err
	}

	chosen, err := selectTenant(memberships, tenantSlug)
	if err != nil {
		return nil, err
	}

	return s.issue(ctx, user.ID, chosen.TenantID, chosen.Slug, string(chosen.Role), user.Email, uuid.New(), client)
}

// SwitchTenant issues a token for another organisation the user belongs to.
//
// It requires a live access token rather than a password, and it mints a new
// token rather than amending the existing one - the tenant claim is what RLS
// binds to, so it has to be signed, not swapped.
func (s *AuthService) SwitchTenant(ctx context.Context, subject auth.Subject, target uuid.UUID, client ClientInfo) (*Session, error) {
	memberships, err := s.tenancy.ListTenantsForUser(ctx, s.scope.DB(), subject.UserID)
	if err != nil {
		return nil, err
	}

	for _, m := range memberships {
		if m.TenantID != target {
			continue
		}
		if m.Status != tenant.MembershipActive || m.TenantStatus != tenant.StatusActive {
			return nil, fmt.Errorf("%w: your membership of that organisation is not active", shared.ErrForbidden)
		}
		return s.issue(ctx, subject.UserID, m.TenantID, m.Slug, string(m.Role), subject.Email, uuid.New(), client)
	}

	// Not-found rather than forbidden: a 403 would confirm that an
	// organisation with that id exists, which is exactly what an attacker
	// enumerating tenant ids wants to learn.
	return nil, shared.ErrNotFound
}

// Refresh rotates a refresh token and mints a new access token.
//
// The rotation is the whole security property. Each refresh token can be
// exchanged once; the exchange is a compare-and-swap on used_at, so exactly
// one caller wins. A token that comes back after it was used is evidence of
// theft, and the response is to revoke the whole family - both the thief's
// session and the victim's - because there is no way to tell which is which.
func (s *AuthService) Refresh(ctx context.Context, presented string, client ClientInfo) (*Session, error) {
	digest, err := auth.HashRefreshToken(presented)
	if err != nil {
		return nil, ErrCredentials
	}

	stored, err := s.tenancy.FindRefreshToken(ctx, s.scope.DB(), digest)
	if err != nil {
		if errors.Is(err, shared.ErrNotFound) {
			return nil, ErrCredentials
		}
		return nil, err
	}

	switch {
	case stored.RevokedAt != nil:
		return nil, ErrCredentials
	case time.Now().After(stored.ExpiresAt):
		return nil, ErrCredentials
	case stored.UsedAt != nil:
		s.log.WarnContext(ctx, "refresh token reuse detected; revoking the session family",
			slog.String("user_id", stored.UserID.String()),
			slog.String("family_id", stored.FamilyID.String()))
		if err := s.tenancy.RevokeRefreshFamily(ctx, s.scope.DB(), stored.FamilyID); err != nil {
			return nil, err
		}
		return nil, ErrTokenReuse
	}

	won, err := s.tenancy.ConsumeRefreshToken(ctx, s.scope.DB(), stored.ID)
	if err != nil {
		return nil, err
	}
	if !won {
		// Another request consumed it between the read and the update. That is
		// either a double-submitting client or a thief racing the victim, and
		// they are indistinguishable, so it is treated as the more serious of
		// the two.
		if err := s.tenancy.RevokeRefreshFamily(ctx, s.scope.DB(), stored.FamilyID); err != nil {
			return nil, err
		}
		return nil, ErrTokenReuse
	}

	user, err := s.tenancy.GetUserByID(ctx, s.scope.DB(), stored.UserID)
	if err != nil {
		return nil, err
	}
	memberships, err := s.tenancy.ListTenantsForUser(ctx, s.scope.DB(), user.ID)
	if err != nil {
		return nil, err
	}
	chosen, err := selectTenant(memberships, "")
	if err != nil {
		return nil, err
	}

	// The new token joins the same family, so a theft discovered three
	// rotations later still revokes everything descended from the original
	// login.
	return s.issue(ctx, user.ID, chosen.TenantID, chosen.Slug, string(chosen.Role), user.Email, stored.FamilyID, client)
}

// Tenants lists the organisations a user may switch to.
func (s *AuthService) Tenants(ctx context.Context, subject auth.Subject) ([]repo.TenantMembership, error) {
	return s.tenancy.ListTenantsForUser(ctx, s.scope.DB(), subject.UserID)
}

func (s *AuthService) issue(
	ctx context.Context,
	userID, tenantID uuid.UUID,
	slug, role, email string,
	familyID uuid.UUID,
	client ClientInfo,
) (*Session, error) {
	accessToken, expiresAt, err := s.tokens.Issue(userID, tenantID, email)
	if err != nil {
		return nil, err
	}

	refreshToken, digest, err := auth.NewRefreshToken()
	if err != nil {
		return nil, err
	}
	if _, err := s.tenancy.StoreRefreshToken(ctx, s.scope.DB(), userID, familyID, digest,
		time.Now().Add(s.refreshTTL), client.UserAgent, client.IP); err != nil {
		return nil, err
	}

	return &Session{
		AccessToken:  accessToken,
		ExpiresAt:    expiresAt,
		RefreshToken: refreshToken,
		TenantID:     tenantID,
		TenantSlug:   slug,
		Role:         role,
	}, nil
}

// selectTenant picks which organisation a session is for.
//
// With a slug, it must match one the user belongs to. Without one, the single
// active membership wins; a user in several organisations without naming one
// is asked to choose, rather than being silently dropped into whichever
// happened to sort first.
func selectTenant(memberships []repo.TenantMembership, slug string) (repo.TenantMembership, error) {
	var active []repo.TenantMembership
	for _, m := range memberships {
		if m.Status == tenant.MembershipActive && m.TenantStatus == tenant.StatusActive {
			active = append(active, m)
		}
	}

	if slug != "" {
		for _, m := range active {
			if strings.EqualFold(m.Slug, slug) {
				return m, nil
			}
		}
		// Deliberately the credentials error rather than not-found: this is an
		// unauthenticated endpoint, and a distinct answer would let a caller
		// probe which organisations an address belongs to.
		return repo.TenantMembership{}, ErrCredentials
	}

	switch len(active) {
	case 0:
		return repo.TenantMembership{}, fmt.Errorf("%w: you have no active organisation membership", shared.ErrForbidden)
	case 1:
		return active[0], nil
	default:
		return repo.TenantMembership{}, shared.FieldError{
			Field:  "organisation",
			Detail: "you belong to more than one organisation; name one to sign in to",
		}
	}
}
