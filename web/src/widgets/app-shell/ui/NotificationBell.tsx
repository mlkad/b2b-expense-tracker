import { useInfiniteQuery } from "@tanstack/react-query";
import { Link } from "react-router";

import { pendingExpensesQuery } from "@/entities/expense";
import { useCan } from "@/entities/session";
import { BellIcon } from "@/shared/ui/icons";

/**
 * A dot when something is actually waiting, and nothing when it is not.
 *
 * The queue this counts is the approver's, so the request is only made by
 * somebody who can decide - a bell that permanently shows nothing is worse than
 * no bell, and one that fires a request for every reader who could never act on
 * the answer is worse still.
 *
 * It is a link, not a menu. There is one thing to do about a pending claim and
 * it is on the approvals screen.
 */
export function NotificationBell() {
  const canApprove = useCan("expense:approve");

  const pending = useInfiniteQuery({
    ...pendingExpensesQuery("limit=20"),
    enabled: canApprove,
    // Nothing here needs to be to the second, and a poll on a dashboard left
    // open all day is a request a minute for a number that rarely changes.
    staleTime: 60 * 1000,
  });

  if (!canApprove) return null;

  const waiting = pending.data?.pages[0]?.items.length ?? 0;
  const more = pending.data?.pages[0]?.has_more ?? false;
  const label =
    waiting === 0
      ? "No claims waiting for a decision"
      : `${waiting}${more ? "+" : ""} claim${waiting === 1 ? "" : "s"} waiting for a decision`;

  return (
    <Link
      to="/approvals"
      aria-label={label}
      title={label}
      className="relative grid size-9 place-items-center rounded-lg text-muted transition-colors hover:bg-elevated hover:text-fg"
    >
      <BellIcon className="size-5" />
      {waiting > 0 && (
        <span
          aria-hidden="true"
          className="absolute top-1.5 right-1.5 size-2 rounded-full bg-accent ring-2 ring-canvas"
        />
      )}
    </Link>
  );
}
