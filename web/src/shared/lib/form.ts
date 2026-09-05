import { useCallback, useMemo, useState } from "react";
import type { z } from "zod";

import { ApiError } from "@/shared/api";

/** Field name to message, in the shape a Field component reads. */
export type FieldErrors = Record<string, string | undefined>;

function issuesToFields(error: z.ZodError): FieldErrors {
  const fields: FieldErrors = {};
  for (const issue of error.issues) {
    const name = issue.path.join(".");
    // The first message per field. Showing "required" and "too short" one
    // above the other tells the reader nothing the first line did not.
    fields[name] ??= issue.message;
  }
  return fields;
}

/**
 * Validates a form locally, then keeps whatever the server rejected.
 *
 * Both halves are needed and neither replaces the other. The schema catches an
 * empty field without a round trip; the server catches what only it can know -
 * that the email is taken, that the amount is over an approval limit - and its
 * messages are keyed by field for exactly this reason.
 *
 * Local errors are cleared on the next submit rather than on every keystroke.
 * Re-validating as someone types marks a half-typed email invalid before they
 * have finished writing it.
 */
export function useFormErrors<T>(schema: z.ZodType<T>) {
  const [local, setLocal] = useState<FieldErrors>({});
  const [failure, setFailure] = useState<ApiError | null>(null);

  const validate = useCallback(
    (values: unknown): T | null => {
      const result = schema.safeParse(values);
      setFailure(null);
      setLocal(result.success ? {} : issuesToFields(result.error));
      return result.success ? result.data : null;
    },
    [schema],
  );

  const capture = useCallback((err: unknown) => {
    // Anything that is not an ApiError is a bug rather than a response, and
    // rethrowing puts it in front of a developer instead of rendering a form
    // that looks like it merely failed to save.
    if (!(err instanceof ApiError)) throw err;
    setFailure(err);
  }, []);

  const fields = useMemo<FieldErrors>(() => {
    const merged: FieldErrors = { ...local };
    for (const field of failure?.fields ?? []) merged[field.field] ??= field.detail;
    return merged;
  }, [local, failure]);

  return {
    fields,
    failure,
    validate,
    capture,
    /** The banner message, when the failure was not about one field. */
    message: failure && (failure.fields.length === 0 ? failure.message : null),
    reset: useCallback(() => {
      setLocal({});
      setFailure(null);
    }, []),
  };
}
