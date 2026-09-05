import { z } from "zod";

import { timestamp } from "@/shared/api";
import { roleSchema } from "@/shared/config";

/** Who the caller is, and what this membership lets them do. */
export const profileSchema = z.object({
  user_id: z.string(),
  email: z.string(),
  full_name: z.string().optional(),
  tenant_id: z.string(),
  tenant_slug: z.string(),
  tenant_name: z.string(),
  default_currency: z.string(),
  membership_id: z.string(),
  role: roleSchema,
  status: z.string(),
  department_id: z.string().nullish(),
  /** Negative means unlimited. */
  approval_limit_minor: z.number().int(),
  permissions: z.array(z.string()),
});

export const sessionSchema = z.object({
  access_token: z.string(),
  expires_at: timestamp,
  tenant_id: z.string(),
  tenant_slug: z.string(),
  role: roleSchema,
});

export type Profile = z.infer<typeof profileSchema>;
export type Session = z.infer<typeof sessionSchema>;
