import { useEffect, useState } from "react";
import { Link } from "react-router";

import { ApiError } from "../api/client";
import type { Entitlement, Summary } from "../api/types";
import { Badge, Card, EmptyState, ErrorNotice, SkeletonRows } from "../components/ui";
import { useSession } from "../auth/context";
import { formatMoney } from "../lib/money";
import { statusLabel } from "../lib/format";

const STATUS_TONE = {
  draft: "neutral",
  pending_approval: "caution",
  approved: "brand",
  rejected: "danger",
  paid: "positive",
} as const;

export function Overview() {
  const { api, profile, can } = useSession();

  const [summary, setSummary] = useState<Summary | null>(null);
  const [entitlement, setEntitlement] = useState<Entitlement | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;

    (async () => {
      try {
        // The summary needs tenant-wide read; a department-scoped manager gets
        // a 403 and simply does not see this panel.
        const [s, e] = await Promise.all([
          can("expense:read:all") ? api.get<Summary>("/summary") : Promise.resolve(null),
          api.get<Entitlement>("/billing/entitlement"),
        ]);
        if (cancelled) return;
        setSummary(s);
        setEntitlement(e);
      } catch (err) {
        if (!cancelled && err instanceof ApiError) setError(err);
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [api, can]);

  return (
    <div className="flex flex-col gap-6">
      <h1 className="text-lg font-semibold">Overview</h1>

      {entitlement?.in_grace_period && (
        <div role="status" className="rounded-md border border-caution-700/20 bg-caution-50 px-4 py-3 text-sm">
          <p className="font-medium text-caution-700">A payment has not gone through</p>
          <p className="mt-1 text-ink-800">
            Nothing is restricted while the card is retried. Update the payment details to avoid
            dropping to the free plan.
          </p>
        </div>
      )}

      {error && <ErrorNotice title="Could not load the overview" detail={error.message} traceId={error.traceId} />}

      <section aria-labelledby="by-status">
        <h2 id="by-status" className="mb-3 text-sm font-medium text-ink-800">
          Claims by status
        </h2>

        {loading ? (
          <Card><SkeletonRows rows={2} columns={5} /></Card>
        ) : !summary ? (
          <Card>
            <EmptyState
              title="Not available for your role"
              detail="An organisation-wide total would show what every department spends, so it needs tenant-wide read access."
            />
          </Card>
        ) : summary.by_status.length === 0 ? (
          <Card>
            <EmptyState
              title="No claims yet"
              detail="Once somebody files a claim, the totals appear here."
              action={<Link className="text-sm text-brand-600 underline" to="/expenses/new">File the first one</Link>}
            />
          </Card>
        ) : (
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
            {summary.by_status.map((row) => (
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

      {summary && summary.by_department.length > 0 && (
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
                  <th scope="col" className="px-4 py-2.5 font-medium">Department</th>
                  <th scope="col" className="px-4 py-2.5 text-right font-medium">Claims</th>
                  <th scope="col" className="px-4 py-2.5 text-right font-medium">Total</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-ink-100">
                {summary.by_department.map((row) => (
                  <tr key={row.department_id ?? "unassigned"}>
                    <td className="px-4 py-3">{row.department_name}</td>
                    <td className="px-4 py-3 text-right tabular-nums">{row.claim_count}</td>
                    {/* tabular-nums so a column of figures lines up on the
                        decimal point rather than wandering by glyph width. */}
                    <td className="px-4 py-3 text-right font-medium tabular-nums">{formatMoney(row.total)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Card>
        </section>
      )}

      {entitlement && profile && (
        <p className="text-xs text-ink-600">
          {profile.tenant_name} is on the <strong>{entitlement.plan}</strong> plan.
        </p>
      )}
    </div>
  );
}
