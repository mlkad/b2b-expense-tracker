import { z } from "zod";

import { roleSchema } from "@/shared/config";

export const memberSchema = z.object({
  id: z.string(),
  user_id: z.string(),
  email: z.string(),
  full_name: z.string().nullish(),
  role: roleSchema,
  status: z.string(),
  department_id: z.string().nullish(),
  department_name: z.string().nullish(),
  /** Negative means unlimited; null means the role's default applies. */
  approval_limit_minor: z.number().int().nullish(),
});

export type Member = z.infer<typeof memberSchema>;
