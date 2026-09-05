import type {
  ButtonHTMLAttributes,
  CSSProperties,
  InputHTMLAttributes,
  ReactNode,
  SelectHTMLAttributes,
} from "react";

type Variant = "primary" | "secondary" | "danger" | "ghost";

/**
 * The primary button is light on dark, not the reverse.
 *
 * A saturated purple fill with white text is the first thing to fail a contrast
 * check at 14px, and it is also the wrong emphasis: on a page this dark, the
 * brightest thing should be the one action worth taking.
 */
const VARIANTS: Record<Variant, string> = {
  primary:
    "bg-accent text-accent-ink hover:bg-accent-hover disabled:bg-elevated disabled:text-faint",
  secondary:
    "bg-elevated text-fg border border-line hover:bg-line disabled:text-faint disabled:border-line-soft",
  danger:
    "bg-tone-danger text-tone-danger-fg border border-tone-danger-fg/25 hover:bg-tone-danger/80 disabled:text-faint",
  ghost: "text-muted hover:bg-elevated hover:text-fg disabled:text-faint",
};

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant;
  busy?: boolean;
}

export function Button({ variant = "primary", busy, disabled, children, className = "", ...rest }: ButtonProps) {
  return (
    <button
      // Explicit, because a button inside a form defaults to submit and a
      // "Cancel" that submits the form is a bug that only appears on Enter.
      type={rest.type ?? "button"}
      disabled={disabled || busy}
      // aria-busy rather than swapping the label for a spinner: a screen
      // reader is told the control is working without the accessible name
      // changing underneath the user.
      aria-busy={busy || undefined}
      className={`inline-flex items-center justify-center gap-2 rounded-md px-3 py-1.5 text-[13px] font-medium transition-colors disabled:cursor-not-allowed ${VARIANTS[variant]} ${className}`}
      {...rest}
    >
      {busy && <Spinner />}
      {children}
    </button>
  );
}

function Spinner() {
  return (
    <span
      aria-hidden="true"
      className="size-3.5 animate-spin rounded-full border-2 border-current border-t-transparent"
    />
  );
}

interface FieldProps {
  label: string;
  htmlFor: string;
  error?: string;
  hint?: string;
  children: ReactNode;
}

export function Field({ label, htmlFor, error, hint, children }: FieldProps) {
  const hintId = hint ? `${htmlFor}-hint` : undefined;
  const errorId = error ? `${htmlFor}-error` : undefined;

  return (
    <div className="flex flex-col gap-1">
      <label htmlFor={htmlFor} className="text-[11px] font-medium text-muted">
        {label}
      </label>
      {children}
      {hint && !error && (
        <p id={hintId} className="text-xs text-faint">
          {hint}
        </p>
      )}
      {/* role="alert" so the message is announced when it appears, rather than
          only being found by somebody who happens to navigate back to it. */}
      {error && (
        <p id={errorId} role="alert" className="text-xs text-tone-danger-fg">
          {error}
        </p>
      )}
    </div>
  );
}

/**
 * The caret a native <select> draws for itself, restated.
 *
 * A data URI rather than an absolutely positioned icon: a real select paints
 * its popup outside the DOM, and an overlaid element sits on top of the click
 * target on some platforms - so the arrow becomes the one part of the control
 * that does not open it.
 */
const CARET =
  "url(\"data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='%23a39fb0' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'%3E%3Cpath d='m6 9 6 6 6-6'/%3E%3C/svg%3E\")";

const CONTROL =
  "w-full rounded-md border bg-surface px-2.5 py-1 text-[13px] text-fg outline-none transition-colors placeholder:text-faint hover:border-line focus:border-accent-strong";

export function TextInput({ invalid, className = "", ...rest }: InputHTMLAttributes<HTMLInputElement> & { invalid?: boolean }) {
  return (
    <input
      aria-invalid={invalid || undefined}
      aria-describedby={invalid ? `${rest.id}-error` : rest["aria-describedby"]}
      className={`${CONTROL} ${invalid ? "border-tone-danger-fg" : "border-line-soft"} ${className}`}
      {...rest}
    />
  );
}

export function Select({ invalid, className = "", children, ...rest }: SelectHTMLAttributes<HTMLSelectElement> & { invalid?: boolean }) {
  return (
    <select
      aria-invalid={invalid || undefined}
      className={`${CONTROL} appearance-none bg-[image:var(--select-caret)] bg-[length:14px] bg-[position:right_0.75rem_center] bg-no-repeat pr-9 ${
        invalid ? "border-tone-danger-fg" : "border-line-soft"
      } ${className}`}
      style={{ "--select-caret": CARET } as CSSProperties}
      {...rest}
    >
      {children}
    </select>
  );
}

const TONES = {
  neutral: "bg-tone-neutral text-tone-neutral-fg",
  positive: "bg-tone-positive text-tone-positive-fg",
  caution: "bg-tone-caution text-tone-caution-fg",
  danger: "bg-tone-danger text-tone-danger-fg",
  info: "bg-tone-info text-tone-info-fg",
  brand: "bg-accent-dim text-accent",
} as const;

export type Tone = keyof typeof TONES;

export function Badge({ tone = "neutral", children }: { tone?: Tone; children: ReactNode }) {
  return (
    <span
      className={`inline-flex items-center rounded-full px-2 py-px text-[11px] font-medium whitespace-nowrap ${TONES[tone]}`}
    >
      {children}
    </span>
  );
}

export function Card({ children, className = "" }: { children: ReactNode; className?: string }) {
  return (
    <div className={`rounded-xl border border-line-soft bg-surface ${className}`}>{children}</div>
  );
}

/**
 * An error the user can act on.
 *
 * The trace id is shown because it is what a support conversation needs: it
 * appears on every log line for that request, so "it failed and the id was
 * abc" is answerable in one query.
 */
export function ErrorNotice({ title, detail, traceId }: { title: string; detail?: string; traceId?: string }) {
  return (
    <div
      role="alert"
      className="rounded-lg border border-tone-danger-fg/25 bg-tone-danger/60 px-3.5 py-2.5 text-[13px]"
    >
      <p className="font-medium text-tone-danger-fg">{title}</p>
      {detail && <p className="mt-1 text-fg/90">{detail}</p>}
      {traceId && <p className="mt-2 font-mono text-xs text-muted">Reference: {traceId}</p>}
    </div>
  );
}

/**
 * An empty state that says what to do next.
 *
 * A table showing nothing with no explanation is indistinguishable from one
 * that failed to load, and the reader cannot tell which without opening the
 * network tab.
 */
export function EmptyState({ title, detail, action }: { title: string; detail?: string; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-1.5 px-6 py-12 text-center">
      <p className="text-sm font-medium text-fg">{title}</p>
      {detail && <p className="max-w-sm text-sm text-muted">{detail}</p>}
      {action && <div className="mt-2">{action}</div>}
    </div>
  );
}

/**
 * A skeleton row, sized to the content it replaces.
 *
 * Matching the real height is the point: a spinner that collapses to nothing
 * and then expands makes the page jump under whatever the reader was about to
 * click.
 */
export function SkeletonRows({ rows = 5, columns = 4 }: { rows?: number; columns?: number }) {
  return (
    <div aria-hidden="true" className="divide-y divide-line-soft">
      {Array.from({ length: rows }, (_, r) => (
        <div key={r} className="flex gap-4 px-4 py-2">
          {Array.from({ length: columns }, (_, c) => (
            <div key={c} className="h-4 flex-1 animate-pulse rounded bg-elevated" />
          ))}
        </div>
      ))}
    </div>
  );
}

/**
 * The column header row every table on this dashboard shares.
 *
 * Small, letter-spaced and quiet: a header that competes with the figures below
 * it makes a table harder to scan, and scanning is the only thing anybody does
 * with these.
 */
export function TableHead({ children }: { children: ReactNode }) {
  return (
    <thead>
      <tr className="border-b border-line-soft text-left text-[10px] font-medium tracking-[0.09em] text-faint uppercase">
        {children}
      </tr>
    </thead>
  );
}
