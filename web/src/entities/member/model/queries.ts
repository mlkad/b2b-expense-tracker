import { queryOptions } from "@tanstack/react-query";
import { api, decode, listOf } from "@/shared/api";

import { memberSchema } from "./schema";

export const memberKeys = {
  all: ["members"] as const,
  list: () => [...memberKeys.all, "list"] as const,
};

const members = listOf(memberSchema);

export function membersQuery() {
  return queryOptions({
    queryKey: memberKeys.list(),
    queryFn: async () => decode(members, await api.get("/members"), "GET /members").items,
  });
}
