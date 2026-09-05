import { useMutation, useQueryClient } from "@tanstack/react-query";

import { api, decode } from "@/shared/api";
import { attachmentSchema, expenseKeys, uploadTicketSchema } from "@/entities/expense";
import { formatBytes, sha256Base64 } from "@/shared/lib/checksum";

/** What the server will accept. Mirrored here only to filter the picker. */
export const ACCEPTED_TYPES =
  "application/pdf,image/jpeg,image/png,image/webp,image/heic,image/tiff";
export const MAX_BYTES = 25 * 1024 * 1024;

export type UploadStage = "idle" | "hashing" | "uploading" | "recording";

export const STAGE_LABEL: Record<UploadStage, string> = {
  idle: "",
  hashing: "Checking the file…",
  uploading: "Uploading…",
  recording: "Recording…",
};

/**
 * The upload, in three steps.
 *
 * The bytes go straight to the object store, not through the API. A receipt is
 * up to 25 MiB and an API that proxied it would hold that much per concurrent
 * upload and spend its request deadline on the client's connection speed.
 *
 * The digest is computed here because the store verifies against it, and it has
 * to be the digest of the bytes actually sent.
 */
export function useUploadReceipt(expenseId: string, onStage: (stage: UploadStage) => void) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (file: File) => {
      if (file.size > MAX_BYTES) {
        throw new Error(
          `That file is ${formatBytes(file.size)}. The limit is ${formatBytes(MAX_BYTES)}.`,
        );
      }

      onStage("hashing");
      const checksum = await sha256Base64(file);

      const declaration = {
        filename: file.name,
        content_type: file.type,
        size_bytes: file.size,
        checksum_sha256: checksum,
      };

      const ticket = decode(
        uploadTicketSchema,
        await api.post(`/expenses/${expenseId}/attachments/presign`, declaration),
        "POST /expenses/:id/attachments/presign",
      );

      onStage("uploading");
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
      // the browser reports the finished request as aborted - which shows up in
      // a network log as a failed upload that plainly succeeded.
      const detail = await response.text();

      if (!response.ok) {
        // The store's own reason, when it gave one: a checksum mismatch says
        // the bytes on disk changed between hashing and reading, which is worth
        // telling the user rather than hiding behind a status code.
        const reason = /<Code>([^<]+)<\/Code>/.exec(detail)?.[1];
        throw new Error(
          reason === "XAmzContentChecksumMismatch"
            ? "The file changed while it was uploading. Try again."
            : `The storage service refused the upload (${response.status}).`,
        );
      }

      onStage("recording");
      return decode(
        attachmentSchema,
        await api.post(`/expenses/${expenseId}/attachments`, {
          ...declaration,
          object_key: ticket.object_key,
        }),
        "POST /expenses/:id/attachments",
      );
    },

    onSettled: async () => {
      onStage("idle");
      await queryClient.invalidateQueries({ queryKey: expenseKeys.attachments(expenseId) });
    },
  });
}

export function useDeleteReceipt(expenseId: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (id: string) => {
      await api.delete(`/attachments/${id}`);
    },
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: expenseKeys.attachments(expenseId) }),
  });
}
