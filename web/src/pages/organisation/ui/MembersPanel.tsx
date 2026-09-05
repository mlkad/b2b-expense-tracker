import { useQuery } from "@tanstack/react-query";

import { membersQuery } from "@/entities/member";
import { useProfile } from "@/entities/session";
import { InviteMemberForm } from "@/features/member-invite";
import { ApiError } from "@/shared/api";
import { formatMinor } from "@/shared/lib/money";
import { Badge, Card, EmptyState, ErrorNotice, SkeletonRows, TableHead } from "@/shared/ui/kit";
import { Monogram } from "@/shared/ui/Monogram";
import { MoreIcon } from "@/shared/ui/icons";

function statusTone(status: string) {
  if (status === "active") return "positive" as const;
  if (status === "invited") return "caution" as const;
  return "danger" as const;
}

export function MembersPanel({
  inviting,
  onDone,
}: {
  inviting: boolean;
  onDone: () => void;
}) {
  const profile = useProfile();
  const members = useQuery(membersQuery());

  return (
    <div className="flex flex-col gap-4">
      <p className="text-sm text-muted">
        You can only invite and administer members below your own role.
      </p>

      {members.error && (
        <ErrorNotice
          title="Could not load the members"
          detail={members.error.message}
          traceId={members.error instanceof ApiError ? members.error.traceId : undefined}
        />
      )}

      {inviting && <InviteMemberForm actorRole={profile?.role} onInvited={onDone} />}

      <Card>
        {members.isPending ? (
          <SkeletonRows rows={4} columns={4} />
        ) : (members.data?.length ?? 0) === 0 ? (
          <EmptyState title="Nobody else yet" />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[42rem] text-sm">
              <caption className="sr-only">Members of this organisation</caption>
              <TableHead>
                <th scope="col" className="py-3 pr-4 pl-5 font-medium">Person</th>
                <th scope="col" className="px-4 py-3 font-medium">Role</th>
                <th scope="col" className="px-4 py-3 font-medium">Department</th>
                <th scope="col" className="px-4 py-3 font-medium">Status</th>
                <th scope="col" className="px-4 py-3 text-right font-medium">Approval limit</th>
                <th scope="col" className="w-12 py-3 pr-4 pl-2">
                  <span className="sr-only">Actions</span>
                </th>
              </TableHead>
              <tbody className="divide-y divide-line-soft">
                {(members.data ?? []).map((member) => (
                  <tr key={member.id} className="transition-colors hover:bg-surface/70">
                    <td className="py-3.5 pr-4 pl-5">
                      <span className="flex items-center gap-2.5">
                        <Monogram name={member.full_name || member.email} />
                        <span>
                          <span className="font-medium">{member.full_name || member.email}</span>
                          {member.full_name && (
                            <span className="ml-2 text-xs text-faint">{member.email}</span>
                          )}
                          {member.id === profile?.membership_id && (
                            <span className="ml-2 text-xs text-faint">(you)</span>
                          )}
                        </span>
                      </span>
                    </td>
                    <td className="px-4 py-3.5">
                      <Badge tone="brand">{member.role}</Badge>
                    </td>
                    <td className="px-4 py-3.5 text-muted">{member.department_name ?? "—"}</td>
                    <td className="px-4 py-3.5">
                      <Badge tone={statusTone(member.status)}>{member.status}</Badge>
                    </td>
                    <td className="px-4 py-3.5 text-right tabular-nums text-muted">
                      {member.approval_limit_minor == null
                        ? "role default"
                        : formatMinor(
                            member.approval_limit_minor,
                            profile?.default_currency ?? "USD",
                          )}
                    </td>
                    <td className="py-3.5 pr-4 pl-2 text-right">
                      {/* Editing a membership - the role, the department, the
                          approval limit - is a screen this build does not have
                          yet, so the control that would open it is disabled
                          rather than absent: a column that appears later moves
                          every figure in the table sideways. */}
                      <button
                        type="button"
                        disabled
                        aria-label={`Manage ${member.full_name || member.email} (not available yet)`}
                        title="Managing a membership is not available yet"
                        className="inline-grid size-7 cursor-not-allowed place-items-center rounded-md text-faint/50"
                      >
                        <MoreIcon className="size-4" />
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </div>
  );
}
