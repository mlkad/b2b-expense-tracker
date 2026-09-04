import { useCallback, useRef, useState } from "react";

import { ApiError } from "../api/client";
import type { Attachment, UploadTicket } from "../api/types";
import { Button, Card, ErrorNotice } from "./ui";
import { useSession } from "../auth/context";
import { useResource } from "../hooks/useResource";
import { formatBytes, sha256Base64 } from "../lib/checksum";
import { formatTimestamp } from "../lib/format";

/** What the server will accept. Mirrored here only to filter the picker. */
const ACCEPTED = "application/pdf,image/jpeg,image/png,image/webp,image/heic,image/tiff";
const MAX_BYTES = 25 * 1024 * 1024;

type Stage = "idle" | "hashing" | "uploading" | "recording";

const STAGE_LABEL: Record<Stage, string> = {
  idle: "",
  hashing: "Checking the file…",
  uploading: "Uploading…",
  recording: "Recording…",
};

export function Receipts({
  expenseId,
  canAttach,
  canDelete,
}: {
  expenseId: string;
  canAttach: boolean;
  canDelete: boolean;
}) {
  const { api } = useSession();
  const input = useRef<HTMLInputElement>(null);

  const [stage, setStage] = useState<Stage>("idle");
  const [error, setError] = useState<string | null>(null);

  const fetchAttachments = useCallback(
    async (key: string) => {
      try {
        const result = await api.get<{ items: Attachment[] }>(`/expenses/${key}/attachments`);
        return result.items;
      } catch (err) {
        // 501 means this deployment has no object store configured. That is a
        // fact about the environment rather than a fault, so the panel simply
        // shows nothing instead of an error the reader cannot act on.
        if (err instanceof ApiError && err.status === 501) return [];
        throw err;
      }
    },
    [api],
  );

  const { data: items, error: loadError, reload } = useResource(expenseId, fetchAttachments);
  const load = reload;

  /**
   * The upload, in three steps.
   *
   * The bytes go straight to the object store, not through the API. A receipt
   * is up to 25 MiB and an API that proxied it would hold that much per
   * concurrent upload and spend its request deadline on the client's
   * connection speed.
   *
   * The digest is computed here because the store verifies against it, and it
   * has to be the digest of the bytes actually sent.
   */
  async function upload(file: File) {
    setError(null);

    if (file.size > MAX_BYTES) {
      setError(`That file is ${formatBytes(file.size)}. The limit is ${formatBytes(MAX_BYTES)}.`);
      return;
    }

    try {
      setStage("hashing");
      const checksum = await sha256Base64(file);

      const declaration = {
        filename: file.name,
        content_type: file.type,
        size_bytes: file.size,
        checksum_sha256: checksum,
      };

      const ticket = await api.post<UploadTicket>(`/expenses/${expenseId}/attachments/presign`, declaration);

      setStage("uploading");
      const response = await fetch(ticket.upload.url, {
        method: ticket.upload.method,
        headers: ticket.upload.headers,
        body: file,
        // No cookies to the object store: the presigned URL is the entire
        // authorisation, and sending credentials to a third-party host would
        // be handing them somewhere they are not needed.
        credentials: "omit",
      });

      // The body is read even though nothing needs it. Leaving a response
      // stream unconsumed keeps the connection open until it is collected, and
      // the browser reports the finished request as aborted - which shows up
      // in a network log as a failed upload that plainly succeeded.
      const detail = await response.text();

      if (!response.ok) {
        // The store refusing means the bytes disagree with what was declared -
        // the file changed on disk between the digest and the read, usually.
        // The store's own reason, when it gave one: a checksum mismatch says
        // the bytes on disk changed between hashing and reading, which is
        // worth telling the user rather than hiding behind a status code.
        const reason = /<Code>([^<]+)<\/Code>/.exec(detail)?.[1];
        throw new Error(
          reason === "XAmzContentChecksumMismatch"
            ? "The file changed while it was uploading. Try again."
            : `The storage service refused the upload (${response.status}).`,
        );
      }

      setStage("recording");
      await api.post<Attachment>(`/expenses/${expenseId}/attachments`, {
        ...declaration,
        object_key: ticket.object_key,
      });

      load();
    } catch (err) {
      if (err instanceof ApiError) setError(err.fields[0]?.detail ?? err.message);
      else if (err instanceof Error) setError(err.message);
      else setError("The upload did not complete.");
    } finally {
      setStage("idle");
      // Cleared so the same file can be chosen again after a failure; a file
      // input does not fire change when the selection has not changed.
      if (input.current) input.current.value = "";
    }
  }

  async function remove(id: string) {
    setError(null);
    try {
      await api.delete(`/attachments/${id}`);
      load();
    } catch (err) {
      if (err instanceof ApiError) setError(err.message);
    }
  }

  const busy = stage !== "idle";

  return (
    <Card className="p-5">
      <div className="mb-3 flex items-center justify-between">
        <h2 className="text-sm font-medium text-ink-800">Receipts</h2>
        {canAttach && (
          <>
            <input
              ref={input}
              type="file"
              accept={ACCEPTED}
              className="sr-only"
              id="receipt-input"
              onChange={(e) => {
                const file = e.target.files?.[0];
                if (file) void upload(file);
              }}
            />
            {/* A label rather than a button clicking a hidden input: the label
                is what makes the control reachable by keyboard and announced
                properly, without scripting the click. */}
            <label
              htmlFor="receipt-input"
              className={`inline-flex cursor-pointer items-center rounded-md border border-ink-200 bg-white px-3 py-1.5 text-sm font-medium hover:bg-ink-50 ${
                busy ? "pointer-events-none opacity-60" : ""
              }`}
            >
              {busy ? STAGE_LABEL[stage] : "Attach"}
            </label>
          </>
        )}
      </div>

      {(error ?? loadError) && (
        <div className="mb-3">
          <ErrorNotice
            title={error ? "Could not attach that file" : "Could not load the receipts"}
            detail={error ?? loadError?.message}
            traceId={loadError?.traceId}
          />
        </div>
      )}

      {items === null ? (
        <p className="text-sm text-ink-600">Loading…</p>
      ) : items.length === 0 ? (
        <p className="text-sm text-ink-600">
          {canAttach
            ? "None yet. PDFs and photographs up to 25 MiB."
            : "None attached."}
        </p>
      ) : (
        <ul className="flex flex-col divide-y divide-ink-100">
          {items.map((file) => (
            <li key={file.id} className="flex items-center gap-3 py-2.5">
              <div className="min-w-0 flex-1">
                {/* A plain link the browser navigates to: the API answers 302
                    with a short-lived signed URL, and following it in a new tab
                    keeps the redirect out of this page's history. */}
                <a
                  href={api.url(`/attachments/${file.id}/download`)}
                  target="_blank"
                  rel="noreferrer"
                  className="block truncate text-sm font-medium text-brand-600 hover:underline"
                >
                  {file.filename}
                </a>
                <p className="text-xs text-ink-600">
                  {formatBytes(file.size_bytes)} · {formatTimestamp(file.created_at)}
                </p>
              </div>
              {canDelete && (
                <Button variant="ghost" onClick={() => void remove(file.id)}>
                  Remove
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}

      {items !== null && items.length > 0 && (
        <p className="mt-3 text-xs text-ink-600">
          A receipt on a submitted claim is evidence and cannot be removed.
        </p>
      )}
    </Card>
  );
}
