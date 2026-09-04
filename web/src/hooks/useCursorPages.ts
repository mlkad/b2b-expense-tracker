import { useCallback, useState } from "react";

/**
 * Walks a keyset-paginated endpoint.
 *
 * The server has no page numbers to offer: a cursor names the last row of the
 * previous page, which is what makes the cost the same at page one and page one
 * thousand, and what stops a row inserted mid-walk from shifting the window.
 *
 * Going back therefore means remembering where you have been. A stack of the
 * cursors already used is the whole mechanism - there is no way to compute the
 * previous page's cursor from the current one, and pretending otherwise is how
 * a "previous" button ends up skipping rows.
 */
export function useCursorPages() {
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [history, setHistory] = useState<Array<string | undefined>>([]);

  const next = useCallback((nextCursor: string) => {
    setHistory((visited) => [...visited, cursor]);
    setCursor(nextCursor);
  }, [cursor]);

  const previous = useCallback(() => {
    setHistory((visited) => {
      if (visited.length === 0) return visited;
      setCursor(visited[visited.length - 1]);
      return visited.slice(0, -1);
    });
  }, []);

  /** Called when a filter changes: the old cursors point into a different set. */
  const reset = useCallback(() => {
    setCursor(undefined);
    setHistory([]);
  }, []);

  return {
    cursor,
    pageNumber: history.length + 1,
    hasPrevious: history.length > 0,
    next,
    previous,
    reset,
  };
}
