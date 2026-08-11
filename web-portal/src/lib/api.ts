import type { CatalogEntry, MyRequest, PortalProfile } from "./types";

/**
 * The citizen portal's API client.
 *
 * Deliberately its own file rather than a slice of the console's: this bundle
 * ships to the public, and anything imported here is readable by anyone. It
 * knows about seven endpoints and nothing else — no admin routes, no
 * permission names, no internal vocabulary.
 */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = "ApiError";
  }

  get isUnauthenticated() {
    return this.status === 401;
  }
}

// Same-origin: Apache serves /api on this host too, so the session cookie is
// scoped here and never needs SameSite=None.
const BASE = `${import.meta.env.BASE_URL.replace(/\/$/, "")}/api/portal`;

async function call<T>(path: string, opts: { method?: string; body?: unknown } = {}): Promise<T> {
  const init: RequestInit = {
    method: opts.method ?? "GET",
    credentials: "same-origin",
    headers: { Accept: "application/json" },
  };
  if (opts.body !== undefined) {
    init.headers = { ...init.headers, "Content-Type": "application/json" };
    init.body = JSON.stringify(opts.body);
  }

  const res = await fetch(`${BASE}${path}`, init);
  if (res.status === 204) return undefined as T;

  const text = await res.text();
  let parsed: unknown = null;
  if (text) {
    try {
      parsed = JSON.parse(text);
    } catch {
      parsed = null;
    }
  }

  if (!res.ok) {
    const problem = (parsed ?? {}) as { detail?: string; title?: string; code?: string };
    throw new ApiError(
      res.status,
      problem.code ?? "unknown",
      problem.detail || problem.title || "Something went wrong. Please try again.",
    );
  }
  return parsed as T;
}

export interface Listing<T> {
  items: T[];
}

export const portalApi = {
  // Pass { silent: true } for the prompt=none probe that signs in a resident
  // who already holds a live C2 session without showing any UI.
  loginUrl: (returnTo?: string, opts: { silent?: boolean } = {}) => {
    const params = new URLSearchParams();
    if (returnTo) params.set("returnTo", returnTo);
    if (opts.silent) params.set("silent", "true");
    const qs = params.toString();
    return `${BASE}/auth/login${qs ? `?${qs}` : ""}`;
  },
  logout: () => call<{ status: string; endSessionUrl?: string }>("/auth/logout", { method: "POST" }),
  me: () => call<PortalProfile>("/me"),

  catalog: () => call<Listing<CatalogEntry>>("/catalog"),
  myRequests: (openOnly = false) =>
    call<Listing<MyRequest>>(`/requests${openOnly ? "?openOnly=true" : ""}`),
  request: (reference: string) => call<MyRequest>(`/requests/${encodeURIComponent(reference)}`),

  report: (body: Record<string, unknown>) => call<MyRequest>("/requests", { method: "POST", body }),
  comment: (reference: string, text: string) =>
    call<{ status: string }>(`/requests/${encodeURIComponent(reference)}/comments`, {
      method: "POST", body: { body: text },
    }),
  cancel: (reference: string, reason: string) =>
    call<{ status: string }>(`/requests/${encodeURIComponent(reference)}/cancel`, {
      method: "POST", body: { reason },
    }),
  rate: (reference: string, score: number, comment: string) =>
    call<{ status: string }>(`/requests/${encodeURIComponent(reference)}/rating`, {
      method: "POST", body: { score, comment },
    }),
};
