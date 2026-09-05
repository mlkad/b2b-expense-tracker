import { Link } from "react-router";

import { StatusBadge, type Expense } from "@/entities/expense";
import { formatDate, formatTimestamp } from "@/shared/lib/format";
import { formatMoney } from "@/shared/lib/money";
import { ArrowUpIcon, MoreIcon } from "@/shared/ui/icons";
import { Monogram } from "@/shared/ui/Monogram";
import { TableHead } from "@/shared/ui/kit";

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
    // The wrapper scrolls, not the page. A table that pushes the body wider
    // makes every other column on the screen scroll sideways with it.
    <div className="overflow-x-auto">
      <table className="w-full min-w-[36rem] text-sm">
        <caption className="sr-only">
          {queue ? "Claims awaiting a decision" : "Expense claims"}
        </caption>
        <TableHead>
          <th scope="col" className="py-3 pr-4 pl-5 font-medium">
            <span className="inline-flex items-center gap-1">
              {queue ? "Submitted" : "Date"}
              {/* The queue is ordered oldest first and cannot be reordered, so
                  the arrow states the order rather than offering to change it -
                  it is not a button. */}
              {queue ? <ArrowUpIcon className="size-3" /> : null}
            </span>
          </th>
          <th scope="col" className="px-4 py-3 font-medium">
            Merchant
          </th>
          <th scope="col" className="px-4 py-3 font-medium">
            {queue ? "Spent on" : "Category"}
          </th>
          {!queue && (
            <th scope="col" className="px-4 py-3 font-medium">
              Status
            </th>
          )}
          <th scope="col" className="px-4 py-3 text-right font-medium">
            Amount
          </th>
          <th scope="col" className="w-12 py-3 pr-4 pl-2">
            <span className="sr-only">Actions</span>
          </th>
        </TableHead>
        <tbody className="divide-y divide-line-soft">
          {claims.map((claim) => (
            <tr key={claim.id} className="group transition-colors hover:bg-surface/70">
              <td className="py-3.5 pr-4 pl-5 whitespace-nowrap text-muted">
                {queue ? formatTimestamp(claim.submitted_at) : formatDate(claim.spent_at)}
              </td>
              <td className="px-4 py-3.5">
                <span className="flex items-center gap-2.5">
                  <Monogram name={claim.merchant} />
                  {/* The whole row is not a link: a row-level click target
                      swallows text selection, and a real anchor is what makes
                      middle-click and "open in new tab" work. */}
                  <Link
                    to={`/expenses/${claim.id}`}
                    className="font-medium text-fg hover:text-accent hover:underline"
                  >
                    {claim.merchant}
                  </Link>
                  {claim.revision > 1 && (
                    <span className="rounded bg-tone-caution px-1.5 py-0.5 text-[11px] text-tone-caution-fg">
                      rev {claim.revision}
                    </span>
                  )}
                </span>
              </td>
              <td className="px-4 py-3.5 text-muted">
                {queue ? formatDate(claim.spent_at) : claim.category}
              </td>
              {!queue && (
                <td className="px-4 py-3.5">
                  <StatusBadge status={claim.status} />
                </td>
              )}
              <td className="px-4 py-3.5 text-right font-medium tabular-nums">
                {formatMoney(claim.amount)}
              </td>
              <td className="py-3.5 pr-4 pl-2 text-right">
                {/* Opens the claim, where the actions the server allows are
                    listed. It is not a menu of its own: a second copy of the
                    transition rules, one row high, is the surest way to offer a
                    button that 403s. */}
                <Link
                  to={`/expenses/${claim.id}`}
                  aria-label={`Open the ${claim.merchant} claim`}
                  className="inline-grid size-7 place-items-center rounded-md text-faint transition-colors hover:bg-elevated hover:text-fg"
                >
                  <MoreIcon className="size-4" />
                </Link>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
