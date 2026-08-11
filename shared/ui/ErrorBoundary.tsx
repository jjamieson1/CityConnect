import { Component } from "react";
import type { ErrorInfo, ReactNode } from "react";

/**
 * Catches render-time errors so one broken component cannot unmount the whole
 * application.
 *
 * Without this, any exception thrown during render leaves React with an empty
 * root — a blank page, no message, nothing in the UI to act on. An agent
 * working a queue sees the console vanish mid-shift and has no idea whether
 * their last action saved. Showing the error, and letting the rest of the app
 * keep working, is the difference between a bug and an outage.
 */
interface Props {
  children: ReactNode;
  /** Where this boundary sits, used in the message and the log. */
  area?: string;
  /** Rendered instead of the default panel. */
  fallback?: (error: Error, reset: () => void) => ReactNode;
}

interface State {
  error: Error | null;
}

export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // The stack is the only record of what happened; the server never sees a
    // client-side crash.
    console.error(`[CityConnect] render error in ${this.props.area ?? "the app"}`, error, info.componentStack);
  }

  reset = () => this.setState({ error: null });

  render() {
    const { error } = this.state;
    if (!error) return this.props.children;

    if (this.props.fallback) return this.props.fallback(error, this.reset);

    return (
      <div
        role="alert"
        className="cc-card m-4 p-5"
        style={{ borderColor: "var(--status-critical)" }}
      >
        <h2 className="text-base font-semibold" style={{ color: "var(--status-critical)" }}>
          This part of the page could not be displayed
        </h2>
        <p className="mt-1 text-sm text-ink-muted">
          {this.props.area ? `The ${this.props.area} hit an error. ` : ""}
          The rest of CityConnect is still working — the message below is what went wrong.
        </p>

        <pre
          className="mt-3 max-h-48 overflow-auto rounded p-3 text-xs"
          style={{ background: "var(--surface-0)", color: "var(--text-secondary)" }}
        >
          {error.message}
        </pre>

        <div className="mt-3 flex gap-2">
          <button className="cc-btn cc-btn-primary" onClick={this.reset}>
            Try again
          </button>
          <button className="cc-btn cc-btn-secondary" onClick={() => location.reload()}>
            Reload the page
          </button>
        </div>
      </div>
    );
  }
}
