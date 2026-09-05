import { useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";

import { attachmentsQuery } from "@/entities/expense";
import {
  ACCEPTED_TYPES,
  STAGE_LABEL,
  useDeleteReceipt,
  useUploadReceipt,
  type UploadStage,
} from "@/features/receipt-upload";
import { api, ApiError } from "@/shared/api";
import { formatBytes } from "@/shared/lib/checksum";
import { formatTimestamp } from "@/shared/lib/format";
import { Button, Card, ErrorNotice } from "@/shared/ui/kit";

function messageFor(err: unknown): string | null {
  if (err === null || err === undefined) return null;
  if (err instanceof ApiError) return err.fields[0]?.detail ?? err.message;
  if (err instanceof Error) return err.message;
  return "The upload did not complete.";
}

export function ReceiptPanel({
  expenseId,
  canAttach,
  canDelete,
}: {
  expenseId: string;
  canAttach: boolean;
  canDelete: boolean;
}) {
  const input = useRef<HTMLInputElement>(null);
  const [stage, setStage] = useState<UploadStage>("idle");

  const upload = useUploadReceipt(expenseId, setStage);
  const remove = useDeleteReceipt(expenseId);

  const attachments = useQuery({
    ...attachmentsQuery(expenseId),
    // 501 means this deployment has no object store configured. That is a fact
    // about the environment rather than a fault, so the panel shows nothing
    // instead of an error the reader cannot act on.
    retry: (_, err) => !(err instanceof ApiError) || err.status >= 500,
  });

  const unconfigured =
    attachments.error instanceof ApiError && attachments.error.status === 501;
  const items = unconfigured ? [] : attachments.data;

  const failure = messageFor(upload.error) ?? messageFor(remove.error);
  const loadFailure = unconfigured ? null : attachments.error;
  const busy = stage !== "idle";

  return (
    <Card className="p-5">
      <div className="mb-3 flex items-center justify-between">
        <h2 className="text-sm font-medium text-fg">Receipts</h2>
        {canAttach && (
          <>
            <input
              ref={input}
              type="file"
              accept={ACCEPTED_TYPES}
              className="sr-only"
              id="receipt-input"
              onChange={(e) => {
                const file = e.target.files?.[0];
                if (!file) return;
                upload.mutate(file, {
                  // Cleared so the same file can be chosen again after a
                  // failure; a file input does not fire change when the
                  // selection has not changed.
                  onSettled: () => {
                    if (input.current) input.current.value = "";
                  },
                });
              }}
            />
            {/* A label rather than a button clicking a hidden input: the label
                is what makes the control reachable by keyboard and announced
                properly, without scripting the click. */}
            <label
              htmlFor="receipt-input"
              className={`inline-flex cursor-pointer items-center rounded-md border border-line bg-surface px-3 py-1.5 text-sm font-medium hover:bg-elevated ${
                busy ? "pointer-events-none opacity-60" : ""
              }`}
            >
              {busy ? STAGE_LABEL[stage] : "Attach"}
            </label>
          </>
        )}
      </div>

      {(failure ?? loadFailure) && (
        <div className="mb-3">
          <ErrorNotice
            title={failure ? "Could not attach that file" : "Could not load the receipts"}
            detail={failure ?? loadFailure?.message}
            traceId={loadFailure instanceof ApiError ? loadFailure.traceId : undefined}
          />
        </div>
      )}

      {items === undefined ? (
        <p className="text-sm text-muted">Loading…</p>
      ) : items.length === 0 ? (
        <p className="text-sm text-muted">
          {canAttach ? "None yet. PDFs and photographs up to 25 MiB." : "None attached."}
        </p>
      ) : (
        <>
          <ul className="flex flex-col divide-y divide-line-soft">
            {items.map((file) => (
              <li key={file.id} className="flex items-center gap-3 py-2.5">
                <div className="min-w-0 flex-1">
                  {/* A plain link the browser navigates to: the API answers 302
                      with a short-lived signed URL, and following it in a new
                      tab keeps the redirect out of this page's history. */}
                  <a
                    href={api.url(`/attachments/${file.id}/download`)}
                    target="_blank"
                    rel="noreferrer"
                    className="block truncate text-sm font-medium text-accent hover:underline"
                  >
                    {file.filename}
                  </a>
                  <p className="text-xs text-muted">
                    {formatBytes(file.size_bytes)} · {formatTimestamp(file.created_at)}
                  </p>
                </div>
                {canDelete && (
                  <Button variant="ghost" onClick={() => remove.mutate(file.id)}>
                    Remove
                  </Button>
                )}
              </li>
            ))}
          </ul>
          <p className="mt-3 text-xs text-muted">
            A receipt on a submitted claim is evidence and cannot be removed.
          </p>
        </>
      )}
    </Card>
  );
}
