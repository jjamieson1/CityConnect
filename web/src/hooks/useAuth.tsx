import { createContext, useCallback, useContext, useMemo } from "react";
import type { ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";

import { api, ApiError } from "@/lib/api";
import type { Me } from "@/lib/types";

interface AuthValue {
  me: Me | null;
  loading: boolean;
  error: unknown;
  /** can reads the permission list the server sent, rather than duplicating
   *  the role table in the client — a permission change on the server reaches
   *  the UI without a front-end release. */
  can: (permission: string) => boolean;
  signIn: () => void;
  signOut: () => Promise<void>;
}

const AuthContext = createContext<AuthValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();

  const { data, isLoading, error } = useQuery({
    queryKey: ["me"],
    queryFn: () => api.me(),
    retry: false,
    staleTime: 5 * 60_000,
  });

  const signIn = useCallback(() => {
    // A full page navigation, not fetch: the authorization flow is a browser
    // redirect through C2 and back, and XHR cannot carry the user through it.
    window.location.href = api.loginUrl();
  }, []);

  const signOut = useCallback(async () => {
    let endSessionUrl: string | undefined;
    try {
      ({ endSessionUrl } = await api.logout());
    } catch {
      // A failed local logout still clears the client's view of the session.
    }
    queryClient.clear();

    // Ending only the local session leaves the C2 session alive, so the user
    // would be silently signed straight back in. Follow C2's end_session
    // endpoint when it gave us one.
    window.location.href = endSessionUrl ?? `${import.meta.env.BASE_URL}login`;
  }, [queryClient]);

  const value = useMemo<AuthValue>(() => {
    const permissions = new Set(data?.permissions ?? []);
    return {
      me: data ?? null,
      loading: isLoading,
      error: error instanceof ApiError && error.isUnauthenticated ? null : error,
      can: (permission: string) => permissions.has(permission),
      signIn,
      signOut,
    };
  }, [data, isLoading, error, signIn, signOut]);

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth() {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used inside an AuthProvider");
  return ctx;
}
