import { act, renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { useCursorPages } from "./useCursorPages";

describe("useCursorPages", () => {
  it("starts on the first page with no cursor", () => {
    const { result } = renderHook(() => useCursorPages());

    expect(result.current.cursor).toBeUndefined();
    expect(result.current.pageNumber).toBe(1);
    expect(result.current.hasPrevious).toBe(false);
  });

  it("walks forward and back through the cursors it has seen", () => {
    const { result } = renderHook(() => useCursorPages());

    act(() => result.current.next("cursor-a"));
    expect(result.current.cursor).toBe("cursor-a");
    expect(result.current.pageNumber).toBe(2);

    act(() => result.current.next("cursor-b"));
    expect(result.current.cursor).toBe("cursor-b");
    expect(result.current.pageNumber).toBe(3);

    // Going back is a stack pop, not arithmetic: there is no way to compute
    // the previous page's cursor from the current one, and pretending
    // otherwise is how a "previous" button skips rows.
    act(() => result.current.previous());
    expect(result.current.cursor).toBe("cursor-a");
    expect(result.current.pageNumber).toBe(2);

    act(() => result.current.previous());
    expect(result.current.cursor).toBeUndefined();
    expect(result.current.pageNumber).toBe(1);
    expect(result.current.hasPrevious).toBe(false);
  });

  it("does nothing when asked to go back from the first page", () => {
    const { result } = renderHook(() => useCursorPages());

    act(() => result.current.previous());
    expect(result.current.pageNumber).toBe(1);
    expect(result.current.cursor).toBeUndefined();
  });

  it("discards the trail when the filters change", () => {
    const { result } = renderHook(() => useCursorPages());

    act(() => result.current.next("cursor-a"));
    act(() => result.current.next("cursor-b"));

    // The collected cursors point into the previous result set. Carrying one
    // across a filter change resumes a walk through rows that are no longer
    // in the list.
    act(() => result.current.reset());

    expect(result.current.cursor).toBeUndefined();
    expect(result.current.pageNumber).toBe(1);
    expect(result.current.hasPrevious).toBe(false);
  });
});
