import { Link } from "react-router";

import { expenseListQuery } from "@/entities/expense";
import { useCan } from "@/entities/session";
import { ExportMenu } from "@/features/expense-export";
import {
  ExpenseFilterBar,
  exportSearch,
  isFiltered,
  listSearch,
  useExpenseFilters,
} from "@/features/expense-filters";
import { ApiError } from "@/shared/api";
import { usePagedQuery } from "@/shared/lib/paged";
import { Card, EmptyState, ErrorNotice, SkeletonRows } from "@/shared/ui/kit";
import { PageHeader } from "@/shared/ui/PageHeader";
import { PlusIcon } from "@/shared/ui/icons";
import { Pagination } from "@/shared/ui/Pagination";
import { ExpenseTable } from "@/widgets/expense-table";

export function ExpensesPage() {
  const { filters } = useExpenseFilters();
  const canExport = useCan("report:export");

  const page = usePagedQuery(expenseListQuery(listSearch(filters)));

  return (
    <div>
      <PageHeader
        title="Expenses"
        description="Keep your spending in check. Submit, track and manage all company expenses."
        actions={
          <>
            {canExport && <ExportMenu queryFor={(format) => exportSearch(filters, format)} />}
            <Link
              to="/expenses/new"
              className="inline-flex items-center gap-1.5 rounded-lg bg-accent px-3.5 py-2 text-sm font-medium text-accent-ink transition-colors hover:bg-accent-hover"
            >
              <PlusIcon className="size-4" />
              New claim
            </Link>
          </>
        }
      />

      <div className="flex flex-col gap-5">
      <ExpenseFilterBar />

      {page.error && (
        <ErrorNotice
          title="Could not load the claims"
          detail={page.error.message}
          traceId={page.error instanceof ApiError ? page.error.traceId : undefined}
        />
      )}

      <Card>
        {page.initial ? (
          <SkeletonRows rows={6} columns={5} />
        ) : page.items.length === 0 ? (
          <EmptyState
            title="No claims match"
            detail={
              isFiltered(filters)
                ? "Try widening the date range or clearing a filter."
                : "Nothing has been filed yet."
            }
          />
        ) : (
          <>
            <ExpenseTable claims={page.items} />
            <Pagination
              page={page.pageNumber}
              hasPrevious={page.hasPrevious}
              hasNext={page.hasNext}
              onPrevious={page.previous}
              onNext={page.next}
              busy={page.busy}
            />
          </>
        )}
      </Card>
      </div>
    </div>
  );
}
