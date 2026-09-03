-- Every query here names tenant_id explicitly even though RLS already
-- restricts the rows.
--
-- That is deliberate redundancy, and it buys two things. The planner sees a
-- constant equality on the leading column of expenses_tenant_spent_at_idx and
-- friends, which is what turns these into index seeks. And if a future
-- migration ever disables a policy, or a table is added without one, the
-- queries still isolate. Two independent mechanisms, neither assumed from the
-- other. The value always comes from TenantConn.TenantID(), which is the
-- binding itself, so it cannot disagree with what RLS is enforcing.

-- name: CreateExpense :one
INSERT INTO expenses (
    id, tenant_id, submitter_id, department_id, status, category,
    amount_minor, currency, merchant, description, spent_at,
    revision, version, source_subscription_id, created_at, updated_at
) VALUES (
    @id, @tenant_id, @submitter_id, @department_id, @status, @category,
    @amount_minor, @currency, @merchant, @description, @spent_at,
    @revision, @version, @source_subscription_id, @created_at, @updated_at
)
RETURNING *;

-- name: GetExpense :one
SELECT * FROM expenses
WHERE tenant_id = @tenant_id AND id = @id;

-- GetExpenseForUpdate takes a row lock for the duration of the transaction.
--
-- Read-decide-write is only atomic while the row is held. Without the lock,
-- two approvers loading the same pending claim both see 'pending_approval',
-- both decide it is theirs to approve, and the second write lands on a row the
-- first already moved. The compare-and-swap in TransitionExpense would catch
-- that and return no rows - but only after the service had already appended an
-- event to the ledger, because the ledger insert and the update are two
-- statements. Taking the lock first makes the whole sequence serial.
--
-- NOWAIT rather than a blocking wait: a second approver clicking during the
-- first one's transaction should be told to reload, not held for the duration
-- of a statement_timeout. The 55P03 lock_not_available is translated to
-- ErrConflict by the repository.
-- name: GetExpenseForUpdate :one
SELECT * FROM expenses
WHERE tenant_id = @tenant_id AND id = @id
FOR UPDATE NOWAIT;

-- TransitionExpense is a compare-and-swap on the version column.
--
-- `WHERE version = @expected_version` is what makes a lost update impossible.
-- If any other transaction changed the row between the read and this write,
-- the predicate matches nothing, no row is returned, and the repository
-- reports ErrStaleWrite rather than overwriting a decision nobody saw.
-- name: TransitionExpense :one
UPDATE expenses
SET status        = @status,
    submitted_at  = @submitted_at,
    decided_at    = @decided_at,
    decided_by    = @decided_by,
    decision_note = @decision_note,
    paid_at       = @paid_at,
    payment_ref   = @payment_ref,
    revision      = @revision,
    version       = @version,
    updated_at    = @updated_at
WHERE tenant_id = @tenant_id
  AND id        = @id
  AND version   = @expected_version
RETURNING *;

-- name: UpdateExpenseDraft :one
UPDATE expenses
SET department_id = @department_id,
    category      = @category,
    amount_minor  = @amount_minor,
    currency      = @currency,
    merchant      = @merchant,
    description   = @description,
    spent_at      = @spent_at,
    version       = @version,
    updated_at    = @updated_at
WHERE tenant_id = @tenant_id
  AND id        = @id
  AND version   = @expected_version
  -- Belt and braces with expenses_delete_drafts_only's sibling policy and with
  -- the state machine: an edit can only ever touch a draft.
  AND status    = 'draft'
RETURNING *;

-- The RLS policy expenses_delete_drafts_only refuses this for any other
-- status, so the predicate here is documentation of an existing guarantee
-- rather than the guarantee itself.
-- name: DeleteExpenseDraft :execrows
DELETE FROM expenses
WHERE tenant_id = @tenant_id AND id = @id AND status = 'draft';

-- ListExpenses is the dashboard's main query: keyset pagination with every
-- filter optional.
--
-- The `sqlc.narg(x) IS NULL OR column = sqlc.narg(x)` idiom keeps this as one
-- prepared statement instead of a string builder, which is what keeps it
-- injection-free by construction. The cost is that PostgreSQL plans it
-- generically, so a filter combination that would suit a partial index does
-- not necessarily get one - which is why the approver queue, the single
-- highest-traffic filtered read, has its own query below rather than being
-- expressed as a status filter here.
--
-- The caller passes page_limit = size + 1 and uses the extra row to decide
-- has_more. A COUNT(*) over a filtered tenant history would cost more than the
-- page itself.
-- name: ListExpenses :many
SELECT * FROM expenses
WHERE tenant_id = @tenant_id
  AND (sqlc.narg('status')::expense_status IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('category')::expense_category IS NULL OR category = sqlc.narg('category'))
  AND (sqlc.narg('department_id')::uuid IS NULL OR department_id = sqlc.narg('department_id'))
  AND (sqlc.narg('submitter_id')::uuid IS NULL OR submitter_id = sqlc.narg('submitter_id'))
  AND (sqlc.narg('spent_from')::date IS NULL OR spent_at >= sqlc.narg('spent_from'))
  AND (sqlc.narg('spent_to')::date IS NULL OR spent_at <= sqlc.narg('spent_to'))
  AND (sqlc.narg('min_minor')::bigint IS NULL OR amount_minor >= sqlc.narg('min_minor'))
  AND (sqlc.narg('max_minor')::bigint IS NULL OR amount_minor <= sqlc.narg('max_minor'))
  AND (sqlc.narg('search')::text IS NULL
       OR merchant ILIKE '%' || sqlc.narg('search')::text || '%'
       OR description ILIKE '%' || sqlc.narg('search')::text || '%')
  -- Row-value comparison, not `spent_at < x OR (spent_at = x AND id < y)`.
  -- The tuple form is what the planner turns into a single seek on
  -- (tenant_id, spent_at DESC, id DESC); the expanded OR form does not get
  -- the same treatment.
  AND (sqlc.narg('cursor_spent_at')::date IS NULL
       OR (spent_at, id) < (sqlc.narg('cursor_spent_at')::date, sqlc.narg('cursor_id')::uuid))
ORDER BY spent_at DESC, id DESC
LIMIT @page_limit;

-- ListPendingForApproval serves the approver's queue from
-- expenses_pending_queue_idx, which holds only pending rows. A tenant with two
-- years of settled claims and nine pending ones scans a nine-row index.
--
-- Oldest first, which is the opposite of every other listing: a queue is
-- worked from the front.
-- name: ListPendingForApproval :many
SELECT * FROM expenses
WHERE tenant_id = @tenant_id
  AND status = 'pending_approval'
  AND (sqlc.narg('department_id')::uuid IS NULL OR department_id = sqlc.narg('department_id'))
  AND (sqlc.narg('cursor_submitted_at')::timestamptz IS NULL
       OR (submitted_at, id) > (sqlc.narg('cursor_submitted_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
ORDER BY submitted_at ASC, id ASC
LIMIT @page_limit;

-- The export scan is deliberately NOT here.
--
-- sqlc emits a :many query as a function that returns a slice, which means it
-- calls pgx.CollectRows and materialises the entire result set before the
-- caller sees the first row. For a report covering a hundred thousand claims
-- that is a hundred thousand structs resident at once, which is precisely what
-- the streaming export exists to avoid.
--
-- ExpenseRepository.StreamForExport in expense_repo.go therefore holds that
-- one query as hand-written SQL and iterates rows.Next() directly. It is the
-- only query in the service outside sqlc's reach, and TestExportQueryMatchesSchema
-- runs it against a live schema so a column renamed in a migration still
-- breaks the build's test stage rather than production.

-- name: CountExpensesByStatus :many
SELECT status, count(*) AS claim_count, coalesce(sum(amount_minor), 0)::bigint AS total_minor
  FROM expenses
 WHERE tenant_id = @tenant_id
   AND (sqlc.narg('spent_from')::date IS NULL OR spent_at >= sqlc.narg('spent_from'))
   AND (sqlc.narg('spent_to')::date IS NULL OR spent_at <= sqlc.narg('spent_to'))
 GROUP BY status;

-- ---------------------------------------------------------------------------
-- Audit ledger
-- ---------------------------------------------------------------------------

-- name: AppendExpenseEvent :one
INSERT INTO expense_events (
    tenant_id, expense_id, action, from_status, to_status,
    actor_id, reason, amount_minor, currency, revision, metadata, occurred_at
) VALUES (
    @tenant_id, @expense_id, @action, @from_status, @to_status,
    @actor_id, @reason, @amount_minor, @currency, @revision, @metadata, @occurred_at
)
RETURNING *;

-- name: ListExpenseEvents :many
SELECT ev.*, u.email AS actor_email
  FROM expense_events ev
  LEFT JOIN memberships m ON m.id = ev.actor_id AND m.tenant_id = ev.tenant_id
  LEFT JOIN users u       ON u.id = m.user_id
 WHERE ev.tenant_id = @tenant_id
   AND ev.expense_id = @expense_id
 ORDER BY ev.occurred_at DESC, ev.id DESC;

-- ---------------------------------------------------------------------------
-- Attachments
-- ---------------------------------------------------------------------------

-- name: AddExpenseAttachment :one
INSERT INTO expense_attachments (
    tenant_id, expense_id, object_key, filename, content_type,
    size_bytes, checksum, uploaded_by
) VALUES (
    @tenant_id, @expense_id, @object_key, @filename, @content_type,
    @size_bytes, @checksum, @uploaded_by
)
RETURNING *;

-- name: ListExpenseAttachments :many
SELECT * FROM expense_attachments
WHERE tenant_id = @tenant_id AND expense_id = @expense_id
ORDER BY created_at ASC;
