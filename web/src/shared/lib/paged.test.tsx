import type { ReactNode } from "react";
import {
  QueryClient,
  QueryClientProvider,
  infiniteQueryOptions,
  keepPreviousData,
} from "@tanstack/react-query";
import { renderHook, waitFor, act } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import type { Page } from "@/shared/api";

import { usePagedQuery } from "./paged";

/** Three pages of one row each, walked by cursor. */
function server() {
  const pages: Record<string, Page<{ id: string }>> = {
    start: { items: [{ id: "a" }], has_more: true, next_cursor: "c1" },
    c1: { items: [{ id: "b" }], has_more: true, next_cursor: "c2" },
    c2: { items: [{ id: "c" }], has_more: false },
  };

  const fetched: string[] = [];
  const fetchPage = vi.fn(async ({ pageParam }: { pageParam: string | undefined }) => {
    const key = pageParam ?? "start";
    fetched.push(key);
    return pages[key];
  });

  return { fetchPage, fetched };
}

function wrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
}

function optionsFor(key: string, fetchPage: ReturnType<typeof server>["fetchPage"]) {
  return infiniteQueryOptions({
    queryKey: ["claims", key],
    queryFn: fetchPage,
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last: Page<{ id: string }>) =>
      last.has_more ? last.next_cursor : undefined,
    placeholderData: keepPreviousData,
  });
}

describe("usePagedQuery", () => {
  it("walks forward one page at a time", async () => {
    const { fetchPage } = server();
    const { result } = renderHook(() => usePagedQuery(optionsFor("all", fetchPage)), {
      wrapper: wrapper(),
    });

    await waitFor(() => expect(result.current.items).toEqual([{ id: "a" }]));
    expect(result.current.hasPrevious).toBe(false);
    expect(result.current.hasNext).toBe(true);

    act(() => result.current.next());
    await waitFor(() => expect(result.current.items).toEqual([{ id: "b" }]));
    expect(result.current.pageNumber).toBe(2);

    act(() => result.current.next());
    await waitFor(() => expect(result.current.items).toEqual([{ id: "c" }]));
    // The last page says so, and the button that would ask for a fourth is
    // disabled rather than producing an empty table.
    expect(result.current.hasNext).toBe(false);
  });

  /**
   * The reason this is an infinite query rather than a stack of cursors.
   *
   * A cursor names the last row of the previous page, so the previous page's
   * cursor cannot be computed from it. Keeping the pages already fetched makes
   * going back free, and - more importantly - makes it correct.
   */
  it("goes back without asking the server again", async () => {
    const { fetchPage, fetched } = server();
    const { result } = renderHook(() => usePagedQuery(optionsFor("all", fetchPage)), {
      wrapper: wrapper(),
    });

    await waitFor(() => expect(result.current.items).toEqual([{ id: "a" }]));
    act(() => result.current.next());
    await waitFor(() => expect(result.current.items).toEqual([{ id: "b" }]));

    const before = fetched.length;
    act(() => result.current.previous());

    await waitFor(() => expect(result.current.items).toEqual([{ id: "a" }]));
    expect(fetched).toHaveLength(before);
    expect(result.current.pageNumber).toBe(1);
  });

  it("returns to the first page when the query changes", async () => {
    const { fetchPage } = server();
    const { result, rerender } = renderHook(
      ({ key }: { key: string }) => usePagedQuery(optionsFor(key, fetchPage)),
      { wrapper: wrapper(), initialProps: { key: "all" } },
    );

    await waitFor(() => expect(result.current.items).toEqual([{ id: "a" }]));
    act(() => result.current.next());
    await waitFor(() => expect(result.current.pageNumber).toBe(2));

    // A filter change makes a different query, and the cursors already walked
    // point into a result set that no longer exists.
    rerender({ key: "travel" });

    // The page counter resets at once, and the rows already on screen stay
    // until the new ones arrive - blanking the table mid-change costs the
    // reader their place. Both are checked, because keeping the rows without
    // resetting the counter would leave "page 2" over the first page.
    expect(result.current.pageNumber).toBe(1);
    expect(result.current.items).toEqual([{ id: "a" }]);

    await waitFor(() => expect(result.current.busy).toBe(false));
    expect(result.current.items).toEqual([{ id: "a" }]);
  });
});
