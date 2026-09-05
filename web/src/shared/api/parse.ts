import { z } from "zod";

/**
 * Thrown when a response does not match the shape the caller expected.
 *
 * It names the field. The alternative is what this codebase had before: a
 * response cast to an interface, believed without being checked, and a failure
 * that surfaces three components away as "cannot read properties of undefined"
 * with nothing pointing at the endpoint that changed.
 */
export class ContractError extends Error {
  readonly source: string;
  readonly issues: string[];

  constructor(source: string, error: z.ZodError) {
    const issues = error.issues.map((issue) => {
      const path = issue.path.join(".");
      return path ? `${path}: ${issue.message}` : issue.message;
    });
    super(`${source} did not match the expected shape (${issues.join("; ")})`);
    this.name = "ContractError";
    this.source = source;
    this.issues = issues;
  }
}

/**
 * Checks a decoded response against its schema.
 *
 * Unknown keys are stripped rather than rejected - that is zod's default for an
 * object and it is the behaviour worth having, because the server adding a
 * field must never break a dashboard that is already deployed. A missing or
 * mistyped field is the opposite case and does throw: the screen cannot render
 * correctly from it, and saying so is more useful than drawing it wrong.
 */
export function decode<T>(schema: z.ZodType<T>, value: unknown, source: string): T {
  const result = schema.safeParse(value);
  if (!result.success) throw new ContractError(source, result.error);
  return result.data;
}

/** The envelope every keyset-paginated collection comes back in. */
export function pageOf<T>(item: z.ZodType<T>) {
  return z.object({
    items: z.array(item),
    has_more: z.boolean(),
    next_cursor: z.string().optional(),
  });
}

export type Page<T> = { items: T[]; has_more: boolean; next_cursor?: string };

/**
 * An unpaginated collection.
 *
 * The server wraps every list in an object even when there is nothing to
 * paginate, so that a field can be added beside the items later without
 * changing the response from an array into an object - which would break every
 * existing caller at once.
 */
export function listOf<T>(item: z.ZodType<T>) {
  return z.object({ items: z.array(item) });
}

/** An exact amount. Never a float: see the note on formatMoney. */
export const moneySchema = z.object({
  amount_minor: z.number().int(),
  currency: z.string(),
  /** Rendered by the server with the currency's own exponent. */
  formatted: z.string(),
});

export type Money = z.infer<typeof moneySchema>;

/** ISO-8601 from the server; kept as a string and formatted at the edge. */
export const timestamp = z.string();

/** A calendar date with no time: `2026-09-04`. */
export const isoDate = z.string();
