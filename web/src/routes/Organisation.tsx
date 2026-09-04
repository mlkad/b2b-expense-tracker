import { useCallback, useState, type FormEvent } from "react";

import { ApiError } from "../api/client";
import type { Department, Member, Role, VendorSubscription } from "../api/types";
import { Badge, Button, Card, EmptyState, ErrorNotice, Field, Select, SkeletonRows, TextInput } from "../components/ui";
import { useSession } from "../auth/context";
import { useResource } from "../hooks/useResource";
import { formatDate, sentenceCase } from "../lib/format";
import { formatMinor, formatMoney } from "../lib/money";

const ROLES: Role[] = ["admin", "finance", "manager", "member", "viewer"];

export function Organisation() {
  const [tab, setTab] = useState<"members" | "departments" | "subscriptions">("members");

  return (
    <div className="flex flex-col gap-6">
      <h1 className="text-lg font-semibold">Organisation</h1>

      {/* Real tabs: the roles and arrow-key handling are what let a keyboard
          user move between panels the way they expect, rather than tabbing
          through every control in the hidden ones. */}
      <div role="tablist" aria-label="Organisation sections" className="flex gap-1 border-b border-ink-100">
        {(["members", "departments", "subscriptions"] as const).map((key) => (
          <button
            key={key}
            role="tab"
            type="button"
            aria-selected={tab === key}
            aria-controls={`panel-${key}`}
            id={`tab-${key}`}
            onClick={() => setTab(key)}
            className={`-mb-px border-b-2 px-3 py-2 text-sm ${
              tab === key
                ? "border-brand-600 font-medium text-ink-900"
                : "border-transparent text-ink-600 hover:text-ink-900"
            }`}
          >
            {sentenceCase(key)}
          </button>
        ))}
      </div>

      <div role="tabpanel" id={`panel-${tab}`} aria-labelledby={`tab-${tab}`}>
        {tab === "members" && <Members />}
        {tab === "departments" && <Departments />}
        {tab === "subscriptions" && <Subscriptions />}
      </div>
    </div>
  );
}

function Members() {
  const { api, profile } = useSession();
  const [inviting, setInviting] = useState(false);

  const fetchMembers = useCallback(() => api.get<{ items: Member[] }>("/members"), [api]);
  const { data, error, initial, reload } = useResource("members", fetchMembers);

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between">
        <p className="text-sm text-ink-600">
          You can only invite and administer members below your own role.
        </p>
        <Button onClick={() => setInviting((open) => !open)}>{inviting ? "Cancel" : "Invite"}</Button>
      </div>

      {error && <ErrorNotice title="Could not load the members" detail={error.message} traceId={error.traceId} />}

      {inviting && (
        <InviteMember
          actorRole={profile?.role}
          onInvited={() => {
            setInviting(false);
            reload();
          }}
        />
      )}

      <Card>
        {initial ? (
          <SkeletonRows rows={4} columns={4} />
        ) : (data?.items.length ?? 0) === 0 ? (
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
              {(data?.items ?? []).map((member) => (
                <tr key={member.id}>
                  <td className="px-4 py-3">
                    <span className="font-medium">{member.full_name || member.email}</span>
                    {member.full_name && <span className="ml-2 text-xs text-ink-600">{member.email}</span>}
                    {member.id === profile?.membership_id && (
                      <span className="ml-2 text-xs text-ink-600">(you)</span>
                    )}
                  </td>
                  <td className="px-4 py-3"><Badge tone="brand">{member.role}</Badge></td>
                  <td className="px-4 py-3 text-ink-600">{member.department_name ?? "—"}</td>
                  <td className="px-4 py-3">
                    <Badge tone={member.status === "active" ? "positive" : member.status === "invited" ? "caution" : "danger"}>
                      {member.status}
                    </Badge>
                  </td>
                  <td className="px-4 py-3 text-right tabular-nums text-ink-600">
                    {member.approval_limit_minor == null
                      ? "role default"
                      : formatMinor(member.approval_limit_minor, profile?.default_currency ?? "USD")}
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

function InviteMember({ actorRole, onInvited }: { actorRole?: Role; onInvited: () => void }) {
  const { api } = useSession();
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<Role>("member");
  const [error, setError] = useState<ApiError | null>(null);
  const [busy, setBusy] = useState(false);

  // Ranks, most privileged first, matching the server's enum. Offering only
  // roles below the caller's own avoids a 403 they cannot do anything about -
  // the server refuses the rest regardless, since inviting a peer is one step
  // from inviting a superior.
  const RANK: Role[] = ["owner", "admin", "finance", "manager", "member", "viewer"];
  const actorRank = actorRole ? RANK.indexOf(actorRole) : RANK.length;
  const available = ROLES.filter((r) => RANK.indexOf(r) > actorRank);

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.post("/members", { email, role, department_id: null, approval_limit_minor: null });
      onInvited();
    } catch (err) {
      if (err instanceof ApiError) setError(err);
      else throw err;
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card className="p-5">
      {error && (
        <div className="mb-4">
          <ErrorNotice
            title={error.isPlanLimit ? "No seats left on your plan" : "Could not send the invitation"}
            detail={error.message}
            traceId={error.traceId}
          />
        </div>
      )}

      <form onSubmit={onSubmit} className="grid gap-4 sm:grid-cols-3" noValidate>
        <div className="sm:col-span-2">
          <Field label="Email" htmlFor="invite-email" error={error?.fieldError("email")}>
            <TextInput
              id="invite-email"
              type="email"
              required
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              invalid={Boolean(error?.fieldError("email"))}
            />
          </Field>
        </div>

        <Field label="Role" htmlFor="invite-role" error={error?.fieldError("role")}>
          <Select id="invite-role" value={role} onChange={(e) => setRole(e.target.value as Role)}>
            {available.map((r) => (
              <option key={r} value={r}>
                {r}
              </option>
            ))}
          </Select>
        </Field>

        <div className="sm:col-span-3">
          <Button type="submit" busy={busy}>
            Send invitation
          </Button>
        </div>
      </form>
    </Card>
  );
}

function Departments() {
  const { api } = useSession();
  const [name, setName] = useState("");
  const [error, setError] = useState<ApiError | null>(null);
  const [busy, setBusy] = useState(false);

  const fetchDepartments = useCallback(() => api.get<{ items: Department[] }>("/departments"), [api]);
  const { data, error: loadError, initial, reload } = useResource("departments", fetchDepartments);

  async function create(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.post("/departments", { name });
      setName("");
      reload();
    } catch (err) {
      if (err instanceof ApiError) setError(err);
      else throw err;
    } finally {
      setBusy(false);
    }
  }

  async function archive(id: string) {
    try {
      await api.delete(`/departments/${id}`);
      reload();
    } catch (err) {
      if (err instanceof ApiError) setError(err);
    }
  }

  return (
    <div className="flex flex-col gap-4">
      {(error ?? loadError) && (
        <ErrorNotice
          title={error?.isPlanLimit ? "No departments left on your plan" : "Could not update the departments"}
          detail={(error ?? loadError)?.message}
          traceId={(error ?? loadError)?.traceId}
        />
      )}

      <Card className="p-5">
        <form onSubmit={create} className="flex items-end gap-3" noValidate>
          <div className="flex-1">
            <Field label="New department" htmlFor="dept-name" error={error?.fieldError("name")}>
              <TextInput id="dept-name" required value={name} onChange={(e) => setName(e.target.value)} />
            </Field>
          </div>
          <Button type="submit" busy={busy}>
            Add
          </Button>
        </form>
      </Card>

      <Card>
        {initial ? (
          <SkeletonRows rows={3} columns={2} />
        ) : (data?.items.length ?? 0) === 0 ? (
          <EmptyState title="No departments" detail="Claims can be filed without one, but budgets need them." />
        ) : (
          <ul className="divide-y divide-ink-100">
            {(data?.items ?? []).map((department) => (
              <li key={department.id} className="flex items-center justify-between px-4 py-3">
                <span className="text-sm font-medium">{department.name}</span>
                <Button variant="ghost" onClick={() => void archive(department.id)}>
                  Archive
                </Button>
              </li>
            ))}
          </ul>
        )}
      </Card>

      <p className="text-xs text-ink-600">
        Archiving retires a department without deleting it. Claims filed against it stay attributable,
        which is the point of having had one.
      </p>
    </div>
  );
}

function Subscriptions() {
  const { api, profile } = useSession();

  const fetchSubs = useCallback(
    () =>
      api.get<{ items: VendorSubscription[]; annualised_total_minor: number; currency: string }>(
        "/vendor-subscriptions",
      ),
    [api],
  );
  const { data, error, initial } = useResource("vendor-subscriptions", fetchSubs);

  const currency = data?.currency || profile?.default_currency || "USD";

  return (
    <div className="flex flex-col gap-4">
      <p className="text-sm text-ink-600">
        The recurring software your organisation pays for. Due charges become draft claims
        automatically, so nothing renews unnoticed.
      </p>

      {error && (
        <ErrorNotice
          title={error.status === 403 ? "Your plan does not include this" : "Could not load the subscriptions"}
          detail={error.message}
          traceId={error.traceId}
        />
      )}

      {data && data.items.length > 0 && (
        <Card className="p-5">
          <p className="text-xs uppercase tracking-wide text-ink-600">Annualised, active only</p>
          <p className="mt-1 text-2xl font-semibold tabular-nums">
            {formatMinor(data.annualised_total_minor, currency)}
          </p>
        </Card>
      )}

      <Card>
        {initial ? (
          <SkeletonRows rows={3} columns={4} />
        ) : (data?.items.length ?? 0) === 0 ? (
          <EmptyState title="Nothing tracked yet" />
        ) : (
          <table className="w-full text-sm">
            <caption className="sr-only">Tracked vendor subscriptions</caption>
            <thead>
              <tr className="border-b border-ink-100 text-left text-xs uppercase tracking-wide text-ink-600">
                <th scope="col" className="px-4 py-2.5 font-medium">Vendor</th>
                <th scope="col" className="px-4 py-2.5 font-medium">Cadence</th>
                <th scope="col" className="px-4 py-2.5 font-medium">Next charge</th>
                <th scope="col" className="px-4 py-2.5 text-right font-medium">Amount</th>
                <th scope="col" className="px-4 py-2.5 text-right font-medium">Per year</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-ink-100">
              {(data?.items ?? []).map((sub) => (
                <tr key={sub.id}>
                  <td className="px-4 py-3">
                    <span className="font-medium">{sub.vendor}</span>
                    {sub.plan_name && <span className="ml-2 text-xs text-ink-600">{sub.plan_name}</span>}
                  </td>
                  <td className="px-4 py-3 text-ink-600">{sub.cadence}</td>
                  <td className="px-4 py-3 text-ink-600">{formatDate(sub.next_charge_on)}</td>
                  <td className="px-4 py-3 text-right tabular-nums">{formatMoney(sub.amount)}</td>
                  <td className="px-4 py-3 text-right font-medium tabular-nums">
                    {formatMinor(sub.annualised_minor, currency)}
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
