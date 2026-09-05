import { useState } from "react";
import { useQuery } from "@tanstack/react-query";

import { membersQuery } from "@/entities/member";
import { useProfile } from "@/entities/session";
import { InviteMemberForm } from "@/features/member-invite";
import { ApiError } from "@/shared/api";
import { formatMinor } from "@/shared/lib/money";
import { Badge, Button, Card, EmptyState, ErrorNotice, SkeletonRows } from "@/shared/ui/kit";

function statusTone(status: string) {
  if (status === "active") return "positive" as const;
  if (status === "invited") return "caution" as const;
  return "danger" as const;
}

export function MembersPanel() {
  const profile = useProfile();
  const [inviting, setInviting] = useState(false);
  const members = useQuery(membersQuery());

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between">
        <p className="text-sm text-ink-600">
          You can only invite and administer members below your own role.
        </p>
        <Button onClick={() => setInviting((open) => !open)}>
          {inviting ? "Cancel" : "Invite"}
        </Button>
      </div>

      {members.error && (
        <ErrorNotice
          title="Could not load the members"
          detail={members.error.message}
          traceId={members.error instanceof ApiError ? members.error.traceId : undefined}
        />
      )}

      {inviting && (
        <InviteMemberForm actorRole={profile?.role} onInvited={() => setInviting(false)} />
      )}

      <Card>
        {members.isPending ? (
          <SkeletonRows rows={4} columns={4} />
        ) : (members.data?.length ?? 0) === 0 ? (
          <EmptyState title="Nobody else yet" />
        ) : (
          <table className="w-full text-sm">
            <caption className="sr-only">Members of this organisation</caption>
            <thead>
              <tr className="border-b border-ink-100 text-left text-xs uppercase tracking-wide text-ink-600">
                <th scope="col" className="px-4 py-2.5 font-medium">Person</th>
                <th scope="col" className="px-4 py-2.5 font-medium">Role</th>
                <th scope="col" className="px-4 py-2.5 font-medium">Department</th>
                <th scope="col" className="px-4 py-2.5 font-medium">Status</th>
                <th scope="col" className="px-4 py-2.5 text-right font-medium">Approval limit</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-ink-100">
              {(members.data ?? []).map((member) => (
                <tr key={member.id}>
                  <td className="px-4 py-3">
                    <span className="font-medium">{member.full_name || member.email}</span>
                    {member.full_name && (
                      <span className="ml-2 text-xs text-ink-600">{member.email}</span>
                    )}
                    {member.id === profile?.membership_id && (
                      <span className="ml-2 text-xs text-ink-600">(you)</span>
                    )}
                  </td>
                  <td className="px-4 py-3">
                    <Badge tone="brand">{member.role}</Badge>
                  </td>
                  <td className="px-4 py-3 text-ink-600">{member.department_name ?? "—"}</td>
                  <td className="px-4 py-3">
                    <Badge tone={statusTone(member.status)}>{member.status}</Badge>
                  </td>
                  <td className="px-4 py-3 text-right tabular-nums text-ink-600">
                    {member.approval_limit_minor == null
                      ? "role default"
                      : formatMinor(
                          member.approval_limit_minor,
                          profile?.default_currency ?? "USD",
                        )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>
    </div>
  );
}
