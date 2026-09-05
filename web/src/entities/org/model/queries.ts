import { queryOptions } from "@tanstack/react-query";
import { z } from "zod";

import { api, decode } from "@/shared/api";

import { vendorSubscriptionSchema } from "./schema";

export const orgKeys = { vendors: () => ["vendor-subscriptions"] as const };

/**
 * The subscription list carries a total alongside its items.
 *
 * Summed on the server, not here: the annualised figure has to account for
 * paused rows and mixed cadences the same way the sweep does, and a second
 * implementation of that in TypeScript would eventually disagree with the one
 * that actually generates the claims.
 */
const vendors = z.object({
  items: z.array(vendorSubscriptionSchema),
  annualised_total_minor: z.number().int(),
  currency: z.string(),
});

export function vendorSubscriptionsQuery() {
  return queryOptions({
    queryKey: orgKeys.vendors(),
    queryFn: async () =>
      decode(vendors, await api.get("/vendor-subscriptions"), "GET /vendor-subscriptions"),
  });
}
