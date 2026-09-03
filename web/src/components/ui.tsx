import type { ButtonHTMLAttributes, InputHTMLAttributes, ReactNode, SelectHTMLAttributes } from "react";

type Variant = "primary" | "secondary" | "danger" | "ghost";

const VARIANTS: Record<Variant, string> = {
  primary: "bg-brand-600 text-white hover:bg-brand-700 disabled:bg-ink-200 disabled:text-ink-600",
  secondary: "bg-white text-ink-900 border border-ink-200 hover:bg-ink-50 disabled:text-ink-400",
  danger: "bg-danger-700 text-white hover:bg-danger-700/90 disabled:bg-ink-200 disabled:text-ink-600",
  ghost: "text-ink-600 hover:bg-ink-100 disabled:text-ink-400",
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
      className={`inline-flex items-center justify-center gap-2 rounded-md px-3.5 py-2 text-sm font-medium transition-colors disabled:cursor-not-allowed ${VARIANTS[variant]} ${className}`}
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
    <div className="flex flex-col gap-1.5">
      <label htmlFor={htmlFor} className="text-sm font-medium text-ink-800">
        {label}
      </label>
      {children}
      {hint && !error && (
        <p id={hintId} className="text-xs text-ink-600">
          {hint}
        </p>
      )}
      {/* role="alert" so the message is announced when it appears, rather than
          only being found by somebody who happens to navigate back to it. */}
      {error && (
        <p id={errorId} role="alert" className="text-xs text-danger-700">
          {error}
        </p>
      )}
    </div>
  );
}

export function TextInput({ invalid, className = "", ...rest }: InputHTMLAttributes<HTMLInputElement> & { invalid?: boolean }) {
  return (
    <input
      aria-invalid={invalid || undefined}
      aria-describedby={invalid ? `${rest.id}-error` : rest["aria-describedby"]}
      className={`rounded-md border bg-white px-3 py-2 text-sm outline-none placeholder:text-ink-400 ${
        invalid ? "border-danger-700" : "border-ink-200"
      } ${className}`}
      {...rest}
    />
  );
}

export function Select({ invalid, className = "", children, ...rest }: SelectHTMLAttributes<HTMLSelectElement> & { invalid?: boolean }) {
  return (
    <select
      aria-invalid={invalid || undefined}
      className={`rounded-md border bg-white px-3 py-2 text-sm outline-none ${
        invalid ? "border-danger-700" : "border-ink-200"
      } ${className}`}
      {...rest}
    >
      {children}
    </select>
  );
}

const TONES = {
  neutral: "bg-ink-100 text-ink-800",
  positive: "bg-positive-50 text-positive-700",
  caution: "bg-caution-50 text-caution-700",
  danger: "bg-danger-50 text-danger-700",
  brand: "bg-brand-50 text-brand-600",
} as const;

export type Tone = keyof typeof TONES;

export function Badge({ tone = "neutral", children }: { tone?: Tone; children: ReactNode }) {
  return (
    <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${TONES[tone]}`}>
      {children}
    </span>
  );
}

export function Card({ children, className = "" }: { children: ReactNode; className?: string }) {
  return (
    <div className={`rounded-lg border border-ink-100 bg-white ${className}`}>{children}</div>
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
    <div role="alert" className="rounded-md border border-danger-700/20 bg-danger-50 px-4 py-3 text-sm">
      <p className="font-medium text-danger-700">{title}</p>
      {detail && <p className="mt-1 text-ink-800">{detail}</p>}
      {traceId && <p className="mt-2 font-mono text-xs text-ink-600">Reference: {traceId}</p>}
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
    <div className="flex flex-col items-center gap-2 px-6 py-12 text-center">
      <p className="text-sm font-medium text-ink-800">{title}</p>
      {detail && <p className="max-w-sm text-sm text-ink-600">{detail}</p>}
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
    <div aria-hidden="true" className="divide-y divide-ink-100">
      {Array.from({ length: rows }, (_, r) => (
        <div key={r} className="flex gap-4 px-4 py-3.5">
          {Array.from({ length: columns }, (_, c) => (
            <div key={c} className="h-4 flex-1 animate-pulse rounded bg-ink-100" />
          ))}
        </div>
      ))}
    </div>
  );
}
