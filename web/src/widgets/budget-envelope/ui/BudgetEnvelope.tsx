import type { BudgetConsumption } from "@/entities/budget";
import { formatDate } from "@/shared/lib/format";
import { formatBasisPoints, formatMoney } from "@/shared/lib/money";
import { Card } from "@/shared/ui/kit";
import { Monogram } from "@/shared/ui/Monogram";

export function BudgetEnvelope({ envelope }: { envelope: BudgetConsumption }) {
  const overspent = envelope.remaining.amount_minor < 0;
  const usage = Math.min(envelope.usage_bps / 100, 100);
  const name = envelope.department_name ?? "Organisation-wide";

  // The bar's colour is the one place the state is not spelled out, so it is
  // also said in words underneath. Purple while there is room, amber past the
  // threshold the budget itself sets, red once it is gone.
  const fill = overspent
    ? "bg-tone-danger-fg"
    : envelope.breached
      ? "bg-tone-caution-fg"
      : "bg-accent-strong";

  return (
    <Card className="p-5">
      <div className="flex items-start gap-3">
        <Monogram name={name} className="size-11 rounded-xl text-base" />
        <div className="min-w-0 flex-1">
          <h2 className="truncate text-[15px] font-semibold">{name}</h2>
          <p className="mt-0.5 text-xs text-faint">
            {formatDate(envelope.period_start)} – {formatDate(envelope.period_end)}
          </p>
        </div>
      </div>

      <p className="mt-5 text-[26px] leading-none font-semibold tabular-nums">
        {formatMoney(envelope.consumed)}
        <span className="ml-1.5 text-sm font-normal text-faint">
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
        className="mt-4 h-2 w-full overflow-hidden rounded-full bg-elevated"
      >
        <div className={`h-full rounded-full ${fill}`} style={{ width: `${usage}%` }} />
      </div>

      <div className="mt-2.5 flex items-center justify-between text-xs">
        <span className={overspent ? "font-medium text-tone-danger-fg" : "text-muted"}>
          {formatBasisPoints(envelope.usage_bps)} used
          {envelope.breached && !overspent && " · past the alert threshold"}
          {overspent && " · overspent"}
        </span>
        <span className="text-muted tabular-nums">
          {overspent ? "Over by " : "Remaining "}
          {formatMoney({
            ...envelope.remaining,
            amount_minor: Math.abs(envelope.remaining.amount_minor),
          })}
        </span>
      </div>

      <p className="mt-3 border-t border-line-soft pt-3 text-xs text-faint">
        {envelope.claim_count} {envelope.claim_count === 1 ? "claim" : "claims"} · alerts at{" "}
        {formatBasisPoints(envelope.alert_threshold_bps)}
      </p>
    </Card>
  );
}
