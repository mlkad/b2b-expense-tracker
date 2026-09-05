/**
 * Date rendering.
 *
 * Everything the API returns is UTC, and spent_at is a calendar date rather
 * than an instant - a receipt dated the 1st is dated the 1st wherever the
 * reader is. Rendering it in the browser's zone would show the previous day to
 * anybody west of Greenwich, which is the kind of bug that gets reported as
 * "the export is wrong".
 */
export function formatDate(iso: string, locale?: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "—";

  return new Intl.DateTimeFormat(locale, {
    day: "numeric",
    month: "short",
    year: "numeric",
    timeZone: "UTC",
  }).format(date);
}

/**
 * An instant, rendered in the reader's own zone.
 *
 * The opposite choice from formatDate, and deliberately: "approved at 14:02" is
 * a moment in time, and a reader wants to know when that was for them.
 */
export function formatTimestamp(iso: string | null | undefined, locale?: string): string {
  if (!iso) return "—";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "—";

  return new Intl.DateTimeFormat(locale, {
    day: "numeric",
    month: "short",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  }).format(date);
}

/** Today, as the API's date format expects it, in UTC. */
export function todayISO(): string {
  return new Date().toISOString().slice(0, 10);
}

const STATUS_LABELS: Record<string, string> = {
  draft: "Draft",
  pending_approval: "Awaiting approval",
  approved: "Approved",
  rejected: "Rejected",
  paid: "Paid",
};

export function statusLabel(status: string): string {
  return STATUS_LABELS[status] ?? sentenceCase(status);
}

const ACTION_LABELS: Record<string, string> = {
  submit: "Submit for approval",
  approve: "Approve",
  reject: "Reject",
  withdraw: "Withdraw",
  revise: "Revise",
  pay: "Mark as paid",
};

export function actionLabel(action: string): string {
  return ACTION_LABELS[action] ?? sentenceCase(action);
}

export function sentenceCase(value: string): string {
  const spaced = value.replace(/[_-]+/g, " ").trim();
  return spaced.charAt(0).toUpperCase() + spaced.slice(1);
}
