import { z } from "zod";

/**
 * The membership roles, most privileged first, matching the server's enum.
 *
 * In shared rather than in the member entity because the session speaks the
 * same vocabulary: a profile carries a role, and a member list carries roles,
 * and neither of those two slices owns the word. Ordering is meaningful - it is
 * what "below your own role" is decided against.
 */
export const roleSchema = z.enum(["owner", "admin", "finance", "manager", "member", "viewer"]);

export type Role = z.infer<typeof roleSchema>;

export const ROLES = roleSchema.options;
