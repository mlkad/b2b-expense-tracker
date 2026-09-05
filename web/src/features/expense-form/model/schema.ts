import { z } from "zod";

import { expenseCategorySchema } from "@/entities/expense";
import { exponentFor, parseAmount } from "@/shared/lib/money";
import { todayISO } from "@/shared/lib/format";

/**
 * The claim form, validated against the currency it is filed in.
 *
 * The schema is built per currency rather than fixed, because how many decimal
 * places are valid is a property of the currency and not of the form: JPY has
 * none, KWD has three. A single "two decimal places" rule would reject a
 * correct yen amount and quietly accept a dinar one that loses a digit.
 *
 * The amount is parsed here, so what leaves this schema is the integer the
 * server stores - the string never reaches the arithmetic.
 */
export function expenseDraftSchema(currency: string) {
  const exponent = exponentFor(currency);
  const places = `${exponent} decimal place${exponent === 1 ? "" : "s"}`;

  return z.object({
    merchant: z.string().trim().min(1, "Who was this paid to?").max(200),

    amount: z.string().transform((value, ctx) => {
      const minor = parseAmount(value, currency);
      if (minor === null) {
        ctx.addIssue({
          code: "custom",
          message:
            exponent === 0
              ? `${currency} has no minor unit, so enter a whole number.`
              : `Enter an amount with at most ${places}.`,
        });
        return z.NEVER;
      }
      if (minor <= 0) {
        ctx.addIssue({ code: "custom", message: "The amount must be more than zero." });
        return z.NEVER;
      }
      return minor;
    }),

    category: expenseCategorySchema,

    spent_at: z
      .string()
      .min(1, "When was it spent?")
      // A claim for money not yet spent is a typo, and the server refuses it.
      .refine((value) => value <= todayISO(), "That date is in the future."),

    department_id: z.string(),
    description: z.string().trim().max(4000),
  });
}

export type ExpenseDraft = z.output<ReturnType<typeof expenseDraftSchema>>;
