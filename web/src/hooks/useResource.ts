import { useCallback, useEffect, useState } from "react";

import { ApiError } from "../api/client";

interface Loaded<T> {
  /** The query this data was fetched for, so staleness is comparable. */
  key: string;
  data: T | null;
  error: ApiError | null;
}

export interface Resource<T> {
  data: T | null;
  error: ApiError | null;
  /** True while the data on screen does not match the query being asked for. */
  loading: boolean;
  /** True on the very first load, when there is nothing to show yet. */
  initial: boolean;
  reload: () => void;
}

/**
 * Fetches a value that depends on a query string.
 *
 * Loading is *derived* rather than stored: the state carries the key it was
 * fetched for, and anything else is by definition stale. Tracking it with a
 * separate `setLoading(true)` at the top of the effect schedules an extra
 * render on every mount and every query change, for a boolean that can be
 * computed from what is already there.
 *
 * Keeping the previous data while the next request is in flight is deliberate.
 * Blanking the table on every filter change makes the page jump and costs the
 * reader their place; the rows they were looking at stay until there are new
 * ones to replace them.
 */
export function useResource<T>(key: string, fetcher: (key: string) => Promise<T>): Resource<T> {
  const [loaded, setLoaded] = useState<Loaded<T>>({ key: "", data: null, error: null });
  const [reloads, setReloads] = useState(0);

  useEffect(() => {
    let cancelled = false;

    fetcher(key)
      .then((data) => {
        if (!cancelled) setLoaded({ key, data, error: null });
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        if (err instanceof ApiError) setLoaded({ key, data: null, error: err });
        // Anything that is not an ApiError is a bug rather than a response -
        // rethrowing puts it in front of the developer instead of rendering an
        // empty table that looks like "no results".
        else throw err;
      });

    return () => {
      cancelled = true;
    };
    // reloads is in the dependency list precisely so that bumping it re-runs
    // the effect; it is the whole mechanism behind reload().
  }, [key, fetcher, reloads]);

  const reload = useCallback(() => setReloads((n) => n + 1), []);

  return {
    data: loaded.data,
    error: loaded.error,
    loading: loaded.key !== key,
    initial: loaded.key === "",
    reload,
  };
}
