import { pendingExpensesQuery } from "@/entities/expense";
import { useProfile } from "@/entities/session";
import { ApiError } from "@/shared/api";
import { usePagedQuery } from "@/shared/lib/paged";
import { Card, EmptyState, ErrorNotice, SkeletonRows } from "@/shared/ui/kit";
import { Pagination } from "@/shared/ui/Pagination";
import { ExpenseTable } from "@/widgets/expense-table";

/**
 * The approver's queue.
 *
 * Oldest first, which is the opposite of every other listing here: a queue is
 * worked from the front, and the claim somebody has been waiting on longest is
 * the one that matters. The server orders it that way and serves it from a
 * partial index holding only pending rows.
 */
export function ApprovalsPage() {
  const profile = useProfile();
  const page = usePagedQuery(pendingExpensesQuery("limit=20"));

  const unlimited = (profile?.approval_limit_minor ?? 0) < 0;

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-lg font-semibold">Approvals</h1>
        <p className="mt-1 text-sm text-ink-600">
          Oldest first. You cannot decide on your own claim
          {profile?.department_id ? ", and this queue is limited to your department" : ""}
          {unlimited ? "." : ", up to your approval limit."}
        </p>
      </div>

      {page.error && (
        <ErrorNotice
          title="Could not load the queue"
          detail={page.error.message}
          traceId={page.error instanceof ApiError ? page.error.traceId : undefined}
        />
      )}

      <Card>
        {page.initial ? (
          <SkeletonRows rows={5} columns={4} />
        ) : page.items.length === 0 ? (
          <EmptyState
            title="Nothing waiting"
            detail="Claims appear here as soon as somebody submits one you can decide on."
          />
        ) : (
          <>
            <ExpenseTable claims={page.items} variant="queue" />
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
