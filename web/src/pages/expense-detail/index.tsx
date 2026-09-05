import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "react-router";

import { expenseHistoryQuery, expenseQuery, StatusBadge } from "@/entities/expense";
import { useProfile } from "@/entities/session";
import { DiscardDraft, ExpenseActions } from "@/features/expense-decision";
import { ApiError } from "@/shared/api";
import { formatDate, formatTimestamp, sentenceCase } from "@/shared/lib/format";
import { formatMoney } from "@/shared/lib/money";
import { Card, ErrorNotice, SkeletonRows } from "@/shared/ui/kit";
import { ReceiptPanel } from "@/widgets/receipt-panel";

export function ExpenseDetailPage() {
  const { id = "" } = useParams();
  const profile = useProfile();

  const claim = useQuery(expenseQuery(id));
  const history = useQuery(expenseHistoryQuery(id));

  if (claim.isPending) {
    return (
      <Card>
        <SkeletonRows rows={5} columns={2} />
      </Card>
    );
  }

  if (!claim.data) {
    const error = claim.error instanceof ApiError ? claim.error : null;
    return (
      <ErrorNotice
        title="That claim is not available"
        detail={
          error?.message ??
          "It may have been deleted, or it belongs to another organisation."
        }
        traceId={error?.traceId}
      />
    );
  }

  const detail = claim.data;
  const mine = detail.submitter_id === profile?.membership_id;

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <Link to="/expenses" className="text-sm text-brand-600 hover:underline">
            ← All claims
          </Link>
          <h1 className="mt-1 flex items-center gap-3 text-lg font-semibold">
            {detail.merchant}
            <StatusBadge status={detail.status} />
          </h1>
        </div>
        <p className="text-2xl font-semibold tabular-nums">{formatMoney(detail.amount)}</p>
      </div>

      <ExpenseActions id={detail.id} actions={detail.allowed_actions ?? []}>
        {mine && detail.status === "draft" && (
          <Link
            to={`/expenses/${detail.id}/edit`}
            className="inline-flex items-center rounded-md border border-ink-200 bg-white px-3.5 py-2 text-sm font-medium hover:bg-ink-50"
          >
            Edit
          </Link>
        )}
      </ExpenseActions>

      <div className="grid gap-6 lg:grid-cols-3">
        <Card className="p-5 lg:col-span-2">
          <h2 className="mb-4 text-sm font-medium text-ink-800">Details</h2>
          <dl className="grid gap-x-6 gap-y-3 sm:grid-cols-2">
            <Detail term="Spent on" value={formatDate(detail.spent_at)} />
            <Detail term="Category" value={sentenceCase(detail.category)} />
            <Detail term="Submitted" value={formatTimestamp(detail.submitted_at)} />
            <Detail term="Decided" value={formatTimestamp(detail.decided_at)} />
            {detail.paid_at && <Detail term="Paid" value={formatTimestamp(detail.paid_at)} />}
            {detail.payment_ref && (
              <Detail term="Payment reference" value={detail.payment_ref} />
            )}
            {detail.revision > 1 && <Detail term="Revision" value={String(detail.revision)} />}
          </dl>

          {detail.description && (
            <>
              <h3 className="mt-5 mb-1 text-xs font-medium uppercase tracking-wide text-ink-600">
                Description
              </h3>
              <p className="text-sm">{detail.description}</p>
            </>
          )}

          {detail.decision_note && (
            <div className="mt-5 border-l-2 border-ink-200 pl-3">
              <h3 className="mb-1 text-xs font-medium uppercase tracking-wide text-ink-600">
                Decision note
              </h3>
              <p className="text-sm">{detail.decision_note}</p>
            </div>
          )}
        </Card>

        <div className="flex flex-col gap-6">
          <ReceiptPanel
            expenseId={detail.id}
            // The server refuses both anyway - attaching to a submitted claim,
            // and removing a receipt from one - so this only avoids offering a
            // control that would be refused.
            canAttach={mine && detail.status === "draft"}
            canDelete={mine && detail.status === "draft"}
          />

          <Card className="p-5">
            <h2 className="mb-4 text-sm font-medium text-ink-800">History</h2>
            {(history.data ?? []).length === 0 ? (
              <p className="text-sm text-ink-600">Nothing recorded yet.</p>
            ) : (
              <ol className="flex flex-col gap-3">
                {(history.data ?? []).map((event) => (
                  <li key={event.id} className="border-l-2 border-ink-100 pl-3">
                    <p className="text-sm font-medium">{sentenceCase(event.action)}</p>
                    <p className="text-xs text-ink-600">
                      {formatTimestamp(event.occurred_at)}
                      {event.actor_email ? ` · ${event.actor_email}` : " · system"}
                    </p>
                    {/* The amount as it was at the time, not today's. An audit
                        row saying "approved" without saying what was approved
                        is useless once the claim has been revised. */}
                    <p className="text-xs text-ink-600 tabular-nums">
                      {formatMoney(event.amount)}
                    </p>
                    {event.reason && <p className="mt-1 text-xs">{event.reason}</p>}
                  </li>
                ))}
              </ol>
            )}
          </Card>
        </div>
      </div>

      {detail.status === "draft" && mine && <DiscardDraft id={detail.id} />}
    </div>
  );
}

function Detail({ term, value }: { term: string; value: string }) {
  return (
    <div>
      <dt className="text-xs uppercase tracking-wide text-ink-600">{term}</dt>
      <dd className="mt-0.5 text-sm">{value}</dd>
    </div>
  );
}
