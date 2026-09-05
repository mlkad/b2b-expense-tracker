import { describe, expect, it } from "vitest";

import { EMPTY_FILTERS, exportSearch, isFiltered, listSearch } from "./use-expense-filters";

describe("listSearch", () => {
  it("asks for a page size and nothing else when nothing is filtered", () => {
    expect(listSearch(EMPTY_FILTERS)).toBe("limit=20");
  });

  it("carries only the filters that are set", () => {
    const search = listSearch({ ...EMPTY_FILTERS, status: "approved", q: "Figma" });
    const params = new URLSearchParams(search);

    expect(params.get("status")).toBe("approved");
    expect(params.get("q")).toBe("Figma");
    // An empty filter must not become `category=`, which the server would read
    // as a request for claims with no category.
    expect(params.has("category")).toBe(false);
  });
});

describe("exportSearch", () => {
  it("signs the same slice the table is showing", () => {
    const params = new URLSearchParams(
      exportSearch({ ...EMPTY_FILTERS, status: "paid", from: "2026-01-01" }, "xlsx"),
    );

    expect(params.get("format")).toBe("xlsx");
    expect(params.get("status")).toBe("paid");
    expect(params.get("from")).toBe("2026-01-01");
  });

  /**
   * The free-text search is not an export filter on the server. Including it
   * would ask for a signature over a query the export route will not accept,
   * and the download would fail after the user had waited for it.
   */
  it("drops the free-text search", () => {
    const params = new URLSearchParams(
      exportSearch({ ...EMPTY_FILTERS, q: "Figma", status: "paid" }, "csv"),
    );

    expect(params.has("q")).toBe(false);
    expect(params.get("status")).toBe("paid");
  });

  it("does not carry a page size, which would truncate the report", () => {
    expect(new URLSearchParams(exportSearch(EMPTY_FILTERS, "pdf")).has("limit")).toBe(false);
  });
});

describe("isFiltered", () => {
  it("tells an empty result set apart from an empty organisation", () => {
    // Which decides whether the empty state says "nothing has been filed yet"
    // or "try widening the date range".
    expect(isFiltered(EMPTY_FILTERS)).toBe(false);
    expect(isFiltered({ ...EMPTY_FILTERS, category: "travel" })).toBe(true);
  });
});
