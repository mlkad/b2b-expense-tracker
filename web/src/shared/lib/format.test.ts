import { describe, expect, it } from "vitest";

import { actionLabel, formatDate, formatTimestamp, statusLabel } from "./format";

describe("formatDate", () => {
  it("renders a calendar date in UTC whatever the reader's zone", () => {
    // spent_at is the date on the receipt. Rendering it in the browser's zone
    // shows the previous day to anybody west of Greenwich, which gets reported
    // as "the export is wrong".
    expect(formatDate("2026-03-01T00:00:00Z", "en-GB")).toBe("1 Mar 2026");
    expect(formatDate("2026-01-01T00:00:00Z", "en-GB")).toBe("1 Jan 2026");
  });

  it("does not throw on rubbish", () => {
    expect(formatDate("not a date")).toBe("—");
  });
});

describe("formatTimestamp", () => {
  it("renders an absent instant as a dash rather than Invalid Date", () => {
    expect(formatTimestamp(null)).toBe("—");
    expect(formatTimestamp(undefined)).toBe("—");
    expect(formatTimestamp("")).toBe("—");
  });

  it("renders a real one", () => {
    expect(formatTimestamp("2026-03-01T14:02:00Z", "en-GB")).toContain("2026");
  });
});

describe("labels", () => {
  it("reads as English rather than as a database enum", () => {
    expect(statusLabel("pending_approval")).toBe("Awaiting approval");
    expect(actionLabel("pay")).toBe("Mark as paid");
  });

  it("falls back for a value this build does not know", () => {
    // A status added by a newer server must render as something, not as blank.
    expect(statusLabel("under_review")).toBe("Under review");
  });
});
