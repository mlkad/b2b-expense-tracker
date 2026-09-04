import { useCallback, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";

import { ApiError } from "../api/client";
import type { Expense, ExpenseAction, ExpenseEventRecord } from "../api/types";
import { Button, Card, ErrorNotice, Field, SkeletonRows, TextInput } from "../components/ui";
import { Modal } from "../components/Modal";
import { Receipts } from "../components/Receipts";
import { StatusBadge } from "../components/StatusBadge";
import { useResource } from "../hooks/useResource";
import { useSession } from "../auth/context";
import { actionLabel, formatDate, formatTimestamp, sentenceCase } from "../lib/format";
import { formatMoney } from "../lib/money";

/**
 * The two actions that need something typed, and what the server calls it.
 *
 * Rejecting without a reason generates a support ticket and a resubmission of
 * the identical claim; settling without a reference produces a paid row that
 * cannot be reconciled against a bank statement. The server refuses both, and
 * this is what asks rather than letting the user find out by being refused.
 */
const PROMPTS: Partial<Record<ExpenseAction, { field: "reason" | "payment_ref"; label: string; hint: string }>> = {
  reject: {
    field: "reason",
    label: "Why is it being rejected?",
    hint: "The person who filed it sees this, and it is what tells them what to change.",
  },
  pay: {
    field: "payment_ref",
    label: "Payment reference",
    hint: "The bank reference or payment run this was settled in, so it can be reconciled later.",
  },
};

/** Actions whose consequence is not undoable get a second look. */
const CONFIRM: Partial<Record<ExpenseAction, string>> = {
  pay: "Settling a claim is final - there is no transition out of paid.",
  approve: "An approval cannot be reversed; a mistake is corrected with a compensating claim.",
};

export function ExpenseDetail() {
  const { id = "" } = useParams();
  const { api, profile } = useSession();
  const navigate = useNavigate();

  const [pending, setPending] = useState<ExpenseAction | null>(null);
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState<ApiError | null>(null);

  // Both in one fetcher, so the claim and its ledger always describe the same
  // moment. Two independent requests would let a transition land between them
  // and show a history that does not match the status above it.
  const fetchClaim = useCallback(
    async (key: string) => {
      const [detail, events] = await Promise.all([
        api.get<Expense>(`/expenses/${key}`),
        api.get<{ items: ExpenseEventRecord[] }>(`/expenses/${key}/history`),
      ]);
      return { detail, history: events.items };
    },
    [api],
  );

  const { data, error: loadError, initial, reload } = useResource(id, fetchClaim);

  const claim = data?.detail ?? null;
  const history = data?.history ?? [];
  const error = actionError ?? loadError;

  const run = useCallback(
    async (action: ExpenseAction, value?: string) => {
      setBusy(true);
      try {
        const prompt = PROMPTS[action];
        const body = prompt && value ? { [prompt.field]: value } : undefined;
        await api.post<Expense>(`/expenses/${id}/${action}`, body);

        // Reloaded rather than patched from the response. The transition may
        // have changed the revision, the timestamps and what is allowed next,
        // and the ledger has gained a row - reconstructing all of that on the
        // client is a second copy of rules the server already applied.
        reload();
        setPending(null);
        setInput("");
        setActionError(null);
      } catch (err) {
        if (err instanceof ApiError) {
          setActionError(err);
          // A 409 means somebody else moved it. What is on screen is stale, so
          // it is reloaded to show what actually happened rather than leaving
          // buttons that will fail the same way.
          if (err.isConflict) reload();
          setPending(null);
        } else {
          throw err;
        }
      } finally {
        setBusy(false);
      }
    },
    [api, id, reload],
  );

  const onAction = useCallback(
    (action: ExpenseAction) => {
      if (PROMPTS[action] || CONFIRM[action]) {
        setPending(action);
        return;
      }
      void run(action);
    },
    [run],
  );

  if (initial) {
    return <Card><SkeletonRows rows={5} columns={2} /></Card>;
  }

  if (!claim) {
    return (
      <ErrorNotice
        title="That claim is not available"
        detail={error?.message ?? "It may have been deleted, or it belongs to another organisation."}
        traceId={error?.traceId}
      />
    );
  }

  const actions = claim.allowed_actions ?? [];
  const prompt = pending ? PROMPTS[pending] : undefined;
  const mine = claim.submitter_id === profile?.membership_id;

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <Link to="/expenses" className="text-sm text-brand-600 hover:underline">
            ← All claims
          </Link>
          <h1 className="mt-1 flex items-center gap-3 text-lg font-semibold">
            {claim.merchant}
            <StatusBadge status={claim.status} />
          </h1>
        </div>
        <p className="text-2xl font-semibold tabular-nums">{formatMoney(claim.amount)}</p>
      </div>

      {error && (
        <ErrorNotice
          title={error.isConflict ? "Somebody else changed this claim" : "That did not work"}
          detail={error.message}
          traceId={error.traceId}
        />
      )}

      {actions.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {/* One button per action the server says is allowed. The dashboard
              does not decide - a second copy of the state machine here would
              drift, and the symptom would be a button that 403s. */}
          {actions.map((action) => (
            <Button
              key={action}
              variant={action === "reject" ? "danger" : action === "approve" || action === "pay" ? "primary" : "secondary"}
              onClick={() => onAction(action)}
              busy={busy && pending === action}
            >
              {actionLabel(action)}
            </Button>
          ))}
          {mine && claim.status === "draft" && (
            <Link
              to={`/expenses/${claim.id}/edit`}
              className="inline-flex items-center rounded-md border border-ink-200 bg-white px-3.5 py-2 text-sm font-medium hover:bg-ink-50"
            >
              Edit
            </Link>
          )}
        </div>
      )}

      <div className="grid gap-6 lg:grid-cols-3">
        <Card className="p-5 lg:col-span-2">
          <h2 className="mb-4 text-sm font-medium text-ink-800">Details</h2>
          <dl className="grid gap-x-6 gap-y-3 sm:grid-cols-2">
            <Detail term="Spent on" value={formatDate(claim.spent_at)} />
            <Detail term="Category" value={sentenceCase(claim.category)} />
            <Detail term="Submitted" value={formatTimestamp(claim.submitted_at)} />
            <Detail term="Decided" value={formatTimestamp(claim.decided_at)} />
            {claim.paid_at && <Detail term="Paid" value={formatTimestamp(claim.paid_at)} />}
            {claim.payment_ref && <Detail term="Payment reference" value={claim.payment_ref} />}
            {claim.revision > 1 && <Detail term="Revision" value={String(claim.revision)} />}
          </dl>

          {claim.description && (
            <>
              <h3 className="mt-5 mb-1 text-xs font-medium uppercase tracking-wide text-ink-600">Description</h3>
              <p className="text-sm">{claim.description}</p>
            </>
          )}

          {claim.decision_note && (
            <div className="mt-5 border-l-2 border-ink-200 pl-3">
              <h3 className="mb-1 text-xs font-medium uppercase tracking-wide text-ink-600">Decision note</h3>
              <p className="text-sm">{claim.decision_note}</p>
            </div>
          )}
        </Card>

        <div className="flex flex-col gap-6">
        <Receipts
          expenseId={claim.id}
          // The server refuses both anyway - attaching to a submitted claim,
          // and removing a receipt from one - so this only avoids offering a
          // control that would be refused.
          canAttach={mine && claim.status === "draft"}
          canDelete={mine && claim.status === "draft"}
        />

        <Card className="p-5">
          <h2 className="mb-4 text-sm font-medium text-ink-800">History</h2>
          {history.length === 0 ? (
            <p className="text-sm text-ink-600">Nothing recorded yet.</p>
          ) : (
            <ol className="flex flex-col gap-3">
              {history.map((event) => (
                <li key={event.id} className="border-l-2 border-ink-100 pl-3">
                  <p className="text-sm font-medium">{sentenceCase(event.action)}</p>
                  <p className="text-xs text-ink-600">
                    {formatTimestamp(event.occurred_at)}
                    {event.actor_email ? ` · ${event.actor_email}` : " · system"}
                  </p>
                  {/* The amount as it was at the time, not today's. An audit
                      row saying "approved" without saying what was approved is
                      useless once the claim has been revised. */}
                  <p className="text-xs text-ink-600 tabular-nums">{formatMoney(event.amount)}</p>
                  {event.reason && <p className="mt-1 text-xs">{event.reason}</p>}
                </li>
              ))}
            </ol>
          )}
        </Card>
        </div>
      </div>

      <Modal
        open={pending !== null}
        title={pending ? actionLabel(pending) : ""}
        onClose={() => {
          setPending(null);
          setInput("");
        }}
      >
        {pending && CONFIRM[pending] && <p className="text-sm text-ink-600">{CONFIRM[pending]}</p>}

        {prompt && (
          <Field label={prompt.label} htmlFor="action-input" hint={prompt.hint} error={error?.fieldError(prompt.field)}>
            <TextInput
              id="action-input"
              autoFocus
              value={input}
              onChange={(e) => setInput(e.target.value)}
              invalid={Boolean(error?.fieldError(prompt.field))}
            />
          </Field>
        )}

        <div className="flex justify-end gap-2">
          <Button
            variant="ghost"
            onClick={() => {
              setPending(null);
              setInput("");
            }}
          >
            Cancel
          </Button>
          <Button
            variant={pending === "reject" ? "danger" : "primary"}
            busy={busy}
            disabled={Boolean(prompt) && input.trim() === ""}
            onClick={() => pending && void run(pending, input.trim() || undefined)}
          >
            {pending ? actionLabel(pending) : "Confirm"}
          </Button>
        </div>
      </Modal>

      {claim.status === "draft" && mine && (
        <DeleteClaim
          onDelete={async () => {
            await api.delete(`/expenses/${claim.id}`);
            void navigate("/expenses");
          }}
        />
      )}
    </div>
  );
}

function Detail({ term, value }: { term: string; value: string }) {
  return (
    <div>
      <dt className="text-xs uppercase tracking-wide text-ink-600">{term}</dt>
      <dd className="mt-0.5 text-sm">{value}</dd>
    </div>
  );
}

/**
 * Only a draft can be discarded, and only by the person who filed it. The
 * server enforces both - through a row-level security policy, not just a
 * check - and this only avoids offering a button that would be refused.
 */
function DeleteClaim({ onDelete }: { onDelete: () => Promise<void> }) {
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);

  return (
    <div className="flex items-center justify-between rounded-md border border-ink-100 bg-white px-4 py-3">
      <p className="text-sm text-ink-600">Discard this draft. It has not been submitted, so nothing is lost from the record.</p>
      <Button variant="danger" busy={busy} onClick={() => setConfirming(true)}>
        Discard
      </Button>

      <Modal open={confirming} title="Discard this draft?" onClose={() => setConfirming(false)}>
        <p className="text-sm text-ink-600">This cannot be undone.</p>
        <div className="flex justify-end gap-2">
          <Button variant="ghost" onClick={() => setConfirming(false)}>
            Keep it
          </Button>
          <Button
            variant="danger"
            busy={busy}
            onClick={async () => {
              setBusy(true);
              try {
                await onDelete();
              } finally {
                setBusy(false);
                setConfirming(false);
              }
            }}
          >
            Discard
          </Button>
        </div>
      </Modal>
    </div>
  );
}
