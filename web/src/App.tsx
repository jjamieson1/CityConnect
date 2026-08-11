import { Navigate, Route, Routes } from "react-router-dom";

import { AuthProvider, useAuth } from "@/hooks/useAuth";
import { Shell } from "@/components/Shell";
import { Spinner } from "@/components/ui";

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
 * There is no local-account fallback by design, so an unauthenticated visitor
 * always ends up at the sign-in page, which explains the C2 dependency rather
 * than showing a bare form.
 */
function Protected() {
  const { me, loading } = useAuth();

  if (loading) {
    return (
      <div className="flex h-screen items-center justify-center">
        <Spinner label="Checking your session" />
      </div>
    );
  }
  if (!me?.user) {
    return <Navigate to="/login" replace />;
  }

  return (
    <Shell>
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
    </Shell>
  );
}
