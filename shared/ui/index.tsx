import type { ReactNode, ButtonHTMLAttributes, SelectHTMLAttributes, InputHTMLAttributes, TextareaHTMLAttributes } from "react";
import { useEffect, useRef } from "react";
import type { Priority, RequestStatus } from "./types";

export function cx(...parts: (string | false | null | undefined)[]) {
  return parts.filter(Boolean).join(" ");
}

// ---------------------------------------------------------------------------
// Primitives
// ---------------------------------------------------------------------------

type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "primary" | "secondary" | "danger" | "ghost";
  size?: "sm" | "md";
};

export function Button({ variant = "secondary", size = "md", className, ...rest }: ButtonProps) {
  return (
    <button
      {...rest}
      className={cx(
        "cc-btn",
        variant === "primary" && "cc-btn-primary",
        variant === "secondary" && "cc-btn-secondary",
        variant === "danger" && "cc-btn-danger",
        variant === "ghost" && "text-ink-muted hover:text-ink",
        size === "sm" && "px-2 py-1 text-xs",
        className,
      )}
    />
  );
}

export function Input({ className, ...rest }: InputHTMLAttributes<HTMLInputElement>) {
  return <input {...rest} className={cx("cc-input", className)} />;
}

export function Textarea({ className, ...rest }: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea {...rest} className={cx("cc-input", className)} />;
}

export function Select({ className, ...rest }: SelectHTMLAttributes<HTMLSelectElement>) {
  return <select {...rest} className={cx("cc-input", className)} />;
}

export function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label className="block">
      <span className="cc-label">{label}</span>
      {children}
      {hint && <span className="mt-1 block text-xs text-ink-faint">{hint}</span>}
    </label>
  );
}

export function Card({ title, actions, children, className }: {
  title?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={cx("cc-card", className)}>
      {(title || actions) && (
        <header className="flex items-center justify-between gap-3 border-b px-4 py-3" style={{ borderColor: "var(--border)" }}>
          <h2 className="text-sm font-semibold text-ink">{title}</h2>
          {actions && <div className="flex items-center gap-2">{actions}</div>}
        </header>
      )}
      <div className="p-4">{children}</div>
    </section>
  );
}

export function Empty({ title, hint, action }: { title: string; hint?: string; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-2 px-4 py-12 text-center">
      <p className="text-sm font-medium text-ink">{title}</p>
      {hint && <p className="max-w-md text-sm text-ink-muted">{hint}</p>}
      {action}
    </div>
  );
}

export function Spinner({ label = "Loading" }: { label?: string }) {
  return (
    <div className="flex items-center gap-2 px-4 py-8 text-sm text-ink-muted" role="status">
      <span
        className="inline-block h-4 w-4 animate-spin rounded-full border-2 border-current border-t-transparent"
        aria-hidden
      />
      {label}…
    </div>
  );
}

export function ErrorNote({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  const message = error instanceof Error ? error.message : "Something went wrong.";
  return (
    <div
      role="alert"
      className="flex items-start gap-3 rounded-md px-4 py-3 text-sm"
      style={{ background: "var(--status-critical-bg)", color: "var(--status-critical)" }}
    >
      <span aria-hidden>⚠</span>
      <div className="flex-1">
        <p className="font-medium">Could not complete that</p>
        <p className="mt-0.5 opacity-90">{message}</p>
      </div>
      {onRetry && (
        <Button size="sm" onClick={onRetry}>
          Retry
        </Button>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Status and priority
// ---------------------------------------------------------------------------

/**
 * Citizen-facing labels for internal statuses. A citizen should never be shown
 * "waiting_third_party", and staff read these faster than the raw enum too.
 */
export const STATUS_LABELS: Record<RequestStatus, string> = {
  new: "New",
  triaged: "Triaged",
  assigned: "Assigned",
  in_progress: "In progress",
  waiting_citizen: "Waiting on citizen",
  waiting_third_party: "Waiting on third party",
  resolved: "Resolved",
  closed: "Closed",
  cancelled: "Cancelled",
};

/**
 * Status tone maps onto the reserved status palette, never onto series
 * colours — and every badge ships its label, so state is never carried by
 * colour alone.
 */
const STATUS_TONE: Record<RequestStatus, "neutral" | "good" | "warning" | "serious"> = {
  new: "warning",
  triaged: "neutral",
  assigned: "neutral",
  in_progress: "neutral",
  waiting_citizen: "serious",
  waiting_third_party: "serious",
  resolved: "good",
  closed: "neutral",
  cancelled: "neutral",
};

export function StatusBadge({ status }: { status: RequestStatus }) {
  return <Badge tone={STATUS_TONE[status] ?? "neutral"}>{STATUS_LABELS[status] ?? status}</Badge>;
}

export const PRIORITY_LABELS: Record<Priority, string> = {
  low: "Low",
  normal: "Normal",
  high: "High",
  urgent: "Urgent",
  critical: "Critical",
};

export function PriorityBadge({ priority }: { priority: Priority }) {
  const tone =
    priority === "critical" ? "critical"
    : priority === "urgent" ? "serious"
    : priority === "high" ? "warning"
    : "neutral";
  return <Badge tone={tone}>{PRIORITY_LABELS[priority] ?? priority}</Badge>;
}

type Tone = "neutral" | "good" | "warning" | "serious" | "critical" | "accent";

export function Badge({ tone = "neutral", children }: { tone?: Tone; children: ReactNode }) {
  const styles: Record<Tone, { background: string; color: string }> = {
    neutral: { background: "var(--surface-0)", color: "var(--text-secondary)" },
    good: { background: "var(--status-good-bg)", color: "var(--status-good)" },
    warning: { background: "var(--status-warning-bg)", color: "var(--status-warning)" },
    serious: { background: "var(--status-serious-bg)", color: "var(--status-serious)" },
    critical: { background: "var(--status-critical-bg)", color: "var(--status-critical)" },
    accent: { background: "var(--surface-0)", color: "var(--accent)" },
  };
  return (
    <span
      className="inline-flex items-center whitespace-nowrap rounded px-1.5 py-0.5 text-xs font-medium"
      style={styles[tone]}
    >
      {children}
    </span>
  );
}

/**
 * SLA state is shown with an icon plus words, not a bare colour: "late" must
 * be legible to a colourblind reader and in a printed report.
 */
export function SLABadge({ dueAt, breached, warned, status }: {
  dueAt?: string;
  breached: boolean;
  warned: boolean;
  status: RequestStatus;
}) {
  if (status === "closed" || status === "cancelled") return null;
  if (breached) return <Badge tone="critical">⚑ Overdue</Badge>;
  if (warned) return <Badge tone="warning">◐ Due soon</Badge>;
  if (!dueAt) return null;
  return <Badge tone="neutral">Due {formatDate(dueAt)}</Badge>;
}

// ---------------------------------------------------------------------------
// Formatting
// ---------------------------------------------------------------------------

export function formatDate(value?: string | null) {
  if (!value) return "—";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" });
}

export function formatDateTime(value?: string | null) {
  if (!value) return "—";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString(undefined, {
    day: "numeric", month: "short", year: "numeric", hour: "2-digit", minute: "2-digit",
  });
}

export function relativeTime(value?: string | null) {
  if (!value) return "—";
  const then = new Date(value).getTime();
  if (Number.isNaN(then)) return "—";

  const seconds = Math.round((then - Date.now()) / 1000);
  const units: [Intl.RelativeTimeFormatUnit, number][] = [
    ["year", 31536000], ["month", 2592000], ["week", 604800],
    ["day", 86400], ["hour", 3600], ["minute", 60],
  ];
  const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
  for (const [unit, secondsPer] of units) {
    if (Math.abs(seconds) >= secondsPer) {
      return rtf.format(Math.round(seconds / secondsPer), unit);
    }
  }
  return rtf.format(seconds, "second");
}

export function formatBytes(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

// ---------------------------------------------------------------------------
// Modal
// ---------------------------------------------------------------------------

export function Modal({ open, onClose, title, children, wide }: {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  wide?: boolean;
}) {
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKey);
    ref.current?.focus();
    return () => document.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  if (!open) return null;

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/40 p-4 pt-16"
      onClick={onClose}
      role="presentation"
    >
      <div
        ref={ref}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className={cx("cc-card w-full shadow-xl", wide ? "max-w-3xl" : "max-w-lg")}
        onClick={(e) => e.stopPropagation()}
      >
        <header className="flex items-center justify-between border-b px-4 py-3" style={{ borderColor: "var(--border)" }}>
          <h2 className="text-sm font-semibold">{title}</h2>
          <Button variant="ghost" size="sm" onClick={onClose} aria-label="Close">
            ✕
          </Button>
        </header>
        <div className="p-4">{children}</div>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Pagination
// ---------------------------------------------------------------------------

export function Pager({ total, limit, offset, onChange }: {
  total: number;
  limit: number;
  offset: number;
  onChange: (offset: number) => void;
}) {
  const page = Math.floor(offset / limit) + 1;
  const pages = Math.max(1, Math.ceil(total / limit));
  if (total <= limit) return null;

  return (
    <nav className="flex items-center justify-between gap-3 px-4 py-3 text-sm" aria-label="Pagination">
      <span className="text-ink-muted">
        {offset + 1}–{Math.min(offset + limit, total)} of {total.toLocaleString()}
      </span>
      <div className="flex items-center gap-2">
        <Button size="sm" disabled={page <= 1} onClick={() => onChange(Math.max(0, offset - limit))}>
          Previous
        </Button>
        <span className="text-ink-muted">
          Page {page} of {pages}
        </span>
        <Button size="sm" disabled={page >= pages} onClick={() => onChange(offset + limit)}>
          Next
        </Button>
      </div>
    </nav>
  );
}
