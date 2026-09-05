import { z } from "zod";

import { isoDate, moneySchema } from "@/shared/api";

export const vendorSubscriptionSchema = z.object({
  id: z.string(),
  vendor: z.string(),
  plan_name: z.string().nullish(),
  department_id: z.string().nullish(),
  department_name: z.string().nullish(),
  amount: moneySchema,
  cadence: z.enum(["weekly", "monthly", "quarterly", "annual"]),
  status: z.enum(["active", "paused", "cancelled"]),
  next_charge_on: isoDate,
  auto_create_expense: z.boolean(),
  /** What the vendor costs over a year, for comparing cadences side by side. */
  annualised_minor: z.number().int(),
});

export type VendorSubscription = z.infer<typeof vendorSubscriptionSchema>;
