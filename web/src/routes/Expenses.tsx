import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "react-router";

import type { Department, Expense, ExpenseCategory, ExpenseStatus, Page } from "../api/types";
import { Button, Card, EmptyState, ErrorNotice, Field, Select, SkeletonRows, TextInput } from "../components/ui";
import { useResource } from "../hooks/useResource";
import { Pagination } from "../components/Pagination";
import { StatusBadge } from "../components/StatusBadge";
import { useSession } from "../auth/context";
import { useCursorPages } from "../hooks/useCursorPages";
import { formatDate } from "../lib/format";
import { formatMoney } from "../lib/money";

const STATUSES: ExpenseStatus[] = ["draft", "pending_approval", "approved", "rejected", "paid"];

const CATEGORIES: ExpenseCategory[] = [
  "travel", "meals", "accommodation", "software", "hardware",
  "marketing", "training", "office", "contractor", "other",
];

interface Filters {
  status: string;
  category: string;
  department_id: string;
  from: string;
  to: string;
  q: string;
}

const EMPTY: Filters = { status: "", category: "", department_id: "", from: "", to: "", q: "" };

export function Expenses() {
  const { api, can } = useSession();

  const [filters, setFilters] = useState<Filters>(EMPTY);
  const [applied, setApplied] = useState<Filters>(EMPTY);
  const [departments, setDepartments] = useState<Department[]>([]);
  const pages = useCursorPages();
  const { reset } = pages;

  const query = useMemo(() => {
    const params = new URLSearchParams({ limit: "20" });
    for (const [key, value] of Object.entries(applied)) {
      if (value) params.set(key, value);
    }
    if (pages.cursor) params.set("cursor", pages.cursor);
    return params.toString();
  }, [applied, pages.cursor]);

  const fetchPage = useCallback((key: string) => api.get<Page<Expense>>(`/expenses?${key}`), [api]);
  const { data: page, error, loading, initial } = useResource(query, fetchPage);

  useEffect(() => {
    let cancelled = false;
    api
      .get<{ items: Department[] }>("/departments")
      .then((d) => !cancelled && setDepartments(d.items))
      // A failed department list narrows the filters and nothing else, so it
      // must not replace the page with an error.
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [api]);

  const applyFilters = useCallback(() => {
    // The cursors already collected point into the previous result set, so
    // they are discarded rather than carried across a filter change - reusing
    // one would resume a walk through rows that are no longer in the list.
    reset();
    setApplied(filters);
  }, [filters, reset]);

  const clearFilters = useCallback(() => {
    reset();
    setFilters(EMPTY);
    setApplied(EMPTY);
  }, [reset]);

  const exportUrl = (format: string) => {
    const params = new URLSearchParams({ format });
    for (const [key, value] of Object.entries(applied)) {
      if (value && key !== "q") params.set(key, value);
    }
    return api.url(`/reports/expenses/export?${params.toString()}`);
  };

  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-center justify-between">
        <h1 className="text-lg font-semibold">Expenses</h1>
        <div className="flex gap-2">
          {can("report:export") && <ExportMenu urlFor={exportUrl} />}
          <Link
            to="/expenses/new"
            className="inline-flex items-center rounded-md bg-brand-600 px-3.5 py-2 text-sm font-medium text-white hover:bg-brand-700"
          >
            New claim
          </Link>
        </div>
      </div>

      <Card className="p-4">
        <form
          className="grid gap-3 sm:grid-cols-2 lg:grid-cols-6"
          onSubmit={(e) => {
            e.preventDefault();
            applyFilters();
          }}
        >
          <Field label="Search" htmlFor="q">
            <TextInput
              id="q"
              value={filters.q}
              placeholder="Merchant or description"
              onChange={(e) => setFilters({ ...filters, q: e.target.value })}
            />
          </Field>

          <Field label="Status" htmlFor="status">
            <Select id="status" value={filters.status} onChange={(e) => setFilters({ ...filters, status: e.target.value })}>
              <option value="">Any</option>
              {STATUSES.map((s) => (
                <option key={s} value={s}>
                  {s.replace(/_/g, " ")}
                </option>
              ))}
            </Select>
          </Field>

          <Field label="Category" htmlFor="category">
            <Select id="category" value={filters.category} onChange={(e) => setFilters({ ...filters, category: e.target.value })}>
              <option value="">Any</option>
              {CATEGORIES.map((c) => (
                <option key={c} value={c}>
                  {c}
                </option>
              ))}
            </Select>
          </Field>

          <Field label="Department" htmlFor="department">
            <Select
              id="department"
              value={filters.department_id}
              onChange={(e) => setFilters({ ...filters, department_id: e.target.value })}
            >
              <option value="">Any</option>
              {departments.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.name}
                </option>
              ))}
            </Select>
          </Field>

          <Field label="Spent from" htmlFor="from">
            <TextInput id="from" type="date" value={filters.from} onChange={(e) => setFilters({ ...filters, from: e.target.value })} />
          </Field>

          <Field label="Spent to" htmlFor="to">
            <TextInput id="to" type="date" value={filters.to} onChange={(e) => setFilters({ ...filters, to: e.target.value })} />
          </Field>

          <div className="flex items-end gap-2 sm:col-span-2 lg:col-span-6">
            <Button type="submit">Apply</Button>
            <Button type="button" variant="ghost" onClick={clearFilters}>
              Clear
            </Button>
          </div>
        </form>
      </Card>

      {error && <ErrorNotice title="Could not load the claims" detail={error.message} traceId={error.traceId} />}

      <Card>
        {initial ? (
          <SkeletonRows rows={6} columns={5} />
        ) : !page || page.items.length === 0 ? (
          <EmptyState
            title="No claims match"
            detail={
              applied === EMPTY
                ? "Nothing has been filed yet."
                : "Try widening the date range or clearing a filter."
            }
          />
        ) : (
          <>
            <table className="w-full text-sm">
              <caption className="sr-only">Expense claims</caption>
              <thead>
                <tr className="border-b border-ink-100 text-left text-xs uppercase tracking-wide text-ink-600">
                  <th scope="col" className="px-4 py-2.5 font-medium">Date</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">Merchant</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">Category</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">Status</th>
                  <th scope="col" className="px-4 py-2.5 text-right font-medium">Amount</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-ink-100">
                {page.items.map((claim) => (
                  <tr key={claim.id} className="hover:bg-ink-50">
                    <td className="px-4 py-3 whitespace-nowrap text-ink-600">{formatDate(claim.spent_at)}</td>
                    <td className="px-4 py-3">
                      {/* The whole row is not a link: a row-level click target
                          swallows text selection, and a real anchor is what
                          makes middle-click and "open in new tab" work. */}
                      <Link to={`/expenses/${claim.id}`} className="font-medium text-brand-600 hover:underline">
                        {claim.merchant}
                      </Link>
                      {claim.revision > 1 && (
                        <span className="ml-2 text-xs text-ink-600">rev {claim.revision}</span>
                      )}
                    </td>
                    <td className="px-4 py-3 text-ink-600">{claim.category}</td>
                    <td className="px-4 py-3"><StatusBadge status={claim.status} /></td>
                    <td className="px-4 py-3 text-right font-medium tabular-nums">{formatMoney(claim.amount)}</td>
                  </tr>
                ))}
              </tbody>
            </table>

            <Pagination
              page={pages.pageNumber}
              hasPrevious={pages.hasPrevious}
              hasNext={page.has_more}
              onPrevious={pages.previous}
              onNext={() => page.next_cursor && pages.next(page.next_cursor)}
              busy={loading}
            />
          </>
        )}
      </Card>
    </div>
  );
}

/**
 * Exports are plain links, not fetch calls.
 *
 * A streamed report can be tens of megabytes; fetching it into a Blob to
 * trigger a download would hold the whole thing in the tab's memory, which is
 * the cost the server went to some trouble to avoid. Letting the browser
 * navigate hands it to the download manager instead, which streams to disk.
 *
 * The consequence is that the request carries no Authorization header - a
 * navigation cannot set one - so these rely on the session cookie. That is a
 * real limitation and it is why the links are marked as needing a signed-in
 * session rather than being shareable.
 */
function ExportMenu({ urlFor }: { urlFor: (format: string) => string }) {
  return (
    <div className="flex items-center gap-1 rounded-md border border-ink-200 bg-white px-2 py-1">
      <span className="px-1 text-xs text-ink-600">Export</span>
      {["csv", "xlsx", "pdf"].map((format) => (
        <a
          key={format}
          href={urlFor(format)}
          className="rounded px-2 py-1 text-xs font-medium uppercase text-brand-600 hover:bg-ink-50"
          // download is advisory: the server's Content-Disposition names the
          // file, and this only tells the browser not to try rendering it.
          download
        >
          {format}
        </a>
      ))}
    </div>
  );
}
