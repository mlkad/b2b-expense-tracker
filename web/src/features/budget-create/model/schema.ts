import { z } from "zod";

import { exponentFor, parseAmount } from "@/shared/lib/money";

/**
 * A budget's ceiling and the period it covers.
 *
 * The threshold is asked for as a percentage because that is what a person
 * thinks in, and converted to the basis points the server stores. Two budgets
 * cannot cover the same department and overlapping dates - the database refuses
 * it with an exclusion constraint - so the only ordering rule worth checking
 * here is that the period runs forwards.
 */
export function budgetDraftSchema(currency: string) {
  const exponent = exponentFor(currency);

  return z
    .object({
      department_id: z.string(),
      period_start: z.string().min(1, "When does the period start?"),
      period_end: z.string().min(1, "When does it end?"),

      amount: z.string().transform((value, ctx) => {
        const minor = parseAmount(value, currency);
        if (minor === null) {
          ctx.addIssue({
            code: "custom",
            message:
              exponent === 0
                ? `${currency} has no minor unit, so enter a whole number.`
                : "Enter an amount.",
          });
          return z.NEVER;
        }
        if (minor <= 0) {
          ctx.addIssue({ code: "custom", message: "The budget must be more than zero." });
          return z.NEVER;
        }
        return minor;
      }),

      alert_threshold: z.coerce
        .number("Enter a percentage.")
        .gt(0, "The alert must be above zero.")
        .lte(100, "The alert cannot be past the whole budget."),
    })
    .refine((values) => values.period_end > values.period_start, {
      message: "The end date must be after the start date.",
      path: ["period_end"],
    });
}

export type BudgetDraft = z.output<ReturnType<typeof budgetDraftSchema>>;
