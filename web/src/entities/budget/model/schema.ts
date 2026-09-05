import { z } from "zod";

import { isoDate, moneySchema } from "@/shared/api";

export const budgetSchema = z.object({
  id: z.string(),
  department_id: z.string().nullish(),
  period_start: isoDate,
  period_end: isoDate,
  amount: moneySchema,
  alert_threshold_bps: z.number().int(),
});

/** A budget with its spend rolled up: what the budgets screen actually shows. */
export const budgetConsumptionSchema = z.object({
  budget_id: z.string(),
  department_id: z.string().nullish(),
  department_name: z.string().nullish(),
  period_start: isoDate,
  period_end: isoDate,
  budget: moneySchema,
  consumed: moneySchema,
  remaining: moneySchema,
  claim_count: z.number().int(),
  /** Basis points, not a percentage: 10000 is the whole budget. */
  usage_bps: z.number().int(),
  alert_threshold_bps: z.number().int(),
  breached: z.boolean(),
});

export type Budget = z.infer<typeof budgetSchema>;
export type BudgetConsumption = z.infer<typeof budgetConsumptionSchema>;
