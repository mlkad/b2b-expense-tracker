import { describe, expect, it } from "vitest";
import { z } from "zod";

import { ContractError, decode, listOf, moneySchema, pageOf } from "./parse";

const claim = z.object({ id: z.string(), amount: moneySchema });

const money = { amount_minor: 4500, currency: "USD", formatted: "$45.00" };

describe("decode", () => {
  it("keeps a response that matches", () => {
    expect(decode(claim, { id: "a", amount: money }, "GET /x")).toEqual({ id: "a", amount: money });
  });

  /**
   * The forward-compatibility rule. A server that adds a field must never break
   * a dashboard that is already deployed, and rejecting unknown keys would make
   * every additive change a breaking one.
   */
  it("strips a field the client does not know about", () => {
    const decoded = decode(claim, { id: "a", amount: money, tags: ["new"] }, "GET /x");
    expect(decoded).toEqual({ id: "a", amount: money });
  });

  it("names the field that did not match", () => {
    const bad = { id: "a", amount: { ...money, amount_minor: "4500" } };

    // The whole point of parsing rather than casting: the failure says which
    // field, at the endpoint that produced it, instead of surfacing three
    // components later as "cannot read properties of undefined".
    expect(() => decode(claim, bad, "GET /expenses")).toThrow(ContractError);

    try {
      decode(claim, bad, "GET /expenses");
    } catch (err) {
      expect(err).toBeInstanceOf(ContractError);
      const failure = err as ContractError;
      expect(failure.source).toBe("GET /expenses");
      expect(failure.issues[0]).toContain("amount.amount_minor");
      expect(failure.message).toContain("GET /expenses");
    }
  });

  it("rejects a missing field rather than rendering the screen wrong", () => {
    expect(() => decode(claim, { id: "a" }, "GET /x")).toThrow(ContractError);
  });

  /**
   * A float amount is the failure this catches that a cast never would. Money
   * is an integer count of minor units end to end, and 45.5 cents is not a
   * quantity the rest of the app can represent.
   */
  it("rejects a non-integer amount", () => {
    const bad = { id: "a", amount: { ...money, amount_minor: 45.5 } };
    expect(() => decode(claim, bad, "GET /x")).toThrow(ContractError);
  });
});

describe("envelopes", () => {
  it("reads a keyset page", () => {
    const page = decode(
      pageOf(z.object({ id: z.string() })),
      { items: [{ id: "a" }], has_more: true, next_cursor: "c1" },
      "GET /x",
    );
    expect(page.next_cursor).toBe("c1");
  });

  it("treats a page with no cursor as the last one", () => {
    const page = decode(
      pageOf(z.object({ id: z.string() })),
      { items: [], has_more: false },
      "GET /x",
    );
    expect(page.next_cursor).toBeUndefined();
  });

  it("reads an unpaginated collection", () => {
    const list = decode(listOf(z.object({ id: z.string() })), { items: [{ id: "a" }] }, "GET /x");
    expect(list.items).toHaveLength(1);
  });

  /**
   * Every collection this API returns is wrapped in an object, even when there
   * is nothing to paginate - and taking the array directly is exactly the
   * mistake this schema is here to catch.
   */
  it("refuses a bare array where an envelope was expected", () => {
    expect(() => decode(listOf(z.object({ id: z.string() })), [{ id: "a" }], "GET /x")).toThrow(
      ContractError,
    );
  });
});
