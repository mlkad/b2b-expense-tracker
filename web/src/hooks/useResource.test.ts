import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { ApiError } from "../api/client";
import { useResource } from "./useResource";

describe("useResource", () => {
  it("reports the first load and then the data", async () => {
    const fetcher = vi.fn(async (key: string) => `data for ${key}`);
    const { result } = renderHook(() => useResource("one", fetcher));

    expect(result.current.initial).toBe(true);
    expect(result.current.loading).toBe(true);

    await waitFor(() => expect(result.current.data).toBe("data for one"));
    expect(result.current.loading).toBe(false);
    expect(result.current.initial).toBe(false);
  });

  /**
   * Loading is derived from whether the data matches the key being asked for,
   * rather than stored. That is what removes the extra render a
   * `setLoading(true)` at the top of the effect would schedule on every query
   * change.
   */
  it("keeps the previous data on screen while the next query loads", async () => {
    const fetcher = vi.fn(async (key: string) => `data for ${key}`);
    const { result, rerender } = renderHook(({ key }) => useResource(key, fetcher), {
      initialProps: { key: "one" },
    });

    await waitFor(() => expect(result.current.data).toBe("data for one"));

    rerender({ key: "two" });
    // Blanking the table on every filter change makes the page jump and costs
    // the reader their place.
    expect(result.current.data).toBe("data for one");
    expect(result.current.loading).toBe(true);

    await waitFor(() => expect(result.current.data).toBe("data for two"));
    expect(result.current.loading).toBe(false);
  });

  it("surfaces an ApiError without clearing the key", async () => {
    const fetcher = vi.fn(async () => {
      throw new ApiError(404, "not found");
    });
    const { result } = renderHook(() => useResource("missing", fetcher));

    await waitFor(() => expect(result.current.error).toBeInstanceOf(ApiError));
    expect(result.current.error?.status).toBe(404);
    expect(result.current.data).toBeNull();
    // The request finished, so nothing is still loading - the screen shows the
    // error rather than a skeleton that never resolves.
    expect(result.current.loading).toBe(false);
  });

  it("refetches on reload", async () => {
    let calls = 0;
    const fetcher = vi.fn(async () => {
      calls += 1;
      return calls;
    });

    const { result } = renderHook(() => useResource("same", fetcher));
    await waitFor(() => expect(result.current.data).toBe(1));

    act(() => result.current.reload());
    await waitFor(() => expect(result.current.data).toBe(2));
  });

  it("ignores a response that arrives after the query moved on", async () => {
    // The slow first request must not overwrite the fast second one, or a
    // filter change would flash back to the previous results.
    const fetcher = vi.fn(async (key: string) => {
      await new Promise((resolve) => setTimeout(resolve, key === "slow" ? 50 : 0));
      return key;
    });

    const { result, rerender } = renderHook(({ key }) => useResource(key, fetcher), {
      initialProps: { key: "slow" },
    });

    rerender({ key: "fast" });
    await waitFor(() => expect(result.current.data).toBe("fast"));

    await new Promise((resolve) => setTimeout(resolve, 80));
    expect(result.current.data).toBe("fast");
  });
});
