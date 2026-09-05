import { queryOptions } from "@tanstack/react-query";
import { z } from "zod";

import { api, decode, moneySchema } from "@/shared/api";

import { expenseStatusSchema } from "./schema";

export const statusTotalSchema = z.object({
  status: expenseStatusSchema,
  claim_count: z.number().int(),
  total: moneySchema,
});

export const departmentTotalSchema = z.object({
  department_id: z.string().nullish(),
  department_name: z.string(),
  claim_count: z.number().int(),
  total: moneySchema,
});

export const summarySchema = z.object({
  by_status: z.array(statusTotalSchema),
  by_department: z.array(departmentTotalSchema),
});

export type StatusTotal = z.infer<typeof statusTotalSchema>;
export type DepartmentTotal = z.infer<typeof departmentTotalSchema>;
export type Summary = z.infer<typeof summarySchema>;

export const summaryKeys = { summary: (query: string) => ["expenses", "summary", query] as const };

export function summaryQuery(search = "") {
  return queryOptions({
    queryKey: summaryKeys.summary(search),
    queryFn: async () =>
      decode(summarySchema, await api.get(`/summary${search ? `?${search}` : ""}`), "GET /summary"),
  });
}
