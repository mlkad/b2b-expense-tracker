import { useState, type FormEvent } from "react";

import type { Role } from "@/shared/config";
import { useFormErrors } from "@/shared/lib/form";
import { Button, Card, ErrorNotice, Field, Select, TextInput } from "@/shared/ui/kit";

import { inviteSchema, rolesBelow, useInviteMember } from "../model/mutations";

export function InviteMemberForm({
  actorRole,
  onInvited,
}: {
  actorRole?: Role;
  onInvited: () => void;
}) {
  const form = useFormErrors(inviteSchema);
  const invite = useInviteMember();

  const [email, setEmail] = useState("");
  const [role, setRole] = useState<Role>("member");

  const available = rolesBelow(actorRole);

  async function onSubmit(event: FormEvent) {
    event.preventDefault();

    const values = form.validate({ email, role });
    if (!values) return;

    try {
      await invite.mutateAsync(values);
      onInvited();
    } catch (err) {
      form.capture(err);
    }
  }

  return (
    <Card className="p-5">
      {form.message && (
        <div className="mb-4">
          <ErrorNotice
            title={
              form.failure?.isPlanLimit
                ? "No seats left on your plan"
                : "Could not send the invitation"
            }
            detail={form.message}
            traceId={form.failure?.traceId}
          />
        </div>
      )}

      <form onSubmit={onSubmit} className="grid gap-4 sm:grid-cols-3" noValidate>
        <div className="sm:col-span-2">
          <Field label="Email" htmlFor="invite-email" error={form.fields.email}>
            <TextInput
              id="invite-email"
              type="email"
              required
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              invalid={Boolean(form.fields.email)}
            />
          </Field>
        </div>

        <Field label="Role" htmlFor="invite-role" error={form.fields.role}>
          <Select id="invite-role" value={role} onChange={(e) => setRole(e.target.value as Role)}>
            {available.map((r) => (
              <option key={r} value={r}>
                {r}
              </option>
            ))}
          </Select>
        </Field>

        <div className="sm:col-span-3">
          <Button type="submit" busy={invite.isPending}>
            Send invitation
          </Button>
        </div>
      </form>
    </Card>
  );
}
