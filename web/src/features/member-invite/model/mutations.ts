import { useMutation, useQueryClient } from "@tanstack/react-query";
import { z } from "zod";

import { api } from "@/shared/api";
import { memberKeys } from "@/entities/member";
import { ROLES, type Role } from "@/shared/config";

export const inviteSchema = z.object({
  email: z.email("Enter the address to invite."),
  role: z.enum(ROLES),
});

export type Invitation = z.infer<typeof inviteSchema>;

/**
 * Ranks, most privileged first, matching the server's enum.
 *
 * Offering only roles below the caller's own avoids a 403 they cannot do
 * anything about - the server refuses the rest regardless, since inviting a
 * peer is one step from inviting a superior.
 */
const RANK: Role[] = ["owner", "admin", "finance", "manager", "member", "viewer"];

export function rolesBelow(actor: Role | undefined): Role[] {
  const actorRank = actor ? RANK.indexOf(actor) : RANK.length;
  return RANK.filter((r) => RANK.indexOf(r) > actorRank);
}

export function useInviteMember() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async ({ email, role }: Invitation) => {
      await api.post("/members", {
        email,
        role,
        department_id: null,
        approval_limit_minor: null,
      });
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: memberKeys.all }),
  });
}
