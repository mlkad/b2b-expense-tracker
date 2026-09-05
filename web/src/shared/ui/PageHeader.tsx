import type { ReactNode } from "react";

/**
 * The heading block every screen opens with.
 *
 * One h1 per page, a line explaining what the screen is for, and the actions
 * that belong to the whole screen rather than to a row. Shared so the type
 * scale cannot drift between screens - which it does, quickly, when each page
 * writes its own heading.
 */
export function PageHeader({
  title,
  description,
  actions,
}: {
  title: string;
  description?: string;
  actions?: ReactNode;
}) {
  return (
    <div className="mb-6 flex flex-wrap items-start justify-between gap-4">
      <div>
        <h1 className="text-[28px] leading-tight font-semibold tracking-tight">{title}</h1>
        {description && <p className="mt-1.5 max-w-2xl text-sm text-muted">{description}</p>}
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  );
}
