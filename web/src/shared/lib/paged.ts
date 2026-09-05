import { useMemo, useState } from "react";
import {
  useInfiniteQuery,
  type InfiniteData,
  type UseInfiniteQueryOptions,
} from "@tanstack/react-query";

import type { Page } from "@/shared/api";

/**
 * Walks a keyset-paginated endpoint one page at a time.
 *
 * The server has no page numbers to offer: a cursor names the last row of the
 * previous page, which is what keeps the cost the same at page one and page one
 * thousand, and what stops a row inserted mid-walk from shifting the window.
 *
 * Going back therefore cannot be computed - there is no way to derive the
 * previous page's cursor from the current one, and pretending otherwise is how
 * a "previous" button ends up skipping rows. What makes it work here is that
 * the infinite query keeps every page it has fetched, so stepping back is a
 * read from the cache and costs no request at all.
 */
export function usePagedQuery<T>(
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  options: UseInfiniteQueryOptions<Page<T>, Error, InfiniteData<Page<T>>, any, string | undefined>,
) {
  const query = useInfiniteQuery(options);
  const [index, setIndex] = useState(0);

  // A filter change makes a different query, and page four of the old result
  // set means nothing in the new one.
  //
  // Adjusted during render rather than in an effect. An effect would commit the
  // stale page first and correct it on a second pass, so the reader sees one
  // frame of the previous result set under the new filters.
  const key = JSON.stringify(options.queryKey);
  const [renderedKey, setRenderedKey] = useState(key);
  if (renderedKey !== key) {
    setRenderedKey(key);
    setIndex(0);
  }

  const pages = useMemo(() => query.data?.pages ?? [], [query.data]);
  const current = pages[index];

  const hasNext = index + 1 < pages.length || (current?.has_more ?? false);

  return {
    items: current?.items ?? [],
    error: query.error,
    /** True on the very first load, when there is nothing to show yet. */
    initial: query.isPending,
    /** True while the rows on screen are not the ones being asked for. */
    busy: query.isFetching,
    pageNumber: index + 1,
    hasPrevious: index > 0,
    hasNext,
    previous: () => setIndex((n) => Math.max(0, n - 1)),
    next: () => {
      if (index + 1 < pages.length) {
        setIndex(index + 1);
        return;
      }
      if (query.hasNextPage) void query.fetchNextPage().then(() => setIndex((n) => n + 1));
    },
    refetch: query.refetch,
  };
}
