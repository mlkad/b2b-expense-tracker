import { useMemo, useState, type FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";

import { departmentsQuery } from "@/entities/department";
import { useFormErrors } from "@/shared/lib/form";
import { Button, Card, ErrorNotice, Field, Select, TextInput } from "@/shared/ui/kit";

import { useCreateBudget } from "../model/mutations";
import { budgetDraftSchema } from "../model/schema";

export function NewBudgetForm({
  currency,
  onCreated,
}: {
  currency: string;
  onCreated: () => void;
}) {
  const schema = useMemo(() => budgetDraftSchema(currency), [currency]);
  const form = useFormErrors(schema);
  const create = useCreateBudget(currency);

  const [departmentId, setDepartmentId] = useState("");
  const [start, setStart] = useState("");
  const [end, setEnd] = useState("");
  const [amount, setAmount] = useState("");
  const [threshold, setThreshold] = useState("80");

  const departments = useQuery({ ...departmentsQuery(), throwOnError: false });

  async function onSubmit(event: FormEvent) {
    event.preventDefault();

    const draft = form.validate({
      department_id: departmentId,
      period_start: start,
      period_end: end,
      amount,
      alert_threshold: threshold,
    });
    if (!draft) return;

    try {
      await create.mutateAsync(draft);
      onCreated();
    } catch (err) {
      form.capture(err);
    }
  }

  return (
    <Card className="p-5">
      <h2 className="mb-4 text-sm font-medium">New budget</h2>

      {form.message && (
        <div className="mb-4">
          <ErrorNotice
            title={
              form.failure?.isPlanLimit
                ? "Your plan does not include this"
                : "Could not create the budget"
            }
            detail={form.message}
            traceId={form.failure?.traceId}
          />
        </div>
      )}

      <form onSubmit={onSubmit} className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3" noValidate>
        <Field
          label="Department"
          htmlFor="budget-department"
          hint="Leave blank for an organisation-wide envelope."
        >
          <Select
            id="budget-department"
            value={departmentId}
            onChange={(e) => setDepartmentId(e.target.value)}
          >
            <option value="">Organisation-wide</option>
            {(departments.data ?? []).map((d) => (
              <option key={d.id} value={d.id}>
                {d.name}
              </option>
            ))}
          </Select>
        </Field>

        <Field label="From" htmlFor="budget-start" error={form.fields.period_start}>
          <TextInput
            id="budget-start"
            type="date"
            required
            value={start}
            onChange={(e) => setStart(e.target.value)}
            invalid={Boolean(form.fields.period_start)}
          />
        </Field>

        <Field label="To" htmlFor="budget-end" error={form.fields.period_end}>
          <TextInput
            id="budget-end"
            type="date"
            required
            value={end}
            onChange={(e) => setEnd(e.target.value)}
            invalid={Boolean(form.fields.period_end)}
          />
        </Field>

        <Field
          label={`Amount (${currency})`}
          htmlFor="budget-amount"
          error={form.fields.amount ?? form.fields.amount_minor}
        >
          <TextInput
            id="budget-amount"
            inputMode="decimal"
            required
            value={amount}
            onChange={(e) => setAmount(e.target.value)}
            invalid={Boolean(form.fields.amount ?? form.fields.amount_minor)}
          />
        </Field>

        <Field
          label="Alert at (%)"
          htmlFor="budget-threshold"
          hint="Finance is emailed once committed spend passes this."
          error={form.fields.alert_threshold ?? form.fields.alert_threshold_bps}
        >
          <TextInput
            id="budget-threshold"
            inputMode="numeric"
            value={threshold}
            onChange={(e) => setThreshold(e.target.value)}
            invalid={Boolean(form.fields.alert_threshold)}
          />
        </Field>

        <div className="flex items-end">
          <Button type="submit" busy={create.isPending}>
            Create
          </Button>
        </div>
      </form>

      <p className="mt-3 text-xs text-muted">
        Two budgets cannot cover the same department and overlapping dates — that would make
        &ldquo;how much is left&rdquo; ambiguous, so the database refuses it.
      </p>
    </Card>
  );
}
