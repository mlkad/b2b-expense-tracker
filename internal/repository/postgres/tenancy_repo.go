package postgres

import (
	"context"
	"net/netip"
	"time"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/tenant"
	"github.com/mlkad/b2b-expense-tracker/internal/platform/postgres"
	"github.com/mlkad/b2b-expense-tracker/internal/repository/postgres/gen"
)

// TenancyRepository covers identity, organisations and membership.
//
// Some of its methods take a *TenantConn and some take the *DB directly. That
// split is the tenancy model made visible: anything scoped to one organisation
// needs a binding, and the three things that happen before an organisation is
// known - registering, logging in, listing which organisations you belong to -
// cannot have one.
type TenancyRepository struct{}

func NewTenancyRepository() *TenancyRepository { return &TenancyRepository{} }

// ResolveActor loads the caller's standing in a tenant.
//
// This runs on every authenticated request, inside the same transaction as the
// work it authorises. Doing it in the transaction rather than in middleware
// means the role that authorises an action and the rows that action touches
// come from one snapshot: a demotion committed halfway through cannot apply to
// one and not the other.
//
// A missing row is ErrNotFound, which the HTTP layer renders as 403 rather
// than 404. The token was valid, so the caller exists; what they do not have
// is a membership here, and saying "not found" about the organisation would
// confirm nothing useful while making the failure harder to diagnose.
func (r *TenancyRepository) ResolveActor(ctx context.Context, tc *postgres.TenantConn, userID uuid.UUID) (tenant.Actor, error) {
	row, err := gen.New(tc).ResolveActor(ctx, gen.ResolveActorParams{
		TenantID: tc.TenantID(),
		UserID:   userID,
	})
	if err != nil {
		return tenant.Actor{}, translate(err)
	}

	return tenant.Actor{
		TenantID:           row.TenantID,
		UserID:             row.UserID,
		MembershipID:       row.MembershipID,
		Role:               tenant.Role(row.Role),
		Status:             tenant.MembershipStatus(row.Status),
		DepartmentID:       row.DepartmentID,
		ApprovalLimitMinor: row.ApprovalLimitMinor,
		TenantStatus:       tenant.Status(row.TenantStatus),
	}, nil
}

// -----------------------------------------------------------------------------
// Identity: no tenant binding, because there is no tenant yet
// -----------------------------------------------------------------------------

// User is the identity projection the auth service works with. The password
// hash is a pointer because an invited user may not have set one, and that is
// a different situation from a wrong password.
type User struct {
	ID           uuid.UUID
	Email        string
	PasswordHash *string
	FullName     *string
	CreatedAt    time.Time
}

func (r *TenancyRepository) CreateUser(ctx context.Context, db *postgres.DB, email string, hash *string, fullName *string) (User, error) {
	row, err := gen.New(db.Pool()).CreateUser(ctx, gen.CreateUserParams{
		Email:        email,
		PasswordHash: hash,
		FullName:     fullName,
	})
	if err != nil {
		return User{}, translate(err)
	}
	return User{
		ID: row.ID, Email: row.Email, PasswordHash: row.PasswordHash,
		FullName: row.FullName, CreatedAt: row.CreatedAt,
	}, nil
}

// GetUserByEmail is the login lookup. It runs against the pool with no tenant
// binding, which is safe only because the users table is deliberately outside
// the RLS model - see the note at the top of migration 00006 - and because the
// query is keyed by a unique index and can return at most one row.
func (r *TenancyRepository) GetUserByEmail(ctx context.Context, db *postgres.DB, email string) (User, error) {
	row, err := gen.New(db.Pool()).GetUserByEmail(ctx, email)
	if err != nil {
		return User{}, translate(err)
	}
	return User{
		ID: row.ID, Email: row.Email, PasswordHash: row.PasswordHash,
		FullName: row.FullName, CreatedAt: row.CreatedAt,
	}, nil
}

func (r *TenancyRepository) GetUserByID(ctx context.Context, db *postgres.DB, id uuid.UUID) (User, error) {
	row, err := gen.New(db.Pool()).GetUserByID(ctx, id)
	if err != nil {
		return User{}, translate(err)
	}
	return User{
		ID: row.ID, Email: row.Email, PasswordHash: row.PasswordHash,
		FullName: row.FullName, CreatedAt: row.CreatedAt,
	}, nil
}

// Membership is one row of the tenant switcher.
type TenantMembership struct {
	MembershipID uuid.UUID
	TenantID     uuid.UUID
	Slug         string
	Name         string
	Role         tenant.Role
	Status       tenant.MembershipStatus
	TenantStatus tenant.Status
}

// ListTenantsForUser powers the tenant switcher, and is the one query that
// legitimately crosses tenants for a user.
//
// It runs in a system transaction, scoped by user_id. Everything it can return
// is a tenant the caller already belongs to, so the widening is bounded by the
// join rather than by trust.
func (r *TenancyRepository) ListTenantsForUser(ctx context.Context, db *postgres.DB, userID uuid.UUID) ([]TenantMembership, error) {
	var out []TenantMembership

	err := db.WithSystemTx(ctx, postgres.Binding{ReadOnly: true}, "list a user's own tenant memberships",
		func(ctx context.Context, tc *postgres.TenantConn) error {
			rows, err := gen.New(tc).ListMembershipsForUser(ctx, userID)
			if err != nil {
				return translate(err)
			}
			out = make([]TenantMembership, len(rows))
			for i, row := range rows {
				out[i] = TenantMembership{
					MembershipID: row.MembershipID,
					TenantID:     row.TenantID,
					Slug:         row.Slug,
					Name:         row.Name,
					Role:         tenant.Role(row.Role),
					Status:       tenant.MembershipStatus(row.Status),
					TenantStatus: tenant.Status(row.TenantStatus),
				}
			}
			return nil
		})
	return out, err
}

// -----------------------------------------------------------------------------
// Provisioning
// -----------------------------------------------------------------------------

// CreateTenantWithOwner provisions an organisation and its first membership.
//
// Both writes are in one system transaction, and that is not just tidiness: a
// tenant with no owner is unreachable through every other endpoint, because
// every one of them resolves an actor first. A partial commit here would
// produce an organisation nobody can administer or delete.
func (r *TenancyRepository) CreateTenantWithOwner(
	ctx context.Context,
	db *postgres.DB,
	t *tenant.Tenant,
	ownerUserID uuid.UUID,
) (tenant.Membership, error) {
	var owner tenant.Membership

	err := db.WithSystemTx(ctx, postgres.Binding{}, "provision a new organisation and its owner",
		func(ctx context.Context, tc *postgres.TenantConn) error {
			q := gen.New(tc)

			created, err := q.CreateTenant(ctx, gen.CreateTenantParams{
				Slug:            t.Slug,
				Name:            t.Name,
				DefaultCurrency: string(t.DefaultCurrency),
			})
			if err != nil {
				return translate(err)
			}

			membership, err := q.CreateMembership(ctx, gen.CreateMembershipParams{
				TenantID: created.ID,
				UserID:   ownerUserID,
				Role:     gen.MembershipRoleOwner,
				Status:   gen.MembershipStatusActive,
			})
			if err != nil {
				return translate(err)
			}

			t.ID = created.ID
			t.Status = tenant.Status(created.Status)
			t.CreatedAt = created.CreatedAt
			t.UpdatedAt = created.UpdatedAt

			owner = tenant.Membership{
				ID:       membership.ID,
				TenantID: membership.TenantID,
				UserID:   membership.UserID,
				Role:     tenant.Role(membership.Role),
				Status:   tenant.MembershipStatus(membership.Status),
			}
			return nil
		})

	return owner, err
}

// InviteMember adds a membership to the caller's tenant.
func (r *TenancyRepository) InviteMember(
	ctx context.Context,
	tc *postgres.TenantConn,
	userID uuid.UUID,
	role tenant.Role,
	departmentID *uuid.UUID,
	approvalLimit *int64,
	invitedBy uuid.UUID,
) (tenant.Membership, error) {
	row, err := gen.New(tc).CreateMembership(ctx, gen.CreateMembershipParams{
		TenantID:           tc.TenantID(),
		UserID:             userID,
		Role:               gen.MembershipRole(role),
		Status:             gen.MembershipStatusInvited,
		DepartmentID:       departmentID,
		ApprovalLimitMinor: approvalLimit,
		InvitedBy:          &invitedBy,
	})
	if err != nil {
		return tenant.Membership{}, translate(err)
	}
	return toDomainMembership(row), nil
}

// MemberRecord joins a membership to the user behind it, for the members list.
type MemberRecord struct {
	tenant.Membership
	Email          string
	FullName       *string
	DepartmentName *string
}

func (r *TenancyRepository) ListMembers(ctx context.Context, tc *postgres.TenantConn) ([]MemberRecord, error) {
	rows, err := gen.New(tc).ListMemberships(ctx, tc.TenantID())
	if err != nil {
		return nil, translate(err)
	}
	out := make([]MemberRecord, len(rows))
	for i, row := range rows {
		out[i] = MemberRecord{
			Membership: tenant.Membership{
				ID:                 row.ID,
				TenantID:           row.TenantID,
				UserID:             row.UserID,
				Role:               tenant.Role(row.Role),
				Status:             tenant.MembershipStatus(row.Status),
				ApprovalLimitMinor: row.ApprovalLimitMinor,
				DepartmentID:       row.DepartmentID,
				InvitedBy:          row.InvitedBy,
				CreatedAt:          row.CreatedAt,
				UpdatedAt:          row.UpdatedAt,
			},
			Email:          row.Email,
			FullName:       row.FullName,
			DepartmentName: row.DepartmentName,
		}
	}
	return out, nil
}

// UpdateMember changes a member's standing.
//
// The rule that an actor may only administer someone strictly below them is
// enforced in the service layer, where the acting role is known. It is not
// enforced here because a repository that made authorisation decisions would
// have to be given the actor, and then there would be two places that decide
// the same thing.
func (r *TenancyRepository) UpdateMember(
	ctx context.Context,
	tc *postgres.TenantConn,
	id uuid.UUID,
	role tenant.Role,
	status tenant.MembershipStatus,
	departmentID *uuid.UUID,
	approvalLimit *int64,
) (tenant.Membership, error) {
	row, err := gen.New(tc).UpdateMembership(ctx, gen.UpdateMembershipParams{
		TenantID:           tc.TenantID(),
		ID:                 id,
		Role:               gen.MembershipRole(role),
		Status:             gen.MembershipStatus(status),
		DepartmentID:       departmentID,
		ApprovalLimitMinor: approvalLimit,
	})
	if err != nil {
		return tenant.Membership{}, translate(err)
	}
	return toDomainMembership(row), nil
}

func (r *TenancyRepository) GetTenant(ctx context.Context, tc *postgres.TenantConn) (tenant.Tenant, error) {
	row, err := gen.New(tc).GetTenant(ctx, tc.TenantID())
	if err != nil {
		return tenant.Tenant{}, translate(err)
	}
	return tenant.Tenant{
		ID:                 row.ID,
		Slug:               row.Slug,
		Name:               row.Name,
		Status:             tenant.Status(row.Status),
		DefaultCurrency:    shared.Currency(row.DefaultCurrency),
		BillingCustomerRef: row.BillingCustomerRef,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
	}, nil
}

func toDomainMembership(row gen.Membership) tenant.Membership {
	return tenant.Membership{
		ID:                 row.ID,
		TenantID:           row.TenantID,
		UserID:             row.UserID,
		Role:               tenant.Role(row.Role),
		Status:             tenant.MembershipStatus(row.Status),
		ApprovalLimitMinor: row.ApprovalLimitMinor,
		DepartmentID:       row.DepartmentID,
		InvitedBy:          row.InvitedBy,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
	}
}

// -----------------------------------------------------------------------------
// Refresh tokens
// -----------------------------------------------------------------------------

// RefreshToken is a stored session credential. The token itself is never here
// - only its digest - so a database read cannot be turned into a live session.
type RefreshToken struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	FamilyID  uuid.UUID
	IssuedAt  time.Time
	ExpiresAt time.Time
	UsedAt    *time.Time
	RevokedAt *time.Time
}

func (r *TenancyRepository) StoreRefreshToken(
	ctx context.Context,
	db *postgres.DB,
	userID, familyID uuid.UUID,
	digest []byte,
	expiresAt time.Time,
	userAgent *string,
	clientIP *netip.Addr,
) (RefreshToken, error) {
	row, err := gen.New(db.Pool()).CreateRefreshToken(ctx, gen.CreateRefreshTokenParams{
		UserID:    userID,
		TokenHash: digest,
		FamilyID:  familyID,
		ExpiresAt: expiresAt,
		UserAgent: userAgent,
		ClientIp:  clientIP,
	})
	if err != nil {
		return RefreshToken{}, translate(err)
	}
	return toDomainRefreshToken(row), nil
}

func (r *TenancyRepository) FindRefreshToken(ctx context.Context, db *postgres.DB, digest []byte) (RefreshToken, error) {
	row, err := gen.New(db.Pool()).GetRefreshTokenByHash(ctx, digest)
	if err != nil {
		return RefreshToken{}, translate(err)
	}
	return toDomainRefreshToken(row), nil
}

// ConsumeRefreshToken marks a token used, and reports whether it won the race.
//
// The UPDATE carries `used_at IS NULL`, so exactly one of several concurrent
// refreshes succeeds. The losers are indistinguishable from a replay - and are
// treated as one, because they might be.
func (r *TenancyRepository) ConsumeRefreshToken(ctx context.Context, db *postgres.DB, id uuid.UUID) (bool, error) {
	n, err := gen.New(db.Pool()).MarkRefreshTokenUsed(ctx, id)
	if err != nil {
		return false, translate(err)
	}
	return n == 1, nil
}

// RevokeRefreshFamily ends every session descended from one login.
//
// Called when a token that was already used comes back. That is evidence the
// token was copied: the legitimate client rotated it and would not send it
// again. There is no way to tell the thief from the victim, so both are logged
// out and asked to authenticate.
func (r *TenancyRepository) RevokeRefreshFamily(ctx context.Context, db *postgres.DB, familyID uuid.UUID) error {
	_, err := gen.New(db.Pool()).RevokeRefreshTokenFamily(ctx, familyID)
	return translate(err)
}

func toDomainRefreshToken(row gen.RefreshToken) RefreshToken {
	return RefreshToken{
		ID:        row.ID,
		UserID:    row.UserID,
		FamilyID:  row.FamilyID,
		IssuedAt:  row.IssuedAt,
		ExpiresAt: row.ExpiresAt,
		UsedAt:    row.UsedAt,
		RevokedAt: row.RevokedAt,
	}
}
