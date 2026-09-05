import type { Money } from "../api/types";

/**
 * How many decimal places a currency's minor unit uses.
 *
 * The default of two is wrong for two groups that appear in real expense data:
 * JPY and KRW have no minor unit at all, and the Gulf currencies use three.
 * Getting it wrong does not produce an error - it produces a figure off by a
 * factor of a hundred - so the exceptions are listed rather than assumed. The
 * same table exists on the server; both are derived from ISO 4217.
 */
const EXPONENTS: Record<string, number> = {
  JPY: 0, KRW: 0, VND: 0, CLP: 0, ISK: 0, UGX: 0, XAF: 0, XOF: 0, XPF: 0,
  BHD: 3, IQD: 3, JOD: 3, KWD: 3, LYD: 3, OMR: 3, TND: 3,
};

export function exponentFor(currency: string): number {
  return EXPONENTS[currency.toUpperCase()] ?? 2;
}

/**
 * Renders an amount for display.
 *
 * The arithmetic is done by Intl on a number derived from the integer, and the
 * integer is what the application carries everywhere else. Nothing in this
 * dashboard ever parses `formatted` back into a number: JavaScript has one
 * numeric type and it is a double, so a round trip through a decimal string is
 * where a total quietly stops matching the receipts.
 *
 * Minor units are exact in a double up to 2^53, which is ninety trillion in
 * cents - well past any expense claim, and the reason dividing here is safe
 * when parsing would not be.
 */
export function formatMoney(money: Money, locale?: string): string {
  const exponent = exponentFor(money.currency);
  const value = money.amount_minor / 10 ** exponent;

  try {
    return new Intl.NumberFormat(locale, {
      style: "currency",
      currency: money.currency,
      minimumFractionDigits: exponent,
      maximumFractionDigits: exponent,
    }).format(value);
  } catch {
    // An unrecognised currency code makes Intl throw. Falling back to the
    // server's own rendering is better than showing nothing, and better than
    // showing a bare number with no indication of the currency.
    return `${money.formatted} ${money.currency}`;
  }
}

/** Formats minor units the caller already holds, for figures with no Money. */
export function formatMinor(amountMinor: number, currency: string, locale?: string): string {
  return formatMoney({ amount_minor: amountMinor, currency, formatted: "" }, locale);
}

/**
 * Parses what somebody typed into minor units.
 *
 * Deliberately string arithmetic rather than `Math.round(parseFloat(x) * 100)`.
 * That rounds 1.005 to 100 rather than 101 on some inputs, because the double
 * nearest 1.005 is slightly below it - a rounding error of one cent, in the
 * direction of the customer's disadvantage, on an amount they typed exactly.
 */
export function parseAmount(input: string, currency: string): number | null {
  const trimmed = input.trim().replace(/[\s,]/g, "");
  if (trimmed === "") return null;

  const match = /^(-?)(\d*)(?:\.(\d*))?$/.exec(trimmed);
  if (!match) return null;

  const [, sign, wholePart, fractionPart = ""] = match;
  if (wholePart === "" && fractionPart === "") return null;

  const exponent = exponentFor(currency);
  if (fractionPart.length > exponent) return null; // more precision than the currency has

  const padded = fractionPart.padEnd(exponent, "0");
  const digits = `${wholePart || "0"}${padded}`;

  const value = Number(digits);
  if (!Number.isSafeInteger(value)) return null;

  return sign === "-" ? -value : value;
}

/** Basis points as a percentage, for budget usage. */
export function formatBasisPoints(bps: number): string {
  return `${(bps / 100).toFixed(1)}%`;
}
