import { z } from "zod";

import { timestamp } from "@/shared/api";

/**
 * What the tenant's plan permits.
 *
 * The limits come back capitalised because the gateway's own projection is
 * serialised straight through; renaming them here would hide that rather than
 * fix it. A negative limit means unlimited.
 */
export const entitlementSchema = z.object({
  plan: z.string(),
  status: z.string(),
  known: z.boolean(),
  in_grace_period: z.boolean(),
  needs_checkout: z.boolean(),
  current_period_end: timestamp,
  cancel_at_period_end: z.boolean(),
  limits: z.object({
    Seats: z.number().int(),
    Departments: z.number().int(),
    VendorSubscriptions: z.number().int(),
    ExportRows: z.number().int(),
  }),
});

export type Entitlement = z.infer<typeof entitlementSchema>;
