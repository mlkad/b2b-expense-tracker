import type { BudgetConsumption } from "@/entities/budget";
import { formatDate } from "@/shared/lib/format";
import { formatBasisPoints, formatMoney } from "@/shared/lib/money";
import { Card } from "@/shared/ui/kit";

export function BudgetEnvelope({ envelope }: { envelope: BudgetConsumption }) {
  const overspent = envelope.remaining.amount_minor < 0;
  const usage = Math.min(envelope.usage_bps / 100, 100);

  const tone = overspent ? "bg-danger-700" : envelope.breached ? "bg-caution-700" : "bg-brand-600";
  const name = envelope.department_name ?? "Organisation-wide";

  return (
    <Card className="p-5">
      <div className="flex items-baseline justify-between gap-3">
        <h2 className="text-sm font-medium">{name}</h2>
        <p className="text-xs text-ink-600">
          {formatDate(envelope.period_start)} – {formatDate(envelope.period_end)}
        </p>
      </div>

      <p className="mt-3 text-xl font-semibold tabular-nums">
        {formatMoney(envelope.consumed)}
        <span className="ml-1 text-sm font-normal text-ink-600">
          of {formatMoney(envelope.budget)}
        </span>
      </p>

      {/* A real progress element, so the value is announced rather than being a
          coloured div a screen reader skips. The bar is capped at 100% while
          the label is not: a 120% bar would just look full. */}
      <div
        role="progressbar"
        aria-valuenow={Math.round(envelope.usage_bps / 100)}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-label={`${name} budget used`}
        className="mt-3 h-2 w-full overflow-hidden rounded-full bg-ink-100"
      >
        <div className={`h-full rounded-full ${tone}`} style={{ width: `${usage}%` }} />
      </div>

      <div className="mt-2 flex items-center justify-between text-xs">
        <span className={overspent ? "font-medium text-danger-700" : "text-ink-600"}>
          {formatBasisPoints(envelope.usage_bps)} used
          {envelope.breached && !overspent && " · past the alert threshold"}
          {overspent && " · overspent"}
        </span>
        <span className="text-ink-600 tabular-nums">
          {overspent ? "Over by " : "Remaining "}
          {formatMoney({
            ...envelope.remaining,
            amount_minor: Math.abs(envelope.remaining.amount_minor),
          })}
        </span>
      </div>

      <p className="mt-3 text-xs text-ink-600">
        {envelope.claim_count} {envelope.claim_count === 1 ? "claim" : "claims"} · alerts at{" "}
        {formatBasisPoints(envelope.alert_threshold_bps)}
      </p>
    </Card>
  );
}
