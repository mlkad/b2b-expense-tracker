import { Link } from "react-router";

import { StatusBadge, type Expense } from "@/entities/expense";
import { formatDate, formatTimestamp } from "@/shared/lib/format";
import { formatMoney } from "@/shared/lib/money";

/**
 * One table, two readings of the same rows.
 *
 * The list answers "what have I filed", so it leads with the date of the spend
 * and shows where each claim has got to. The approver's queue answers "what is
 * waiting on me", so it leads with how long the claim has been sitting there
 * and drops the status column, which would read "pending" on every row.
 */
export function ExpenseTable({
  claims,
  variant = "list",
}: {
  claims: Expense[];
  variant?: "list" | "queue";
}) {
  const queue = variant === "queue";

  return (
    <table className="w-full text-sm">
      <caption className="sr-only">{queue ? "Claims awaiting a decision" : "Expense claims"}</caption>
      <thead>
        <tr className="border-b border-ink-100 text-left text-xs uppercase tracking-wide text-ink-600">
          <th scope="col" className="px-4 py-2.5 font-medium">
            {queue ? "Submitted" : "Date"}
          </th>
          <th scope="col" className="px-4 py-2.5 font-medium">
            Merchant
          </th>
          <th scope="col" className="px-4 py-2.5 font-medium">
            {queue ? "Spent on" : "Category"}
          </th>
          {!queue && (
            <th scope="col" className="px-4 py-2.5 font-medium">
              Status
            </th>
          )}
          <th scope="col" className="px-4 py-2.5 text-right font-medium">
            Amount
          </th>
        </tr>
      </thead>
      <tbody className="divide-y divide-ink-100">
        {claims.map((claim) => (
          <tr key={claim.id} className="hover:bg-ink-50">
            <td className="px-4 py-3 whitespace-nowrap text-ink-600">
              {queue ? formatTimestamp(claim.submitted_at) : formatDate(claim.spent_at)}
            </td>
            <td className="px-4 py-3">
              {/* The whole row is not a link: a row-level click target
                  swallows text selection, and a real anchor is what makes
                  middle-click and "open in new tab" work. */}
              <Link
                to={`/expenses/${claim.id}`}
                className="font-medium text-brand-600 hover:underline"
              >
                {claim.merchant}
              </Link>
              {claim.revision > 1 &&
                (queue ? (
                  // An approver looking at revision 2 needs to know there was a
                  // decision on revision 1, or they review it as if it were new.
                  <span className="ml-2 rounded bg-caution-50 px-1.5 py-0.5 text-xs text-caution-700">
                    revision {claim.revision}
                  </span>
                ) : (
                  <span className="ml-2 text-xs text-ink-600">rev {claim.revision}</span>
                ))}
            </td>
            <td className="px-4 py-3 text-ink-600">
              {queue ? formatDate(claim.spent_at) : claim.category}
            </td>
            {!queue && (
              <td className="px-4 py-3">
                <StatusBadge status={claim.status} />
              </td>
            )}
            <td className="px-4 py-3 text-right font-medium tabular-nums">
              {formatMoney(claim.amount)}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
