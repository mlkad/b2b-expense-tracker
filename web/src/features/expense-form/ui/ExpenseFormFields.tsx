import { useMemo, useState, type FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router";

import { departmentsQuery } from "@/entities/department";
import { EXPENSE_CATEGORIES, expenseQuery, type ExpenseCategory } from "@/entities/expense";
import { useProfile } from "@/entities/session";
import { useFormErrors } from "@/shared/lib/form";
import { todayISO } from "@/shared/lib/format";
import { exponentFor } from "@/shared/lib/money";
import { Button, Card, ErrorNotice, Field, Select, TextInput } from "@/shared/ui/kit";

import { useSaveExpense } from "../model/mutations";
import { expenseDraftSchema } from "../model/schema";

export function ExpenseFormFields({ id }: { id?: string }) {
  const editing = Boolean(id);
  const profile = useProfile();
  const navigate = useNavigate();

  const currency = profile?.default_currency ?? "USD";
  const exponent = exponentFor(currency);

  const schema = useMemo(() => expenseDraftSchema(currency), [currency]);
  const form = useFormErrors(schema);
  const save = useSaveExpense(id, currency);

  const [merchant, setMerchant] = useState("");
  const [amount, setAmount] = useState("");
  const [category, setCategory] = useState<ExpenseCategory>("other");
  const [spentAt, setSpentAt] = useState(todayISO());
  const [departmentId, setDepartmentId] = useState("");
  const [description, setDescription] = useState("");

  const departments = useQuery({ ...departmentsQuery(), throwOnError: false });
  const existing = useQuery({ ...expenseQuery(id ?? ""), enabled: editing });

  // The form is filled from the claim as soon as it arrives, during render
  // rather than in an effect - an effect would commit a frame of empty inputs
  // over data that is already in hand, which reads as the form clearing itself.
  //
  // Keyed on the claim's revision as well as its id, so a claim edited in
  // another tab and refetched reloads the inputs, while a keystroke here does
  // not get overwritten by the copy in the cache.
  const claim = existing.data;
  const revision = claim ? `${claim.id}@${claim.revision}:${claim.updated_at}` : null;
  const [filled, setFilled] = useState<string | null>(null);

  if (claim && revision !== null && filled !== revision) {
    setFilled(revision);
    setMerchant(claim.merchant);
    // Rendered from the integer rather than from the server's formatted string,
    // so what appears in the input is what round-trips back.
    const places = exponentFor(claim.amount.currency);
    setAmount((claim.amount.amount_minor / 10 ** places).toFixed(places));
    setCategory(claim.category);
    setSpentAt(claim.spent_at.slice(0, 10));
    setDepartmentId(claim.department_id ?? "");
    setDescription(claim.description ?? "");
  }

  async function onSubmit(event: FormEvent) {
    event.preventDefault();

    const draft = form.validate({
      merchant,
      amount,
      category,
      spent_at: spentAt,
      department_id: departmentId,
      description,
    });
    if (!draft) return;

    try {
      const saved = await save.mutateAsync(draft);
      void navigate(`/expenses/${saved.id}`);
    } catch (err) {
      form.capture(err);
    }
  }

  if (editing && existing.isPending) {
    return <Card className="p-6 text-sm text-ink-600">Loading…</Card>;
  }

  // The server names this field after what it stores; the form after what is
  // typed into it. Both have to reach the same input.
  const amountError = form.fields.amount ?? form.fields.amount_minor;

  return (
    <div className="mx-auto flex max-w-2xl flex-col gap-4">
      <Link
        to={editing ? `/expenses/${id}` : "/expenses"}
        className="text-sm text-brand-600 hover:underline"
      >
        ← Back
      </Link>
      <h1 className="text-lg font-semibold">{editing ? "Edit claim" : "New claim"}</h1>

      {form.message && (
        <ErrorNotice
          title={
            form.failure?.isConflict
              ? "This claim can no longer be edited"
              : "Could not save the claim"
          }
          detail={form.message}
          traceId={form.failure?.traceId}
        />
      )}

      <Card className="p-6">
        <form onSubmit={onSubmit} className="grid gap-4 sm:grid-cols-2" noValidate>
          <div className="sm:col-span-2">
            <Field label="Merchant" htmlFor="merchant" error={form.fields.merchant}>
              <TextInput
                id="merchant"
                required
                maxLength={200}
                value={merchant}
                onChange={(e) => setMerchant(e.target.value)}
                invalid={Boolean(form.fields.merchant)}
              />
            </Field>
          </div>

          <Field
            label={`Amount (${currency})`}
            htmlFor="amount"
            hint={
              exponent === 0
                ? `${currency} has no minor unit, so whole numbers only.`
                : `Up to ${exponent} decimal places.`
            }
            error={amountError}
          >
            <TextInput
              id="amount"
              // inputMode rather than type="number": a number input silently
              // discards what it cannot parse as the user types, and its
              // spinner is meaningless for money.
              inputMode="decimal"
              required
              value={amount}
              onChange={(e) => setAmount(e.target.value)}
              invalid={Boolean(amountError)}
            />
          </Field>

          <Field label="Spent on" htmlFor="spent_at" error={form.fields.spent_at}>
            <TextInput
              id="spent_at"
              type="date"
              required
              // The picker should not offer a date the server would refuse.
              max={todayISO()}
              value={spentAt}
              onChange={(e) => setSpentAt(e.target.value)}
              invalid={Boolean(form.fields.spent_at)}
            />
          </Field>

          <Field label="Category" htmlFor="category" error={form.fields.category}>
            <Select
              id="category"
              value={category}
              onChange={(e) => setCategory(e.target.value as ExpenseCategory)}
            >
              {EXPENSE_CATEGORIES.map((c) => (
                <option key={c} value={c}>
                  {c}
                </option>
              ))}
            </Select>
          </Field>

          <Field label="Department" htmlFor="department_id" error={form.fields.department_id}>
            <Select
              id="department_id"
              value={departmentId}
              onChange={(e) => setDepartmentId(e.target.value)}
            >
              <option value="">Unassigned</option>
              {(departments.data ?? []).map((d) => (
                <option key={d.id} value={d.id}>
                  {d.name}
                </option>
              ))}
            </Select>
          </Field>

          <div className="sm:col-span-2">
            <Field label="Description" htmlFor="description" error={form.fields.description}>
              <textarea
                id="description"
                rows={3}
                maxLength={4000}
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                className="rounded-md border border-ink-200 bg-white px-3 py-2 text-sm outline-none"
              />
            </Field>
          </div>

          <div className="flex gap-2 sm:col-span-2">
            <Button type="submit" busy={save.isPending}>
              {editing ? "Save changes" : "Create draft"}
            </Button>
            <Link
              to={editing ? `/expenses/${id}` : "/expenses"}
              className="inline-flex items-center rounded-md px-3.5 py-2 text-sm font-medium text-ink-600 hover:bg-ink-100"
            >
              Cancel
            </Link>
          </div>
        </form>
      </Card>

      <p className="text-xs text-ink-600">
        A new claim is created as a draft. Nothing is sent for approval until you submit it, and it
        can be edited freely until then.
      </p>
    </div>
  );
}
