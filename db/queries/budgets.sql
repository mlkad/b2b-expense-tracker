-- name: CreateDepartment :one
INSERT INTO departments (tenant_id, name, parent_id, head_user_id)
VALUES (@tenant_id, @name, @parent_id, @head_user_id)
RETURNING *;

-- name: ListDepartments :many
SELECT * FROM departments
WHERE tenant_id = @tenant_id AND (@include_archived::boolean OR archived_at IS NULL)
ORDER BY name ASC;

-- name: GetDepartment :one
SELECT * FROM departments WHERE tenant_id = @tenant_id AND id = @id;

-- name: ArchiveDepartment :execrows
UPDATE departments SET archived_at = now()
WHERE tenant_id = @tenant_id AND id = @id AND archived_at IS NULL;

-- ---------------------------------------------------------------------------

-- name: CreateBudget :one
INSERT INTO budgets (tenant_id, department_id, period_start, period_end, amount_minor, currency, alert_threshold_bps)
VALUES (@tenant_id, @department_id, @period_start, @period_end, @amount_minor, @currency, @alert_threshold_bps)
RETURNING *;

-- name: ListBudgets :many
SELECT * FROM budgets
WHERE tenant_id = @tenant_id
  AND (sqlc.narg('department_id')::uuid IS NULL OR department_id = sqlc.narg('department_id'))
  AND (sqlc.narg('on_date')::date IS NULL
       OR sqlc.narg('on_date')::date BETWEEN period_start AND period_end)
ORDER BY period_start DESC;

-- BudgetConsumption answers "how much of each envelope is spoken for".
--
-- The join condition, not a WHERE clause, carries the status filter: a budget
-- with no matching expenses must still appear with a zero, and moving that
-- predicate into WHERE would turn the LEFT JOIN into an inner one and drop
-- exactly the budgets a dashboard most needs to show.
--
-- 'approved' and 'paid' only. Counting pending claims would let anyone exhaust
-- a budget on paper by submitting claims nobody agreed to - and it is the same
-- predicate as expenses_budget_rollup_idx, which is what makes this an
-- index-only scan.
-- name: BudgetConsumption :many
SELECT b.id            AS budget_id,
       b.department_id,
       d.name          AS department_name,
       b.period_start,
       b.period_end,
       b.amount_minor  AS budget_minor,
       b.currency,
       b.alert_threshold_bps,
       coalesce(sum(e.amount_minor), 0)::bigint AS consumed_minor,
       count(e.id)                              AS claim_count
  FROM budgets b
  LEFT JOIN departments d
         ON d.id = b.department_id AND d.tenant_id = b.tenant_id
  LEFT JOIN expenses e
         ON e.tenant_id = b.tenant_id
        AND e.status IN ('approved', 'paid')
        AND e.spent_at BETWEEN b.period_start AND b.period_end
        AND (b.department_id IS NULL OR e.department_id = b.department_id)
 WHERE b.tenant_id = @tenant_id
   AND (sqlc.narg('on_date')::date IS NULL
        OR sqlc.narg('on_date')::date BETWEEN b.period_start AND b.period_end)
 GROUP BY b.id, d.name
 ORDER BY b.period_start DESC, d.name ASC;

-- SpendByDepartment is the dashboard's headline chart and the department sheet
-- of the export.
-- name: SpendByDepartment :many
SELECT coalesce(d.name, 'Unassigned')          AS department_name,
       e.department_id,
       count(*)                                AS claim_count,
       coalesce(sum(e.amount_minor), 0)::bigint AS total_minor,
       e.currency
  FROM expenses e
  LEFT JOIN departments d ON d.id = e.department_id AND d.tenant_id = e.tenant_id
 WHERE e.tenant_id = @tenant_id
   AND e.status IN ('approved', 'paid')
   AND (sqlc.narg('spent_from')::date IS NULL OR e.spent_at >= sqlc.narg('spent_from'))
   AND (sqlc.narg('spent_to')::date IS NULL OR e.spent_at <= sqlc.narg('spent_to'))
 GROUP BY d.name, e.department_id, e.currency
 ORDER BY total_minor DESC;
