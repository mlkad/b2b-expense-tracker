import { useQuery } from "@tanstack/react-query";

import { vendorSubscriptionsQuery } from "@/entities/org";
import { useProfile } from "@/entities/session";
import { ApiError } from "@/shared/api";
import { formatDate } from "@/shared/lib/format";
import { formatMinor, formatMoney } from "@/shared/lib/money";
import { Card, EmptyState, ErrorNotice, SkeletonRows, TableHead } from "@/shared/ui/kit";
import { Monogram } from "@/shared/ui/Monogram";

export function SubscriptionsPanel() {
  const profile = useProfile();
  const subscriptions = useQuery(vendorSubscriptionsQuery());

  const data = subscriptions.data;
  const currency = data?.currency || profile?.default_currency || "USD";
  const error = subscriptions.error instanceof ApiError ? subscriptions.error : null;

  return (
    <div className="flex flex-col gap-4">
      <p className="text-sm text-muted">
        The recurring software your organisation pays for. Due charges become draft claims
        automatically, so nothing renews unnoticed.
      </p>

      {subscriptions.error && (
        <ErrorNotice
          title={
            error?.status === 403
              ? "Your plan does not include this"
              : "Could not load the subscriptions"
          }
          detail={subscriptions.error.message}
          traceId={error?.traceId}
        />
      )}

      {data && data.items.length > 0 && (
        <Card className="p-5">
          <p className="text-[11px] font-medium tracking-[0.08em] text-faint uppercase">
            Annualised, active only
          </p>
          <p className="mt-1.5 text-2xl font-semibold tabular-nums">
            {formatMinor(data.annualised_total_minor, currency)}
          </p>
        </Card>
      )}

      <Card>
        {subscriptions.isPending ? (
          <SkeletonRows rows={3} columns={4} />
        ) : (data?.items.length ?? 0) === 0 ? (
          <EmptyState title="Nothing tracked yet" />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[40rem] text-sm">
              <caption className="sr-only">Tracked vendor subscriptions</caption>
              <TableHead>
                <th scope="col" className="py-3 pr-4 pl-5 font-medium">Vendor</th>
                <th scope="col" className="px-4 py-3 font-medium">Cadence</th>
                <th scope="col" className="px-4 py-3 font-medium">Next charge</th>
                <th scope="col" className="px-4 py-3 text-right font-medium">Amount</th>
                <th scope="col" className="px-4 py-3 pr-5 text-right font-medium">Per year</th>
              </TableHead>
              <tbody className="divide-y divide-line-soft">
                {(data?.items ?? []).map((sub) => (
                  <tr key={sub.id} className="transition-colors hover:bg-surface/70">
                    <td className="py-3.5 pr-4 pl-5">
                      <span className="flex items-center gap-2.5">
                        <Monogram name={sub.vendor} />
                        <span>
                          <span className="font-medium">{sub.vendor}</span>
                          {sub.plan_name && (
                            <span className="ml-2 text-xs text-faint">{sub.plan_name}</span>
                          )}
                        </span>
                      </span>
                    </td>
                    <td className="px-4 py-3.5 text-muted capitalize">{sub.cadence}</td>
                    <td className="px-4 py-3.5 text-muted">{formatDate(sub.next_charge_on)}</td>
                    <td className="px-4 py-3.5 text-right tabular-nums">
                      {formatMoney(sub.amount)}
                    </td>
                    <td className="px-4 py-3.5 pr-5 text-right font-medium tabular-nums">
                      {formatMinor(sub.annualised_minor, currency)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </div>
  );
}
