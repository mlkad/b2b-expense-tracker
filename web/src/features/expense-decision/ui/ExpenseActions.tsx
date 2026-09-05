import { useState } from "react";

import type { ExpenseAction } from "@/entities/expense";
import { ApiError } from "@/shared/api";
import { actionLabel } from "@/shared/lib/format";
import { Button, ErrorNotice, Field, TextInput } from "@/shared/ui/kit";
import { Modal } from "@/shared/ui/Modal";

import { useExpenseAction } from "../model/mutations";

/**
 * The two actions that need something typed, and what the server calls it.
 *
 * Rejecting without a reason generates a support ticket and a resubmission of
 * the identical claim; settling without a reference produces a paid row that
 * cannot be reconciled against a bank statement. The server refuses both, and
 * this is what asks rather than letting the user find out by being refused.
 */
const PROMPTS: Partial<
  Record<ExpenseAction, { field: "reason" | "payment_ref"; label: string; hint: string }>
> = {
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

function variantFor(action: ExpenseAction) {
  if (action === "reject") return "danger" as const;
  if (action === "approve" || action === "pay") return "primary" as const;
  return "secondary" as const;
}

export function ExpenseActions({
  id,
  actions,
  children,
}: {
  id: string;
  /**
   * What this caller may do to this claim right now, as computed by the state
   * machine on the server. The dashboard renders one button per entry and does
   * not decide for itself - a second copy of the transition rules in TypeScript
   * would drift, and the symptom would be a button that 403s.
   */
  actions: ExpenseAction[];
  /** Anything else that belongs on the action row, such as an edit link. */
  children?: React.ReactNode;
}) {
  const decide = useExpenseAction(id);

  const [pending, setPending] = useState<ExpenseAction | null>(null);
  const [input, setInput] = useState("");

  const error = decide.error instanceof ApiError ? decide.error : null;
  const prompt = pending ? PROMPTS[pending] : undefined;

  function close() {
    setPending(null);
    setInput("");
  }

  async function run(action: ExpenseAction, value?: string) {
    try {
      await decide.mutateAsync({ action, value });
      close();
    } catch (err) {
      if (!(err instanceof ApiError)) throw err;
      // The message is rendered from the mutation's own error state; a
      // conflict has already refreshed the claim through onSettled, so the
      // buttons below now reflect what actually happened.
      if (!PROMPTS[action]) close();
    }
  }

  return (
    <>
      {error && (
        <ErrorNotice
          title={error.isConflict ? "Somebody else changed this claim" : "That did not work"}
          detail={error.message}
          traceId={error.traceId}
        />
      )}

      {actions.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {actions.map((action) => (
            <Button
              key={action}
              variant={variantFor(action)}
              busy={decide.isPending && pending === action}
              onClick={() => {
                if (PROMPTS[action] || CONFIRM[action]) {
                  setPending(action);
                  return;
                }
                void run(action);
              }}
            >
              {actionLabel(action)}
            </Button>
          ))}
          {children}
        </div>
      )}

      <Modal open={pending !== null} title={pending ? actionLabel(pending) : ""} onClose={close}>
        {pending && CONFIRM[pending] && <p className="text-sm text-ink-600">{CONFIRM[pending]}</p>}

        {prompt && (
          <Field
            label={prompt.label}
            htmlFor="action-input"
            hint={prompt.hint}
            error={error?.fieldError(prompt.field)}
          >
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
          <Button variant="ghost" onClick={close}>
            Cancel
          </Button>
          <Button
            variant={pending === "reject" ? "danger" : "primary"}
            busy={decide.isPending}
            disabled={Boolean(prompt) && input.trim() === ""}
            onClick={() => pending && void run(pending, input.trim() || undefined)}
          >
            {pending ? actionLabel(pending) : "Confirm"}
          </Button>
        </div>
      </Modal>
    </>
  );
}
