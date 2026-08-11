import { useEffect } from "react";
import { Navigate, Route, Routes } from "react-router-dom";

import { AuthProvider, useAuth } from "@/hooks/useAuth";
import { api } from "@/lib/api";
import { Shell } from "@/components/Shell";
import { ErrorBoundary } from "@/components/ErrorBoundary";
import { Spinner } from "@/components/ui";

// One-shot guard, per tab, so a failed silent SSO cannot loop.
const SILENT_SSO_TRIED = "cc.silentSsoTried";

import Login from "@/pages/Login";
import Dashboard from "@/pages/Dashboard";
import RequestList from "@/pages/RequestList";
import RequestDetail from "@/pages/RequestDetail";
import ContactList from "@/pages/ContactList";
import ContactDetail from "@/pages/ContactDetail";
import Reports from "@/pages/Reports";
import Admin from "@/pages/Admin";

export default function App() {
  return (
    <AuthProvider>
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route path="/*" element={<Protected />} />
      </Routes>
    </AuthProvider>
  );
}

/**
 * Protected gates every console route on an established session.
 *
 * An unauthenticated visitor may still hold a live C2 session, so we first try
 * a silent (prompt=none) SSO: if C2 recognises them they are carried straight
 * in without ever seeing a screen. Only when C2 answers "no session" — which
 * lands the callback on /login, outside this component — does the sign-in page
 * appear. A per-tab guard makes the probe a one-shot, so it cannot loop.
 */
function Protected() {
  const { me, loading } = useAuth();

  // Once a session is established, reset the guard so a later sign-out can
  // probe again on the next visit.
  useEffect(() => {
    if (me?.user) sessionStorage.removeItem(SILENT_SSO_TRIED);
  }, [me]);

  if (loading) {
    return (
      <div className="flex h-screen items-center justify-center">
        <Spinner label="Checking your session" />
      </div>
    );
  }
  if (!me?.user) {
    if (!sessionStorage.getItem(SILENT_SSO_TRIED)) {
      sessionStorage.setItem(SILENT_SSO_TRIED, "1");
      // Full-page navigation: the authorization flow is a browser redirect
      // through C2 and back. replace() keeps it out of history.
      window.location.replace(api.loginUrl({ silent: true }));
      return (
        <div className="flex h-screen items-center justify-center">
          <Spinner label="Signing you in" />
        </div>
      );
    }
    return <Navigate to="/login" replace />;
  }

  return (
    <Shell>
      <ErrorBoundary area="page">
      <Routes>
        <Route path="/" element={<Dashboard />} />
        <Route path="/requests" element={<RequestList />} />
        <Route path="/requests/:id" element={<RequestDetail />} />
        <Route path="/contacts" element={<ContactList />} />
        <Route path="/contacts/:id" element={<ContactDetail />} />
        <Route path="/reports/*" element={<Reports />} />
        <Route path="/admin/*" element={<Admin />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
      </ErrorBoundary>
    </Shell>
  );
}
