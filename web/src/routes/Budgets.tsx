import { useCallback, useState, type FormEvent } from "react";

import { ApiError } from "../api/client";
import type { BudgetConsumption, Department } from "../api/types";
import { Button, Card, EmptyState, ErrorNotice, Field, Select, SkeletonRows, TextInput } from "../components/ui";
import { useSession } from "../auth/context";
import { useResource } from "../hooks/useResource";
import { formatDate } from "../lib/format";
import { formatBasisPoints, formatMoney, parseAmount } from "../lib/money";

export function Budgets() {
  const { api, profile, can } = useSession();
  const [creating, setCreating] = useState(false);

  const fetchConsumption = useCallback(
    () => api.get<{ items: BudgetConsumption[] }>("/budgets/consumption"),
    [api],
  );
  const { data, error, initial, reload } = useResource("consumption", fetchConsumption);

  const envelopes = data?.items ?? [];

  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-lg font-semibold">Budgets</h1>
          <p className="mt-1 text-sm text-ink-600">
            Committed means approved and paid claims. Claims awaiting a decision are not counted, so
            this is money the organisation has agreed to spend.
          </p>
        </div>
        {can("budget:manage") && (
          <Button onClick={() => setCreating((open) => !open)}>
            {creating ? "Cancel" : "New budget"}
          </Button>
        )}
      </div>

      {error && <ErrorNotice title="Could not load the budgets" detail={error.message} traceId={error.traceId} />}

      {creating && (
        <NewBudget
          currency={profile?.default_currency ?? "USD"}
          onCreated={() => {
            setCreating(false);
            reload();
          }}
        />
      )}

      {initial ? (
        <Card><SkeletonRows rows={3} columns={4} /></Card>
      ) : envelopes.length === 0 ? (
        <Card>
          <EmptyState
            title="No budgets set"
            detail="A budget gives a department a ceiling for a period, and raises an alert as it is approached."
          />
        </Card>
      ) : (
        <div className="grid gap-4 lg:grid-cols-2">
          {envelopes.map((envelope) => (
            <Envelope key={envelope.budget_id} envelope={envelope} />
          ))}
        </div>
      )}
    </div>
  );
}

function Envelope({ envelope }: { envelope: BudgetConsumption }) {
  const overspent = envelope.remaining.amount_minor < 0;
  const usage = Math.min(envelope.usage_bps / 100, 100);

  const tone = overspent
    ? "bg-danger-700"
    : envelope.breached
      ? "bg-caution-700"
      : "bg-brand-600";

  return (
    <Card className="p-5">
      <div className="flex items-baseline justify-between gap-3">
        <h2 className="text-sm font-medium">{envelope.department_name ?? "Organisation-wide"}</h2>
        <p className="text-xs text-ink-600">
          {formatDate(envelope.period_start)} – {formatDate(envelope.period_end)}
        </p>
      </div>

      <p className="mt-3 text-xl font-semibold tabular-nums">
        {formatMoney(envelope.consumed)}
        <span className="ml-1 text-sm font-normal text-ink-600">of {formatMoney(envelope.budget)}</span>
      </p>

      {/* A real progress element, so the value is announced rather than being a
          coloured div a screen reader skips. The bar is capped at 100% while
          the label is not: a 120% bar would just look full. */}
      <div
        role="progressbar"
        aria-valuenow={Math.round(envelope.usage_bps / 100)}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-label={`${envelope.department_name ?? "Organisation-wide"} budget used`}
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
          {formatMoney({ ...envelope.remaining, amount_minor: Math.abs(envelope.remaining.amount_minor) })}
        </span>
      </div>

      <p className="mt-3 text-xs text-ink-600">
        {envelope.claim_count} {envelope.claim_count === 1 ? "claim" : "claims"} · alerts at{" "}
        {formatBasisPoints(envelope.alert_threshold_bps)}
      </p>
    </Card>
  );
}

function NewBudget({ currency, onCreated }: { currency: string; onCreated: () => void }) {
  const { api } = useSession();

  const [departmentId, setDepartmentId] = useState("");
  const [start, setStart] = useState("");
  const [end, setEnd] = useState("");
  const [amount, setAmount] = useState("");
  const [threshold, setThreshold] = useState("80");
  const [error, setError] = useState<ApiError | null>(null);
  const [amountError, setAmountError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const fetchDepartments = useCallback(() => api.get<{ items: Department[] }>("/departments"), [api]);
  const { data: departments } = useResource("departments", fetchDepartments);

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    setError(null);
    setAmountError(null);

    const minor = parseAmount(amount, currency);
    if (minor === null || minor <= 0) {
      setAmountError("Enter an amount.");
      return;
    }

    setBusy(true);
    try {
      await api.post("/budgets", {
        department_id: departmentId || null,
        period_start: start,
        period_end: end,
        amount_minor: minor,
        currency,
        // The server stores the threshold in basis points; the form asks for a
        // percentage because that is what a person thinks in.
        alert_threshold_bps: Math.round(Number(threshold) * 100),
      });
      onCreated();
    } catch (err) {
      if (err instanceof ApiError) setError(err);
      else throw err;
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card className="p-5">
      <h2 className="mb-4 text-sm font-medium">New budget</h2>

      {error && (
        <div className="mb-4">
          <ErrorNotice
            title={
              error.isPlanLimit
                ? "Your plan does not include this"
                : "Could not create the budget"
            }
            detail={error.message}
            traceId={error.traceId}
          />
        </div>
      )}

      <form onSubmit={onSubmit} className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3" noValidate>
        <Field label="Department" htmlFor="budget-department" hint="Leave blank for an organisation-wide envelope.">
          <Select id="budget-department" value={departmentId} onChange={(e) => setDepartmentId(e.target.value)}>
            <option value="">Organisation-wide</option>
            {(departments?.items ?? []).map((d) => (
              <option key={d.id} value={d.id}>
                {d.name}
              </option>
            ))}
          </Select>
        </Field>

        <Field label="From" htmlFor="budget-start" error={error?.fieldError("period_start")}>
          <TextInput id="budget-start" type="date" required value={start} onChange={(e) => setStart(e.target.value)} />
        </Field>

        <Field label="To" htmlFor="budget-end" error={error?.fieldError("period_end")}>
          <TextInput id="budget-end" type="date" required value={end} onChange={(e) => setEnd(e.target.value)} />
        </Field>

        <Field label={`Amount (${currency})`} htmlFor="budget-amount" error={amountError ?? error?.fieldError("amount_minor")}>
          <TextInput
            id="budget-amount"
            inputMode="decimal"
            required
            value={amount}
            onChange={(e) => setAmount(e.target.value)}
            invalid={Boolean(amountError)}
          />
        </Field>

        <Field
          label="Alert at (%)"
          htmlFor="budget-threshold"
          hint="Finance is emailed once committed spend passes this."
          error={error?.fieldError("alert_threshold_bps")}
        >
          <TextInput
            id="budget-threshold"
            inputMode="numeric"
            value={threshold}
            onChange={(e) => setThreshold(e.target.value)}
          />
        </Field>

        <div className="flex items-end">
          <Button type="submit" busy={busy}>
            Create
          </Button>
        </div>
      </form>

      <p className="mt-3 text-xs text-ink-600">
        Two budgets cannot cover the same department and overlapping dates — that would make
        &ldquo;how much is left&rdquo; ambiguous, so the database refuses it.
      </p>
    </Card>
  );
}
