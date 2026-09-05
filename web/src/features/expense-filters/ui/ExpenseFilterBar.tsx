import { useState, type FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";

import { departmentsQuery } from "@/entities/department";
import { EXPENSE_CATEGORIES, EXPENSE_STATUSES } from "@/entities/expense";
import { Button, Card, Field, Select, TextInput } from "@/shared/ui/kit";
import { SearchIcon } from "@/shared/ui/icons";

import { useExpenseFilters, type ExpenseFilters } from "../model/use-expense-filters";

/**
 * Two copies of the filters, deliberately.
 *
 * `draft` is what the inputs show; the URL holds what has been applied. Writing
 * every keystroke to the address bar would put a request behind each letter of
 * a merchant name and fill the history with them. Apply is the moment the
 * question is finished being asked.
 */
export function ExpenseFilterBar() {
  const { filters, setFilters, clear } = useExpenseFilters();
  const [draft, setDraft] = useState<ExpenseFilters>(filters);

  // Someone arriving on a shared link, or stepping back, changes the applied
  // filters without touching an input - the form has to follow. Adjusted during
  // render rather than in an effect, so the inputs never show one frame of the
  // filters the reader has just navigated away from.
  const applied = JSON.stringify(filters);
  const [renderedFilters, setRenderedFilters] = useState(applied);
  if (renderedFilters !== applied) {
    setRenderedFilters(applied);
    setDraft(filters);
  }

  // A failed department list narrows the filters and nothing else, so it must
  // not replace the page with an error.
  const departments = useQuery({ ...departmentsQuery(), throwOnError: false });

  function onSubmit(event: FormEvent) {
    event.preventDefault();
    void setFilters(draft);
  }

  const set = (patch: Partial<ExpenseFilters>) => setDraft((d) => ({ ...d, ...patch }));

  return (
    <Card className="p-3">
      <form className="flex flex-col gap-2.5" onSubmit={onSubmit}>
        {/* The free-text box spans the width and the structured filters sit
            under it, because they are different questions: one is "find this
            thing", the others are "narrow to this slice". */}
        <div className="relative">
          <SearchIcon className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-faint" />
          <label htmlFor="q" className="sr-only">
            Search claims by merchant or description
          </label>
          <TextInput
            id="q"
            value={draft.q}
            placeholder="Search merchant or description..."
            onChange={(e) => set({ q: e.target.value })}
            className="rounded-md bg-elevated py-1.5 pl-9"
          />
        </div>

        <div className="grid gap-x-3 gap-y-2 sm:grid-cols-2 lg:grid-cols-5">
        <Field label="Status" htmlFor="status">
          <Select id="status" value={draft.status} onChange={(e) => set({ status: e.target.value })}>
            <option value="">Any</option>
            {EXPENSE_STATUSES.map((s) => (
              <option key={s} value={s}>
                {s.replace(/_/g, " ")}
              </option>
            ))}
          </Select>
        </Field>

        <Field label="Category" htmlFor="category">
          <Select
            id="category"
            value={draft.category}
            onChange={(e) => set({ category: e.target.value })}
          >
            <option value="">Any</option>
            {EXPENSE_CATEGORIES.map((c) => (
              <option key={c} value={c}>
                {c}
              </option>
            ))}
          </Select>
        </Field>

        <Field label="Department" htmlFor="department">
          <Select
            id="department"
            value={draft.department_id}
            onChange={(e) => set({ department_id: e.target.value })}
          >
            <option value="">Any</option>
            {(departments.data ?? []).map((d) => (
              <option key={d.id} value={d.id}>
                {d.name}
              </option>
            ))}
          </Select>
        </Field>

        <Field label="Spent from" htmlFor="from">
          <TextInput
            id="from"
            type="date"
            value={draft.from}
            onChange={(e) => set({ from: e.target.value })}
          />
        </Field>

        <Field label="Spent to" htmlFor="to">
          <TextInput
            id="to"
            type="date"
            value={draft.to}
            onChange={(e) => set({ to: e.target.value })}
          />
        </Field>

        </div>

        <div className="flex items-center gap-2">
          <Button type="submit">Apply</Button>
          <Button type="button" variant="ghost" onClick={() => void clear()}>
            Clear
          </Button>
        </div>
      </form>
    </Card>
  );
}
