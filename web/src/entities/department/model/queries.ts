import { queryOptions } from "@tanstack/react-query";
import { api, decode, listOf } from "@/shared/api";

import { departmentSchema } from "./schema";

export const departmentKeys = {
  all: ["departments"] as const,
  list: () => [...departmentKeys.all, "list"] as const,
};

const departments = listOf(departmentSchema);

export function departmentsQuery() {
  return queryOptions({
    queryKey: departmentKeys.list(),
    queryFn: async () =>
      decode(departments, await api.get("/departments"), "GET /departments").items,
    // Departments change a few times a year. Refetching them on every screen
    // that needs a picker is a request nobody asked for.
    staleTime: 5 * 60 * 1000,
  });
}
