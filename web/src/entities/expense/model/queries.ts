import { infiniteQueryOptions, keepPreviousData, queryOptions } from "@tanstack/react-query";

import { api, decode, listOf, pageOf } from "@/shared/api";

import { attachmentSchema, expenseEventSchema, expenseSchema } from "./schema";

/**
 * Every key for this entity, in one place.
 *
 * The shape matters: `["expenses"]` prefixes `["expenses", "list", filters]`,
 * so invalidating the root after a decision refreshes the list, the detail and
 * the approver queue together. Keys assembled by hand at each call site drift,
 * and the symptom is a screen still showing a claim that was approved a moment
 * ago on another one.
 */
export const expenseKeys = {
  all: ["expenses"] as const,
  lists: () => [...expenseKeys.all, "list"] as const,
  list: (query: string) => [...expenseKeys.lists(), query] as const,
  pending: (query: string) => [...expenseKeys.all, "pending", query] as const,
  detail: (id: string) => [...expenseKeys.all, "detail", id] as const,
  history: (id: string) => [...expenseKeys.all, "history", id] as const,
  attachments: (id: string) => [...expenseKeys.all, "attachments", id] as const,
};

const expensePage = pageOf(expenseSchema);
const expenseEvents = listOf(expenseEventSchema);
const attachments = listOf(attachmentSchema);

/**
 * A page of claims, walked by cursor.
 *
 * The server has no page numbers to offer: a cursor names the last row of the
 * previous page, which is what keeps the cost the same at page one and page one
 * thousand, and what stops a row inserted mid-walk from shifting the window.
 * useInfiniteQuery keeps the pages it has already fetched, so going back is a
 * read from the cache rather than a second request.
 */
export function expenseListQuery(search: string) {
  return infiniteQueryOptions({
    queryKey: expenseKeys.list(search),
    queryFn: async ({ pageParam }) => {
      const query = withCursor(search, pageParam);
      return decode(expensePage, await api.get(`/expenses${query}`), "GET /expenses");
    },
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => (last.has_more ? last.next_cursor : undefined),
    // The rows already on screen stay until there are new ones to replace
    // them. Blanking the table on every filter change makes the page jump and
    // costs the reader their place.
    placeholderData: keepPreviousData,
  });
}

export function pendingExpensesQuery(search: string) {
  return infiniteQueryOptions({
    queryKey: expenseKeys.pending(search),
    queryFn: async ({ pageParam }) => {
      const query = withCursor(search, pageParam);
      return decode(
        expensePage,
        await api.get(`/expenses/pending${query}`),
        "GET /expenses/pending",
      );
    },
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => (last.has_more ? last.next_cursor : undefined),
    placeholderData: keepPreviousData,
  });
}

export function expenseQuery(id: string) {
  return queryOptions({
    queryKey: expenseKeys.detail(id),
    queryFn: async () => decode(expenseSchema, await api.get(`/expenses/${id}`), "GET /expenses/:id"),
  });
}

export function expenseHistoryQuery(id: string) {
  return queryOptions({
    queryKey: expenseKeys.history(id),
    queryFn: async () =>
      decode(
        expenseEvents,
        await api.get(`/expenses/${id}/history`),
        "GET /expenses/:id/history",
      ).items,
  });
}

export function attachmentsQuery(id: string) {
  return queryOptions({
    queryKey: expenseKeys.attachments(id),
    queryFn: async () =>
      decode(
        attachments,
        await api.get(`/expenses/${id}/attachments`),
        "GET /expenses/:id/attachments",
      ).items,
  });
}

function withCursor(search: string, cursor: string | undefined): string {
  const params = new URLSearchParams(search);
  if (cursor) params.set("cursor", cursor);
  const query = params.toString();
  return query ? `?${query}` : "";
}
