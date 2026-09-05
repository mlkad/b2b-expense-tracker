import { Button } from "./kit";

/**
 * Next and previous only, with the page number for orientation.
 *
 * No "page 7 of 43" and no jump-to-page, because the server cannot answer
 * either without counting the whole filtered set on every request - and a COUNT
 * over a tenant's history costs more than the page itself. Offering controls
 * the data model cannot support is how a list ends up slow for everybody so a
 * few people can jump to the end.
 */
export function Pagination({
  page,
  hasPrevious,
  hasNext,
  onPrevious,
  onNext,
  busy,
}: {
  page: number;
  hasPrevious: boolean;
  hasNext: boolean;
  onPrevious: () => void;
  onNext: () => void;
  busy?: boolean;
}) {
  if (!hasPrevious && !hasNext) return null;

  return (
    <nav aria-label="Pagination" className="flex items-center justify-between border-t border-ink-100 px-4 py-3">
      <Button variant="secondary" onClick={onPrevious} disabled={!hasPrevious || busy}>
        Previous
      </Button>
      {/* aria-live so a screen reader is told the page changed; the table
          itself gives no such signal when its rows are replaced. */}
      <span aria-live="polite" className="text-sm text-ink-600">
        Page {page}
      </span>
      <Button variant="secondary" onClick={onNext} disabled={!hasNext || busy}>
        Next
      </Button>
    </nav>
  );
}
