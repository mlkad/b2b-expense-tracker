import { describe, expect, it } from "vitest";

import { exponentFor, formatBasisPoints, formatMoney, parseAmount } from "./money";

describe("currency exponents", () => {
  it("knows the three groups", () => {
    // The default of two is wrong for JPY and for the Gulf currencies, and
    // getting it wrong produces a figure off by a factor of a hundred rather
    // than an error.
    expect(exponentFor("USD")).toBe(2);
    expect(exponentFor("EUR")).toBe(2);
    expect(exponentFor("JPY")).toBe(0);
    expect(exponentFor("KRW")).toBe(0);
    expect(exponentFor("KWD")).toBe(3);
    expect(exponentFor("BHD")).toBe(3);
  });

  it("is case-insensitive", () => {
    expect(exponentFor("jpy")).toBe(0);
  });
});

describe("formatMoney", () => {
  const money = (amount_minor: number, currency: string) => ({
    amount_minor,
    currency,
    formatted: "",
  });

  it("places the decimal point according to the currency", () => {
    expect(formatMoney(money(125000, "USD"), "en-US")).toBe("$1,250.00");
    // A thousand yen is a thousand yen, not ten.
    expect(formatMoney(money(1000, "JPY"), "en-US")).toBe("¥1,000");
    expect(formatMoney(money(1234, "KWD"), "en-US")).toContain("1.234");
  });

  it("renders zero and negatives", () => {
    expect(formatMoney(money(0, "USD"), "en-US")).toBe("$0.00");
    expect(formatMoney(money(-500, "USD"), "en-US")).toContain("5.00");
  });

  it("keeps every digit of a large amount", () => {
    // Minor units are exact in a double well past any real claim; this is the
    // assertion that a rounding shortcut would break.
    expect(formatMoney(money(922337203685, "USD"), "en-US")).toBe("$9,223,372,036.85");
  });

  it("falls back to the server's rendering for an unknown currency", () => {
    const unknown = { amount_minor: 100, currency: "XYZ", formatted: "1.00" };
    expect(formatMoney(unknown, "en-US")).toContain("XYZ");
  });
});

describe("parseAmount", () => {
  it("reads what a person types", () => {
    expect(parseAmount("12.50", "USD")).toBe(1250);
    expect(parseAmount("0.01", "USD")).toBe(1);
    expect(parseAmount("1250", "USD")).toBe(125000);
    expect(parseAmount(" 1,250.00 ", "USD")).toBe(125000);
    expect(parseAmount(".5", "USD")).toBe(50);
  });

  it("uses the currency's own precision", () => {
    expect(parseAmount("1000", "JPY")).toBe(1000);
    expect(parseAmount("1.234", "KWD")).toBe(1234);
  });

  it("refuses more precision than the currency has", () => {
    // 12.505 USD is not an amount anybody can be paid, and silently rounding
    // it loses half a cent in a direction nobody chose.
    expect(parseAmount("12.505", "USD")).toBeNull();
    expect(parseAmount("10.5", "JPY")).toBeNull();
  });

  it("is exact where a float round trip is not", () => {
    // Math.round(parseFloat("1.005") * 100) gives 100, because the double
    // nearest 1.005 is below it. String arithmetic gives the digits typed.
    expect(parseAmount("1.005", "KWD")).toBe(1005);
    expect(parseAmount("8.115", "KWD")).toBe(8115);
    expect(parseAmount("0.07", "USD")).toBe(7);
    expect(parseAmount("1.10", "USD")).toBe(110);
  });

  it("rejects anything that is not a number", () => {
    for (const input of ["", "   ", "abc", "1.2.3", "1e5", "--1", "$5", "5%"]) {
      expect(parseAmount(input, "USD"), input).toBeNull();
    }
  });

  it("rejects an amount too large to be exact", () => {
    expect(parseAmount("99999999999999999999", "USD")).toBeNull();
  });
});

describe("formatBasisPoints", () => {
  it("renders budget usage", () => {
    expect(formatBasisPoints(8000)).toBe("80.0%");
    expect(formatBasisPoints(12050)).toBe("120.5%");
    expect(formatBasisPoints(0)).toBe("0.0%");
  });
});
