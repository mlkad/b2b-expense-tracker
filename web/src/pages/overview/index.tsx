import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router";

import { entitlementQuery } from "@/entities/billing";
import { summaryQuery } from "@/entities/expense";
import { useCan, useProfile } from "@/entities/session";
import { ApiError } from "@/shared/api";
import { statusLabel } from "@/shared/lib/format";
import { formatMoney } from "@/shared/lib/money";
import { Badge, Card, EmptyState, ErrorNotice, SkeletonRows } from "@/shared/ui/kit";

const STATUS_TONE = {
  draft: "neutral",
  pending_approval: "caution",
  approved: "brand",
  rejected: "danger",
  paid: "positive",
} as const;

export function OverviewPage() {
  const profile = useProfile();
  // The summary needs tenant-wide read; a department-scoped manager gets a 403
  // and simply does not see the panel, so the request is not made at all.
  const canReadAll = useCan("expense:read:all");

  const summary = useQuery({ ...summaryQuery(), enabled: canReadAll });
  const entitlement = useQuery(entitlementQuery());

  const error = summary.error ?? entitlement.error;
  const loading = (canReadAll && summary.isPending) || entitlement.isPending;
  const totals = summary.data;

  return (
    <div className="flex flex-col gap-6">
      <h1 className="text-lg font-semibold">Overview</h1>

      {entitlement.data?.in_grace_period && (
        <div
          role="status"
          className="rounded-md border border-caution-700/20 bg-caution-50 px-4 py-3 text-sm"
        >
          <p className="font-medium text-caution-700">A payment has not gone through</p>
          <p className="mt-1 text-ink-800">
            Nothing is restricted while the card is retried. Update the payment details to avoid
            dropping to the free plan.
          </p>
        </div>
      )}

      {error && (
        <ErrorNotice
          title="Could not load the overview"
          detail={error.message}
          traceId={error instanceof ApiError ? error.traceId : undefined}
        />
      )}

      <section aria-labelledby="by-status">
        <h2 id="by-status" className="mb-3 text-sm font-medium text-ink-800">
          Claims by status
        </h2>

        {loading ? (
          <Card>
            <SkeletonRows rows={2} columns={5} />
          </Card>
        ) : !totals ? (
          <Card>
            <EmptyState
              title="Not available for your role"
              detail="An organisation-wide total would show what every department spends, so it needs tenant-wide read access."
            />
          </Card>
        ) : totals.by_status.length === 0 ? (
          <Card>
            <EmptyState
              title="No claims yet"
              detail="Once somebody files a claim, the totals appear here."
              action={
                <Link className="text-sm text-brand-600 underline" to="/expenses/new">
                  File the first one
                </Link>
              }
            />
          </Card>
        ) : (
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
            {totals.by_status.map((row) => (
              <Card key={row.status} className="p-4">
                <Badge tone={STATUS_TONE[row.status] ?? "neutral"}>{statusLabel(row.status)}</Badge>
                <p className="mt-3 text-xl font-semibold tabular-nums">{formatMoney(row.total)}</p>
                <p className="text-xs text-ink-600">
                  {row.claim_count} {row.claim_count === 1 ? "claim" : "claims"}
                </p>
              </Card>
            ))}
          </div>
        )}
      </section>

      {totals && totals.by_department.length > 0 && (
        <section aria-labelledby="by-department">
          <h2 id="by-department" className="mb-3 text-sm font-medium text-ink-800">
            Committed spend by department
          </h2>
          <p className="mb-3 text-xs text-ink-600">
            Approved and paid claims only. Claims awaiting a decision are not counted, so this is
            money the organisation has agreed to spend.
          </p>

          <Card>
            <table className="w-full text-sm">
              <caption className="sr-only">Committed spend by department</caption>
              <thead>
                <tr className="border-b border-ink-100 text-left text-xs uppercase tracking-wide text-ink-600">
                  <th scope="col" className="px-4 py-2.5 font-medium">
                    Department
                  </th>
                  <th scope="col" className="px-4 py-2.5 text-right font-medium">
                    Claims
                  </th>
                  <th scope="col" className="px-4 py-2.5 text-right font-medium">
                    Total
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-ink-100">
                {totals.by_department.map((row) => (
                  <tr key={row.department_id ?? "unassigned"}>
                    <td className="px-4 py-3">{row.department_name}</td>
                    <td className="px-4 py-3 text-right tabular-nums">{row.claim_count}</td>
                    {/* tabular-nums so a column of figures lines up on the
                        decimal point rather than wandering by glyph width. */}
                    <td className="px-4 py-3 text-right font-medium tabular-nums">
                      {formatMoney(row.total)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Card>
        </section>
      )}

      {entitlement.data && profile && (
        <p className="text-xs text-ink-600">
          {profile.tenant_name} is on the <strong>{entitlement.data.plan}</strong> plan.
        </p>
      )}
    </div>
  );
}
