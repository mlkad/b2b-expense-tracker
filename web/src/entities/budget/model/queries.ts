import { queryOptions } from "@tanstack/react-query";
import { api, decode, listOf } from "@/shared/api";

import { budgetConsumptionSchema, budgetSchema } from "./schema";

export const budgetKeys = {
  all: ["budgets"] as const,
  list: () => [...budgetKeys.all, "list"] as const,
  consumption: (query: string) => [...budgetKeys.all, "consumption", query] as const,
};

const budgets = listOf(budgetSchema);
const consumption = listOf(budgetConsumptionSchema);

export function budgetsQuery() {
  return queryOptions({
    queryKey: budgetKeys.list(),
    queryFn: async () => decode(budgets, await api.get("/budgets"), "GET /budgets").items,
  });
}

export function budgetConsumptionQuery(search = "") {
  return queryOptions({
    queryKey: budgetKeys.consumption(search),
    queryFn: async () =>
      decode(
        consumption,
        await api.get(`/budgets/consumption${search ? `?${search}` : ""}`),
        "GET /budgets/consumption",
      ).items,
  });
}
