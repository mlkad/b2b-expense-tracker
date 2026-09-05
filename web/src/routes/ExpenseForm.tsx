import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router";

import { ApiError } from "../api/client";
import type { Department, Expense, ExpenseCategory } from "../api/types";
import { Button, Card, ErrorNotice, Field, Select, TextInput } from "../components/ui";
import { useSession } from "../auth/context";
import { todayISO } from "../lib/format";
import { exponentFor, parseAmount } from "../lib/money";

const CATEGORIES: ExpenseCategory[] = [
  "travel", "meals", "accommodation", "software", "hardware",
  "marketing", "training", "office", "contractor", "other",
];

export function ExpenseForm() {
  const { id } = useParams();
  const editing = Boolean(id);

  const { api, profile } = useSession();
  const navigate = useNavigate();

  const [merchant, setMerchant] = useState("");
  const [amount, setAmount] = useState("");
  const [category, setCategory] = useState<ExpenseCategory>("other");
  const [spentAt, setSpentAt] = useState(todayISO());
  const [departmentId, setDepartmentId] = useState("");
  const [description, setDescription] = useState("");

  const [departments, setDepartments] = useState<Department[]>([]);
  const [error, setError] = useState<ApiError | null>(null);
  const [amountError, setAmountError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(editing);

  const currency = profile?.default_currency ?? "USD";

  useEffect(() => {
    let cancelled = false;
    api
      .get<{ items: Department[] }>("/departments")
      .then((d) => !cancelled && setDepartments(d.items))
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [api]);

  useEffect(() => {
    if (!editing) return;
    let cancelled = false;

    (async () => {
      try {
        const claim = await api.get<Expense>(`/expenses/${id}`);
        if (cancelled) return;

        setMerchant(claim.merchant);
        // Rendered from the integer rather than from the server's formatted
        // string, so what appears in the input is what round-trips back.
        setAmount((claim.amount.amount_minor / 10 ** exponentFor(claim.amount.currency)).toFixed(
          exponentFor(claim.amount.currency),
        ));
        setCategory(claim.category);
        setSpentAt(claim.spent_at.slice(0, 10));
        setDepartmentId(claim.department_id ?? "");
        setDescription(claim.description ?? "");
      } catch (err) {
        if (!cancelled && err instanceof ApiError) setError(err);
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [api, editing, id]);

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    setError(null);
    setAmountError(null);

    // Parsed before anything is sent, so a typo is answered instantly rather
    // than after a round trip - and the integer that goes to the server is the
    // one the arithmetic was done on.
    const minor = parseAmount(amount, currency);
    if (minor === null || minor <= 0) {
      setAmountError(
        `Enter an amount with at most ${exponentFor(currency)} decimal place${exponentFor(currency) === 1 ? "" : "s"}.`,
      );
      return;
    }

    setBusy(true);
    const body = {
      department_id: departmentId || null,
      category,
      amount_minor: minor,
      currency,
      merchant,
      description: description.trim() || null,
      spent_at: spentAt,
    };

    try {
      if (editing) {
        await api.patch<Expense>(`/expenses/${id}`, body);
        void navigate(`/expenses/${id}`);
      } else {
        const created = await api.post<Expense>("/expenses", body);
        void navigate(`/expenses/${created.id}`);
      }
    } catch (err) {
      if (err instanceof ApiError) setError(err);
      else throw err;
    } finally {
      setBusy(false);
    }
  }

  if (loading) return <Card className="p-6 text-sm text-ink-600">Loading…</Card>;

  return (
    <div className="mx-auto flex max-w-2xl flex-col gap-4">
      <Link to={editing ? `/expenses/${id}` : "/expenses"} className="text-sm text-brand-600 hover:underline">
        ← Back
      </Link>
      <h1 className="text-lg font-semibold">{editing ? "Edit claim" : "New claim"}</h1>

      {error && (
        <ErrorNotice
          title={error.isConflict ? "This claim can no longer be edited" : "Could not save the claim"}
          detail={error.message}
          traceId={error.traceId}
        />
      )}

      <Card className="p-6">
        <form onSubmit={onSubmit} className="grid gap-4 sm:grid-cols-2" noValidate>
          <div className="sm:col-span-2">
            <Field label="Merchant" htmlFor="merchant" error={error?.fieldError("merchant")}>
              <TextInput
                id="merchant"
                required
                maxLength={200}
                value={merchant}
                onChange={(e) => setMerchant(e.target.value)}
                invalid={Boolean(error?.fieldError("merchant"))}
              />
            </Field>
          </div>

          <Field
            label={`Amount (${currency})`}
            htmlFor="amount"
            hint={
              exponentFor(currency) === 0
                ? `${currency} has no minor unit, so whole numbers only.`
                : `Up to ${exponentFor(currency)} decimal places.`
            }
            error={amountError ?? error?.fieldError("amount_minor")}
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
              invalid={Boolean(amountError ?? error?.fieldError("amount_minor"))}
            />
          </Field>

          <Field label="Spent on" htmlFor="spent_at" error={error?.fieldError("spent_at")}>
            <TextInput
              id="spent_at"
              type="date"
              required
              // A claim for money not yet spent is a typo; the server refuses
              // it and this stops the picker offering it.
              max={todayISO()}
              value={spentAt}
              onChange={(e) => setSpentAt(e.target.value)}
              invalid={Boolean(error?.fieldError("spent_at"))}
            />
          </Field>

          <Field label="Category" htmlFor="category" error={error?.fieldError("category")}>
            <Select id="category" value={category} onChange={(e) => setCategory(e.target.value as ExpenseCategory)}>
              {CATEGORIES.map((c) => (
                <option key={c} value={c}>
                  {c}
                </option>
              ))}
            </Select>
          </Field>

          <Field label="Department" htmlFor="department_id" error={error?.fieldError("department_id")}>
            <Select id="department_id" value={departmentId} onChange={(e) => setDepartmentId(e.target.value)}>
              <option value="">Unassigned</option>
              {departments.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.name}
                </option>
              ))}
            </Select>
          </Field>

          <div className="sm:col-span-2">
            <Field label="Description" htmlFor="description" error={error?.fieldError("description")}>
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
            <Button type="submit" busy={busy}>
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
        A new claim is created as a draft. Nothing is sent for approval until you submit it, and it can
        be edited freely until then.
      </p>
    </div>
  );
}
