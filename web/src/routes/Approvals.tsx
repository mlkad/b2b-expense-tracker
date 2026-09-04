import { useCallback, useMemo } from "react";
import { Link } from "react-router";

import type { Expense, Page } from "../api/types";
import { Card, EmptyState, ErrorNotice, SkeletonRows } from "../components/ui";
import { useResource } from "../hooks/useResource";
import { Pagination } from "../components/Pagination";
import { useSession } from "../auth/context";
import { useCursorPages } from "../hooks/useCursorPages";
import { formatDate, formatTimestamp } from "../lib/format";
import { formatMoney } from "../lib/money";

/**
 * The approver's queue.
 *
 * Oldest first, which is the opposite of every other listing here: a queue is
 * worked from the front, and the claim somebody has been waiting on longest is
 * the one that matters. The server orders it that way and serves it from a
 * partial index holding only pending rows.
 */
export function Approvals() {
  const { api, profile } = useSession();

  const pages = useCursorPages();

  const query = useMemo(() => {
    const params = new URLSearchParams({ limit: "20" });
    if (pages.cursor) params.set("cursor", pages.cursor);
    return params.toString();
  }, [pages.cursor]);

  const fetchPage = useCallback(
    (key: string) => api.get<Page<Expense>>(`/expenses/pending?${key}`),
    [api],
  );
  const { data: page, error, loading, initial } = useResource(query, fetchPage);

  const unlimited = (profile?.approval_limit_minor ?? 0) < 0;

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-lg font-semibold">Approvals</h1>
        <p className="mt-1 text-sm text-ink-600">
          Oldest first. You cannot decide on your own claim
          {profile?.department_id ? ", and this queue is limited to your department" : ""}
          {unlimited ? "." : `, up to your approval limit.`}
        </p>
      </div>

      {error && <ErrorNotice title="Could not load the queue" detail={error.message} traceId={error.traceId} />}

      <Card>
        {initial ? (
          <SkeletonRows rows={5} columns={4} />
        ) : !page || page.items.length === 0 ? (
          <EmptyState
            title="Nothing waiting"
            detail="Claims appear here as soon as somebody submits one you can decide on."
          />
        ) : (
          <>
            <table className="w-full text-sm">
              <caption className="sr-only">Claims awaiting a decision</caption>
              <thead>
                <tr className="border-b border-ink-100 text-left text-xs uppercase tracking-wide text-ink-600">
                  <th scope="col" className="px-4 py-2.5 font-medium">Submitted</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">Merchant</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">Spent on</th>
                  <th scope="col" className="px-4 py-2.5 text-right font-medium">Amount</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-ink-100">
                {page.items.map((claim) => (
                  <tr key={claim.id} className="hover:bg-ink-50">
                    <td className="px-4 py-3 whitespace-nowrap text-ink-600">
                      {formatTimestamp(claim.submitted_at)}
                    </td>
                    <td className="px-4 py-3">
                      <Link to={`/expenses/${claim.id}`} className="font-medium text-brand-600 hover:underline">
                        {claim.merchant}
                      </Link>
                      {claim.revision > 1 && (
                        // An approver looking at revision 2 needs to know there
                        // was a decision on revision 1, or they review it as if
                        // it were new.
                        <span className="ml-2 rounded bg-caution-50 px-1.5 py-0.5 text-xs text-caution-700">
                          revision {claim.revision}
                        </span>
                      )}
                    </td>
                    <td className="px-4 py-3 text-ink-600">{formatDate(claim.spent_at)}</td>
                    <td className="px-4 py-3 text-right font-medium tabular-nums">{formatMoney(claim.amount)}</td>
                  </tr>
                ))}
              </tbody>
            </table>

            <Pagination
              page={pages.pageNumber}
              hasPrevious={pages.hasPrevious}
              hasNext={page.has_more}
              onPrevious={pages.previous}
              onNext={() => page.next_cursor && pages.next(page.next_cursor)}
              busy={loading}
            />
          </>
        )}
      </Card>
    </div>
  );
}
