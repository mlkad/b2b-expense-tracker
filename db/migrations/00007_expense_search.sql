-- =============================================================================
-- 00007_expense_search
--
-- Merchant search for the dashboard's filter bar.
--
-- The obvious implementation, `merchant ILIKE '%figma%'`, cannot use a B-tree
-- index: a leading wildcard means there is no prefix to seek on, so every
-- keystroke in the search box costs a sequential scan of the tenant's whole
-- history. A trigram GIN index makes the same query an index scan, which is
-- why the extension is worth its write-time cost here.
-- =============================================================================

-- +goose Up

CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- gin_trgm_ops supports LIKE, ILIKE and regex matching on the indexed column.
-- The index covers only tenant-scoped rows the search endpoint can reach; the
-- tenant predicate itself is not in the index because GIN does not support a
-- leading equality column the way B-tree does. RLS still applies - the planner
-- intersects this index's result with the tenant filter.
CREATE INDEX expenses_merchant_trgm_idx
    ON expenses USING gin (merchant gin_trgm_ops);

-- Description is searched less often and is much larger; a separate index
-- keeps the common case (merchant only) cheap to maintain.
CREATE INDEX expenses_description_trgm_idx
    ON expenses USING gin (description gin_trgm_ops)
    WHERE description IS NOT NULL;

-- +goose Down

DROP INDEX IF EXISTS expenses_description_trgm_idx;
DROP INDEX IF EXISTS expenses_merchant_trgm_idx;
