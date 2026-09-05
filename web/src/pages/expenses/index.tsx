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
import { Pagination } from "@/shared/ui/Pagination";
import { ExpenseTable } from "@/widgets/expense-table";

export function ExpensesPage() {
  const { filters } = useExpenseFilters();
  const canExport = useCan("report:export");

  const page = usePagedQuery(expenseListQuery(listSearch(filters)));

  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-center justify-between">
        <h1 className="text-lg font-semibold">Expenses</h1>
        <div className="flex gap-2">
          {canExport && <ExportMenu queryFor={(format) => exportSearch(filters, format)} />}
          <Link
            to="/expenses/new"
            className="inline-flex items-center rounded-md bg-brand-600 px-3.5 py-2 text-sm font-medium text-white hover:bg-brand-700"
          >
            New claim
          </Link>
        </div>
      </div>

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
  );
}
