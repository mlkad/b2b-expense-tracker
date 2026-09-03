-- Identity, tenancy and membership.
--
-- users and refresh_tokens are outside the RLS model - login has to find a
-- user by email before any tenant is known - so the queries that touch them
-- are written to be safe on their own terms: every one is keyed by a primary
-- key or a unique index, and none of them accepts a filter that could return
-- an unbounded set of other people's rows.

-- name: CreateUser :one
INSERT INTO users (email, password_hash, full_name)
VALUES (@email, @password_hash, @full_name)
RETURNING *;

-- name: GetUserByEmail :one
SELECT * FROM users
WHERE email = @email AND deleted_at IS NULL;

-- name: GetUserByID :one
SELECT * FROM users
WHERE id = @id AND deleted_at IS NULL;

-- name: UpdateUserPassword :execrows
UPDATE users SET password_hash = @password_hash
WHERE id = @id AND deleted_at IS NULL;

-- ---------------------------------------------------------------------------
-- Tenants
-- ---------------------------------------------------------------------------

-- Runs in a system transaction: at signup there is no tenant to bind to yet,
-- which is why the tenant_provisioning policy requires app.is_system().
-- name: CreateTenant :one
INSERT INTO tenants (slug, name, default_currency)
VALUES (@slug, @name, @default_currency)
RETURNING *;

-- name: GetTenant :one
SELECT * FROM tenants WHERE id = @id;

-- name: GetTenantBySlug :one
SELECT * FROM tenants WHERE slug = @slug;

-- name: UpdateTenant :one
UPDATE tenants
SET name = @name, default_currency = @default_currency
WHERE id = @id
RETURNING *;

-- name: SetTenantBillingRef :execrows
UPDATE tenants SET billing_customer_ref = @billing_customer_ref WHERE id = @id;

-- ---------------------------------------------------------------------------
-- Memberships
-- ---------------------------------------------------------------------------

-- ResolveActor is the query on the authenticated request path.
--
-- It is run on every request, so it is one round trip and it is answered from
-- memberships_tenant_user_key. It reads the role from the database rather than
-- taking it from the JWT: a token issued before a demotion would otherwise
-- keep its old authority until it expired, which is a window in which a
-- removed employee can still approve payments.
--
-- The tenant's own status is joined in so that a suspended tenant is refused
-- by the same check that refuses a suspended member. Two separate checks means
-- one of them eventually gets skipped on a new endpoint.
-- name: ResolveActor :one
SELECT m.id            AS membership_id,
       m.tenant_id,
       m.user_id,
       m.role,
       m.status,
       m.department_id,
       m.approval_limit_minor,
       t.status        AS tenant_status,
       t.default_currency,
       t.slug          AS tenant_slug
  FROM memberships m
  JOIN tenants t ON t.id = m.tenant_id
 WHERE m.tenant_id = @tenant_id
   AND m.user_id   = @user_id;

-- ListMembershipsForUser powers the tenant switcher. It runs without a tenant
-- binding - the caller has not chosen one yet - so it is a system transaction
-- scoped by user_id, and it returns only the tenants that user belongs to.
-- name: ListMembershipsForUser :many
SELECT m.id AS membership_id,
       m.role,
       m.status,
       t.id   AS tenant_id,
       t.slug,
       t.name,
       t.status AS tenant_status
  FROM memberships m
  JOIN tenants t ON t.id = m.tenant_id
 WHERE m.user_id = @user_id
   AND m.status <> 'suspended'
 ORDER BY t.name ASC;

-- name: CreateMembership :one
INSERT INTO memberships (
    tenant_id, user_id, role, status, approval_limit_minor, department_id, invited_by
) VALUES (
    @tenant_id, @user_id, @role, @status, @approval_limit_minor, @department_id, @invited_by
)
RETURNING *;

-- name: ListMemberships :many
SELECT m.*, u.email, u.full_name, d.name AS department_name
  FROM memberships m
  JOIN users u ON u.id = m.user_id
  LEFT JOIN departments d ON d.id = m.department_id AND d.tenant_id = m.tenant_id
 WHERE m.tenant_id = @tenant_id
 ORDER BY u.email ASC;

-- name: GetMembership :one
SELECT * FROM memberships WHERE tenant_id = @tenant_id AND id = @id;

-- name: UpdateMembership :one
UPDATE memberships
SET role                 = @role,
    status               = @status,
    approval_limit_minor = @approval_limit_minor,
    department_id        = @department_id
WHERE tenant_id = @tenant_id AND id = @id
RETURNING *;

-- ---------------------------------------------------------------------------
-- Refresh tokens
-- ---------------------------------------------------------------------------

-- name: CreateRefreshToken :one
INSERT INTO refresh_tokens (user_id, token_hash, family_id, expires_at, user_agent, client_ip)
VALUES (@user_id, @token_hash, @family_id, @expires_at, @user_agent, @client_ip)
RETURNING *;

-- name: GetRefreshTokenByHash :one
SELECT * FROM refresh_tokens WHERE token_hash = @token_hash;

-- MarkRefreshTokenUsed is the rotation step, and it is a compare-and-swap:
-- `used_at IS NULL` means only one concurrent request can consume a token. The
-- loser sees zero rows affected, which is indistinguishable from replay - and
-- is treated as replay, because it might be.
-- name: MarkRefreshTokenUsed :execrows
UPDATE refresh_tokens SET used_at = now()
WHERE id = @id AND used_at IS NULL AND revoked_at IS NULL;

-- A presented token that was already used is evidence the token was copied:
-- the legitimate client rotated it and would not send it again. There is no
-- way to tell the thief from the victim, so the whole rotation family is
-- revoked and both are asked to log in again.
-- name: RevokeRefreshTokenFamily :execrows
UPDATE refresh_tokens SET revoked_at = now()
WHERE family_id = @family_id AND revoked_at IS NULL;

-- name: DeleteExpiredRefreshTokens :execrows
DELETE FROM refresh_tokens WHERE expires_at < now() - @grace::interval;

-- ---------------------------------------------------------------------------
-- Notification recipients
-- ---------------------------------------------------------------------------

-- ListApprovers finds who should be told a claim needs a decision.
--
-- Tenant-wide approvers plus the ones scoped to the claim's own department.
-- A manager scoped elsewhere is deliberately excluded: telling them about a
-- claim they cannot act on trains people to ignore the notification, which is
-- how the ones that matter get missed.
--
-- Suspended and invited memberships are excluded because neither can act, and
-- soft-deleted users because their address may have been reassigned.
-- name: ListApprovers :many
SELECT u.email, u.full_name, m.id AS membership_id, m.role
  FROM memberships m
  JOIN users u ON u.id = m.user_id
 WHERE m.tenant_id = @tenant_id
   AND m.status = 'active'
   AND u.deleted_at IS NULL
   AND m.role IN ('owner', 'admin', 'manager')
   AND (m.department_id IS NULL
        OR sqlc.narg('department_id')::uuid IS NULL
        OR m.department_id = sqlc.narg('department_id'))
 ORDER BY u.email;

-- ListFinance finds who settles payments and watches budgets.
-- name: ListFinance :many
SELECT u.email, u.full_name, m.id AS membership_id, m.role
  FROM memberships m
  JOIN users u ON u.id = m.user_id
 WHERE m.tenant_id = @tenant_id
   AND m.status = 'active'
   AND u.deleted_at IS NULL
   AND m.role IN ('owner', 'finance')
 ORDER BY u.email;

-- name: GetMemberContact :one
SELECT u.email, u.full_name
  FROM memberships m
  JOIN users u ON u.id = m.user_id
 WHERE m.tenant_id = @tenant_id AND m.id = @id AND u.deleted_at IS NULL;
