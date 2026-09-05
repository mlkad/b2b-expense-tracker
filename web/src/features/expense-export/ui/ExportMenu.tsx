import { useState } from "react";
import { z } from "zod";

import { api, ApiError, decode } from "@/shared/api";
import { ChevronDownIcon } from "@/shared/ui/icons";

const ticketSchema = z.object({ url: z.string() });

const FORMATS = ["csv", "xlsx", "pdf"] as const;

/**
 * Exports are a signed link the browser navigates to, obtained on click.
 *
 * Not a fetch: a streamed report can be tens of megabytes, and pulling it into
 * a Blob to trigger a download would hold the whole thing in the tab - the cost
 * the server went to some trouble to avoid. Letting the browser navigate hands
 * it to the download manager, which streams to disk.
 *
 * But a navigation cannot carry an Authorization header, so a plain <a> to the
 * export route arrives with no credential at all and is refused. An earlier
 * version of this component did exactly that, and the buttons did nothing but
 * produce a 401 page.
 *
 * So the click first asks the API for a link signed for this exact query. The
 * token lives a minute and is bound to the filters, which is what stops it
 * being edited in the address bar into a report of the whole organisation.
 */
export function ExportMenu({
  /**
   * Builds the query to sign, for one format.
   *
   * Passed in rather than derived from the filters here: what belongs in an
   * export query is the filter feature's business, and taking a dependency on
   * it would put two sibling features in a knot for the sake of one function.
   */
  queryFor,
}: {
  queryFor: (format: string) => string;
}) {
  const [busy, setBusy] = useState<string | null>(null);
  const [failed, setFailed] = useState<string | null>(null);

  async function download(format: string) {
    setBusy(format);
    setFailed(null);
    try {
      const ticket = decode(
        ticketSchema,
        await api.get(`/reports/expenses/export/token?${queryFor(format)}`),
        "GET /reports/expenses/export/token",
      );
      // Assigning location rather than opening a window: a popup blocker stops
      // window.open from a promise callback, because by then the click is no
      // longer what is driving it. The response is an attachment, so the page
      // does not actually navigate away.
      window.location.assign(ticket.url);
    } catch (err) {
      setFailed(
        err instanceof ApiError && err.isPlanLimit
          ? "That export is larger than your plan allows."
          : "Could not prepare the download.",
      );
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="flex items-center gap-1.5">
      {/* A label, not a control: the three formats beside it are the controls,
          and hiding them behind a disclosure would add a click to every export
          to save 90 pixels. */}
      <span className="hidden items-center gap-1 rounded-lg border border-line-soft px-3 py-1.5 text-[13px] text-muted sm:inline-flex">
        {failed ?? "Export"}
        <ChevronDownIcon className="size-3.5" />
      </span>
      {FORMATS.map((format) => (
        <button
          key={format}
          type="button"
          disabled={busy !== null}
          onClick={() => void download(format)}
          className="rounded-lg border border-line-soft px-2.5 py-1.5 text-[11px] font-medium tracking-wide text-muted uppercase transition-colors hover:border-line hover:bg-elevated hover:text-fg disabled:text-faint"
        >
          {busy === format ? "…" : format}
        </button>
      ))}
      {/* The failure has to be reachable at the width where the label above is
          hidden, and it is an alert either way. */}
      {failed && (
        <span role="alert" className="text-xs text-tone-danger-fg sm:sr-only">
          {failed}
        </span>
      )}
    </div>
  );
}
