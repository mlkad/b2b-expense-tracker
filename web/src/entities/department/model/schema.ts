import { z } from "zod";

import { timestamp } from "@/shared/api";

export const departmentSchema = z.object({
  id: z.string(),
  name: z.string(),
  parent_id: z.string().nullish(),
  archived_at: timestamp.nullish(),
});

export type Department = z.infer<typeof departmentSchema>;
