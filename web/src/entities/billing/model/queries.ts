import { queryOptions } from "@tanstack/react-query";

import { api, decode } from "@/shared/api";

import { entitlementSchema } from "./schema";

export const billingKeys = { entitlement: () => ["billing", "entitlement"] as const };

export function entitlementQuery() {
  return queryOptions({
    queryKey: billingKeys.entitlement(),
    queryFn: async () =>
      decode(entitlementSchema, await api.get("/billing/entitlement"), "GET /billing/entitlement"),
    staleTime: 60 * 1000,
  });
}
