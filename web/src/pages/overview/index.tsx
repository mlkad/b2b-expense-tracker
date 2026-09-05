import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router";

import { entitlementQuery } from "@/entities/billing";
import { summaryQuery } from "@/entities/expense";
import { useCan, useProfile } from "@/entities/session";
import { ApiError } from "@/shared/api";
import { statusLabel } from "@/shared/lib/format";
import { formatMoney } from "@/shared/lib/money";
import { Badge, Card, EmptyState, ErrorNotice, SkeletonRows, TableHead } from "@/shared/ui/kit";
import { PageHeader } from "@/shared/ui/PageHeader";

const STATUS_TONE = {
  draft: "neutral",
  pending_approval: "caution",
  approved: "info",
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
    <div>
      <PageHeader
        title="Overview"
        description="Where the month stands: what has been filed, what has been agreed, and where it is going."
      />

      <div className="flex flex-col gap-6">

      {entitlement.data?.in_grace_period && (
        <div
          role="status"
          className="rounded-md border border-tone-caution-fg/25 bg-tone-caution px-4 py-3 text-sm"
        >
          <p className="font-medium text-tone-caution-fg">A payment has not gone through</p>
          <p className="mt-1 text-fg">
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
        <h2 id="by-status" className="mb-3 text-sm font-medium text-fg">
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
                <Link className="text-sm text-accent underline" to="/expenses/new">
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
                <p className="mt-3.5 text-xl font-semibold tabular-nums">
                  {formatMoney(row.total)}
                </p>
                <p className="mt-0.5 text-xs text-faint">
                  {row.claim_count} {row.claim_count === 1 ? "claim" : "claims"}
                </p>
              </Card>
            ))}
          </div>
        )}
      </section>

      {totals && totals.by_department.length > 0 && (
        <section aria-labelledby="by-department">
          <h2 id="by-department" className="mb-3 text-sm font-medium text-fg">
            Committed spend by department
          </h2>
          <p className="mb-3 text-xs text-muted">
            Approved and paid claims only. Claims awaiting a decision are not counted, so this is
            money the organisation has agreed to spend.
          </p>

          <Card>
            <div className="overflow-x-auto">
              <table className="w-full min-w-[30rem] text-[13px]">
                <caption className="sr-only">Committed spend by department</caption>
                <TableHead>
                  <th scope="col" className="py-2 pr-3 pl-4 font-medium">
                    Department
                  </th>
                  <th scope="col" className="px-3 py-2 text-right font-medium">
                    Claims
                  </th>
                  <th scope="col" className="px-3 py-2 pr-4 text-right font-medium">
                    Total
                  </th>
                </TableHead>
                <tbody className="divide-y divide-line-soft">
                  {totals.by_department.map((row) => (
                    <tr
                      key={row.department_id ?? "unassigned"}
                      className="transition-colors hover:bg-surface/70"
                    >
                      <td className="py-1.5 pr-3 pl-4">{row.department_name}</td>
                      <td className="px-3 py-1.5 text-right tabular-nums text-muted">
                        {row.claim_count}
                      </td>
                      {/* tabular-nums so a column of figures lines up on the
                          decimal point rather than wandering by glyph width. */}
                      <td className="px-3 py-1.5 pr-4 text-right font-medium tabular-nums">
                        {formatMoney(row.total)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </Card>
        </section>
      )}

      {entitlement.data && profile && (
        <p className="text-xs text-faint">
          {profile.tenant_name} is on the <strong className="text-muted">{entitlement.data.plan}</strong> plan.
        </p>
      )}
      </div>
    </div>
  );
}
