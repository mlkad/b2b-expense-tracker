import { useCallback } from "react";
import { parseAsString, useQueryStates } from "nuqs";

/**
 * The filters live in the address bar.
 *
 * Which means a filtered view can be sent to a colleague, kept as a bookmark,
 * and survives a reload - and the back button steps through the filters
 * someone actually applied. Holding them in component state gives up all of
 * that for nothing, and it is the state most worth sharing on this screen: a
 * question about spend is nearly always a question about one slice of it.
 *
 * `clearOnDefault` keeps an empty filter out of the URL entirely, so the plain
 * /expenses link stays plain rather than growing six empty parameters.
 */
export const expenseFilterParsers = {
  q: parseAsString.withDefault(""),
  status: parseAsString.withDefault(""),
  category: parseAsString.withDefault(""),
  department_id: parseAsString.withDefault(""),
  from: parseAsString.withDefault(""),
  to: parseAsString.withDefault(""),
};

export type ExpenseFilters = {
  [K in keyof typeof expenseFilterParsers]: string;
};

export const EMPTY_FILTERS: ExpenseFilters = {
  q: "",
  status: "",
  category: "",
  department_id: "",
  from: "",
  to: "",
};

export function useExpenseFilters() {
  const [filters, setFilters] = useQueryStates(expenseFilterParsers, {
    // Replace rather than push: applying a filter is refining a view, not
    // moving to a new place, and pushing would make back step through six
    // half-built queries before leaving the screen.
    history: "replace",
    clearOnDefault: true,
  });

  const clear = useCallback(() => setFilters(null), [setFilters]);

  return { filters, setFilters, clear, isFiltered: isFiltered(filters) };
}

export function isFiltered(filters: ExpenseFilters): boolean {
  return Object.values(filters).some((value) => value !== "");
}

/** The query string for the list endpoint. */
export function listSearch(filters: ExpenseFilters, limit = 20): string {
  const params = new URLSearchParams({ limit: String(limit) });
  for (const [key, value] of Object.entries(filters)) {
    if (value) params.set(key, value);
  }
  return params.toString();
}

/** The query string for an export, which the server signs. */
export function exportSearch(filters: ExpenseFilters, format: string): string {
  const params = new URLSearchParams({ format });
  for (const [key, value] of Object.entries(filters)) {
    // The free-text search is not an export filter on the server, so including
    // it would ask for a signature over a query the export route will not
    // accept.
    if (value && key !== "q") params.set(key, value);
  }
  return params.toString();
}
