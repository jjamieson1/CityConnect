import { Navigate, useSearchParams } from "react-router-dom";

import { useAuth } from "@/hooks/useAuth";
import { Button, Spinner } from "@/components/ui";

/**
 * Reasons the callback can bounce a user back here. Each gets a specific
 * explanation: "no access" in particular is not an error the user can fix by
 * trying again, and telling them to contact an administrator saves a support
 * call.
 */
const REASONS: Record<string, { title: string; detail: string }> = {
  no_access: {
    title: "This account has no CityConnect access",
    detail:
      "You signed in to C2 successfully, but there is no CityConnect account for this identity. " +
      "Ask a CityConnect administrator to invite you, then sign in again.",
  },
  suspended: {
    title: "This account is suspended",
    detail: "Your CityConnect access has been suspended. Contact an administrator.",
  },
  session_expired: {
    title: "Your session expired",
    detail: "You were signed out because your C2 session ended. Sign in again to continue.",
  },
  expired: {
    title: "That sign-in attempt expired",
    detail: "Sign-in attempts are valid for a few minutes. Please try again.",
  },
  failed: {
    title: "Sign-in could not be completed",
    detail:
      "Something went wrong while completing sign-in. If this keeps happening, " +
      "an administrator can check the service logs for the cause.",
  },
  access_denied: {
    title: "Sign-in was cancelled",
    detail: "You declined to share your identity with CityConnect.",
  },
};

export default function Login() {
  const { me, loading, signIn } = useAuth();
  const [params] = useSearchParams();
  const reason = params.get("reason");

  if (loading) {
    return (
      <div className="flex h-screen items-center justify-center">
        <Spinner label="Checking your session" />
      </div>
    );
  }
  if (me?.user) return <Navigate to="/" replace />;

  const problem = reason ? (REASONS[reason] ?? REASONS.failed) : null;

  return (
    <div className="flex min-h-screen items-center justify-center px-4">
      <div className="w-full max-w-md">
        <div className="mb-6 flex items-center gap-3">
          <span
            className="grid h-11 w-11 place-items-center rounded-lg text-sm font-bold text-white"
            style={{ background: "var(--accent)" }}
            aria-hidden
          >
            CC
          </span>
          <div>
            <h1 className="text-xl font-semibold">CityConnect</h1>
            <p className="text-sm text-ink-muted">Service request management</p>
          </div>
        </div>

        {problem && (
          <div
            role="alert"
            className="mb-4 rounded-md px-4 py-3 text-sm"
            style={{
              background:
                reason === "session_expired" ? "var(--status-warning-bg)" : "var(--status-critical-bg)",
              color:
                reason === "session_expired" ? "var(--status-warning)" : "var(--status-critical)",
            }}
          >
            <p className="font-medium">{problem.title}</p>
            <p className="mt-1 opacity-90">{problem.detail}</p>
          </div>
        )}

        <div className="cc-card p-6">
          <h2 className="text-base font-semibold">Sign in</h2>
          <p className="mt-1 text-sm text-ink-muted">
            CityConnect uses C2 single sign-on. There is no separate CityConnect password.
          </p>

          <Button variant="primary" className="mt-5 w-full" onClick={signIn}>
            Continue with C2
          </Button>

          <p className="mt-4 text-xs text-ink-faint">
            If C2 is unavailable, sign-in will not work — CityConnect has no local accounts by
            design. An administrator can confirm the service status.
          </p>
        </div>
      </div>
    </div>
  );
}
