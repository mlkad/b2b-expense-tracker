import { Badge, type Tone } from "@/shared/ui/kit";
import { statusLabel } from "@/shared/lib/format";
import type { ExpenseStatus } from "../model/schema";

/**
 * Colour carries meaning here, so it is never the only thing that does: every
 * badge also spells the status out. Roughly one man in twelve cannot tell the
 * red from the green, and a table that distinguishes "rejected" from "approved"
 * by hue alone is unreadable to them.
 */
const TONES: Record<ExpenseStatus, Tone> = {
  draft: "neutral",
  pending_approval: "caution",
  approved: "brand",
  rejected: "danger",
  paid: "positive",
};

export function StatusBadge({ status }: { status: ExpenseStatus }) {
  return <Badge tone={TONES[status] ?? "neutral"}>{statusLabel(status)}</Badge>;
}
