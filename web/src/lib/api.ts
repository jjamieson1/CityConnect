import type {
  AgentReport, ApiToken, Attachment, AuditEntry, BusinessCalendar, ConnectedSystem,
  Contact, ContactChannel, ContactIdentity, Department, DuplicateCandidate,
  Interaction, JobStatus, Listing, Macro, Me, NotificationRecord,
  NotificationTemplate, Page, Priority, Queue, RequestComment, RequestEvent,
  RequestLink, RequestStatus, RoutingRule, SavedView, SearchResult,
  ServiceRequest, ServiceType, SimulationResult, SLAPolicy, SLAReport,
  TimelineEntry, User, VolumeReport, Visibility, WebhookDelivery,
} from "./types";

/**
 * ApiError carries the problem+json body so callers can switch on a stable
 * code rather than parsing prose. The two the console acts on specifically are
 * `stale_version` (offer a reload) and `suppressed` (explain the consent gate).
 */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly requestId?: string,
  ) {
    super(message);
    this.name = "ApiError";
  }

  get isStale() {
    return this.code === "stale_version";
  }
  get isUnauthenticated() {
    return this.status === 401;
  }
  get isForbidden() {
    return this.status === 403;
  }
}

// The API lives under the same base path the SPA is served from, so one build
// works at the root in dev and under /cityconnect/ in production.
const BASE = `${import.meta.env.BASE_URL.replace(/\/$/, "")}/api`;

interface RequestOptions {
  method?: string;
  body?: unknown;
  signal?: AbortSignal;
  raw?: boolean;
}

async function call<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const init: RequestInit = {
    method: opts.method ?? "GET",
    // The session is an HTTP-only cookie; it must ride along on every call.
    credentials: "same-origin",
    headers: { Accept: "application/json" },
    signal: opts.signal,
  };

  if (opts.body !== undefined) {
    if (opts.body instanceof FormData) {
      init.body = opts.body;
    } else {
      init.headers = { ...init.headers, "Content-Type": "application/json" };
      init.body = JSON.stringify(opts.body);
    }
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
    const problem = (parsed ?? {}) as {
      detail?: string;
      title?: string;
      code?: string;
      requestId?: string;
    };
    throw new ApiError(
      res.status,
      problem.code ?? "unknown",
      problem.detail || problem.title || `Request failed (${res.status})`,
      problem.requestId,
    );
  }

  return parsed as T;
}

function qs(params: Record<string, unknown>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === "" || value === false) continue;
    if (Array.isArray(value)) {
      if (value.length > 0) search.set(key, value.join(","));
      continue;
    }
    search.set(key, String(value));
  }
  const encoded = search.toString();
  return encoded ? `?${encoded}` : "";
}

export interface RequestFilters {
  q?: string;
  status?: RequestStatus[];
  priority?: Priority[];
  queueId?: string;
  departmentId?: string;
  serviceTypeId?: string;
  assigneeUserId?: string;
  contactId?: string;
  ward?: string;
  tag?: string;
  source?: string;
  openOnly?: boolean;
  unassigned?: boolean;
  breached?: boolean;
  limit?: number;
  offset?: number;
  sort?: string;
  dir?: "asc" | "desc";
}

export const api = {
  // ---- auth -------------------------------------------------------------
  me: () => call<Me>("/auth/me"),
  loginUrl: () => `${BASE}/auth/login?returnTo=${encodeURIComponent(location.pathname)}`,
  logout: () => call<{ status: string; endSessionUrl?: string }>("/auth/logout", { method: "POST" }),

  // ---- requests ---------------------------------------------------------
  requests: (f: RequestFilters = {}) => call<Page<ServiceRequest>>(`/requests${qs({ ...f })}`),
  requestCount: (f: RequestFilters = {}) => call<{ count: number }>(`/requests/count${qs({ ...f })}`),
  request: (id: string) => call<ServiceRequest>(`/requests/${id}`),
  requestByReference: (ref: string) => call<ServiceRequest>(`/requests-by-reference/${encodeURIComponent(ref)}`),

  createRequest: (body: Record<string, unknown>) =>
    call<ServiceRequest>("/requests", { method: "POST", body }),
  updateRequest: (id: string, body: Record<string, unknown>) =>
    call<ServiceRequest>(`/requests/${id}`, { method: "PATCH", body }),
  transition: (id: string, body: { to: RequestStatus; note?: string; resolutionCode?: string; notifyCitizen?: boolean; version?: number }) =>
    call<ServiceRequest>(`/requests/${id}/transition`, { method: "POST", body }),
  assign: (id: string, body: { userId?: string; systemId?: string; version?: number }) =>
    call<ServiceRequest>(`/requests/${id}/assign`, { method: "POST", body }),
  transfer: (id: string, body: { departmentId?: string; queueId?: string; note?: string }) =>
    call<ServiceRequest>(`/requests/${id}/transfer`, { method: "POST", body }),
  applyMacro: (id: string, macroId: string) =>
    call<ServiceRequest>(`/requests/${id}/macros/${macroId}`, { method: "POST" }),
  bulk: (body: Record<string, unknown>) =>
    call<{ succeeded: string[]; failed: Record<string, string> }>("/requests/bulk", { method: "POST", body }),

  requestEvents: (id: string) => call<Listing<RequestEvent>>(`/requests/${id}/events`),
  comments: (id: string) => call<Listing<RequestComment>>(`/requests/${id}/comments`),
  addComment: (id: string, body: { body: string; visibility: Visibility; notifyCitizen?: boolean; macroId?: string }) =>
    call<RequestComment>(`/requests/${id}/comments`, { method: "POST", body }),

  links: (id: string) => call<Listing<RequestLink>>(`/requests/${id}/links`),
  addLink: (id: string, body: { targetReference?: string; targetId?: string; kind: string; note?: string }) =>
    call<RequestLink>(`/requests/${id}/links`, { method: "POST", body }),
  unlink: (id: string, linkId: string) =>
    call<void>(`/requests/${id}/links/${linkId}`, { method: "DELETE" }),

  attachments: (id: string) => call<Listing<Attachment>>(`/requests/${id}/attachments`),
  upload: (id: string, file: File, visibility: Visibility) => {
    const form = new FormData();
    form.append("file", file);
    form.append("visibility", visibility);
    return call<Attachment>(`/requests/${id}/attachments`, { method: "POST", body: form });
  },
  attachmentUrl: (requestId: string, id: string) => `${BASE}/requests/${requestId}/attachments/${id}`,
  deleteAttachment: (requestId: string, id: string) =>
    call<void>(`/requests/${requestId}/attachments/${id}`, { method: "DELETE" }),

  // ---- contacts ---------------------------------------------------------
  contacts: (params: Record<string, unknown> = {}) => call<Page<Contact>>(`/contacts${qs(params)}`),
  contact: (id: string) => call<Contact>(`/contacts/${id}`),
  createContact: (body: Partial<Contact>) => call<Contact>("/contacts", { method: "POST", body }),
  updateContact: (id: string, body: Partial<Contact>) =>
    call<Contact>(`/contacts/${id}`, { method: "PATCH", body }),
  deleteContact: (id: string) => call<void>(`/contacts/${id}`, { method: "DELETE" }),
  timeline: (id: string) => call<Listing<TimelineEntry>>(`/contacts/${id}/timeline`),
  duplicates: (id: string) => call<Listing<DuplicateCandidate>>(`/contacts/${id}/duplicates`),
  merge: (survivorId: string, body: { mergedId: string; note?: string }) =>
    call<Contact>(`/contacts/${survivorId}/merge`, { method: "POST", body }),
  addIdentity: (id: string, body: Partial<ContactIdentity>) =>
    call<ContactIdentity>(`/contacts/${id}/identities`, { method: "POST", body }),
  removeIdentity: (id: string, identityId: string) =>
    call<void>(`/contacts/${id}/identities/${identityId}`, { method: "DELETE" }),
  saveChannel: (id: string, body: Partial<ContactChannel>) =>
    call<ContactChannel>(`/contacts/${id}/channels`, { method: "POST", body }),
  deleteChannel: (id: string, channelId: string) =>
    call<void>(`/contacts/${id}/channels/${channelId}`, { method: "DELETE" }),

  // ---- interactions -----------------------------------------------------
  interactions: (params: Record<string, unknown> = {}) =>
    call<Page<Interaction>>(`/interactions${qs(params)}`),
  createInteraction: (body: Partial<Interaction>) =>
    call<Interaction>("/interactions", { method: "POST", body }),

  // ---- catalogue and routing -------------------------------------------
  serviceTypes: (params: Record<string, unknown> = {}) =>
    call<Listing<ServiceType>>(`/service-types${qs(params)}`),
  saveServiceType: (body: Partial<ServiceType>) =>
    call<ServiceType>(body.id ? `/service-types/${body.id}` : "/service-types", {
      method: body.id ? "PATCH" : "POST",
      body,
    }),
  deleteServiceType: (id: string) => call<void>(`/service-types/${id}`, { method: "DELETE" }),

  slaPolicies: () => call<Listing<SLAPolicy>>("/sla-policies"),
  saveSLAPolicy: (body: Partial<SLAPolicy>) =>
    call<SLAPolicy>(body.id ? `/sla-policies/${body.id}` : "/sla-policies", {
      method: body.id ? "PATCH" : "POST",
      body,
    }),

  calendars: () => call<Listing<BusinessCalendar>>("/business-calendars"),
  saveCalendar: (body: Partial<BusinessCalendar>) =>
    call<BusinessCalendar>(body.id ? `/business-calendars/${body.id}` : "/business-calendars", {
      method: body.id ? "PATCH" : "POST",
      body,
    }),

  queues: (params: Record<string, unknown> = {}) => call<Listing<Queue>>(`/queues${qs(params)}`),
  queue: (id: string) => call<Queue>(`/queues/${id}`),
  saveQueue: (body: Partial<Queue>) =>
    call<Queue>(body.id ? `/queues/${body.id}` : "/queues", {
      method: body.id ? "PATCH" : "POST",
      body,
    }),
  deleteQueue: (id: string) => call<void>(`/queues/${id}`, { method: "DELETE" }),
  setQueueMembers: (id: string, userIds: string[]) =>
    call<void>(`/queues/${id}/members`, { method: "PUT", body: { userIds } }),

  rules: (includeInactive = true) =>
    call<Listing<RoutingRule>>(`/routing-rules${qs({ includeInactive })}`),
  saveRule: (body: Partial<RoutingRule>) =>
    call<RoutingRule>(body.id ? `/routing-rules/${body.id}` : "/routing-rules", {
      method: body.id ? "PATCH" : "POST",
      body,
    }),
  deleteRule: (id: string) => call<void>(`/routing-rules/${id}`, { method: "DELETE" }),
  simulate: (body: { rules?: Partial<RoutingRule>[]; useStored?: boolean; sample?: number }) =>
    call<SimulationResult>("/routing-rules/simulate", { method: "POST", body }),

  macros: (departmentId?: string) => call<Listing<Macro>>(`/macros${qs({ departmentId })}`),
  saveMacro: (body: Partial<Macro>) =>
    call<Macro>(body.id ? `/macros/${body.id}` : "/macros", {
      method: body.id ? "PATCH" : "POST",
      body,
    }),
  deleteMacro: (id: string) => call<void>(`/macros/${id}`, { method: "DELETE" }),

  // ---- organisation -----------------------------------------------------
  departments: (includeInactive = false) =>
    call<Listing<Department>>(`/departments${qs({ includeInactive })}`),
  saveDepartment: (body: Partial<Department>) =>
    call<Department>(body.id ? `/departments/${body.id}` : "/departments", {
      method: body.id ? "PATCH" : "POST",
      body,
    }),
  deleteDepartment: (id: string) => call<void>(`/departments/${id}`, { method: "DELETE" }),

  users: (params: Record<string, unknown> = {}) => call<Page<User>>(`/users${qs(params)}`),
  user: (id: string) => call<User>(`/users/${id}`),
  invite: (body: Record<string, unknown>) =>
    call<{ user: User; note: string }>("/users/invite", { method: "POST", body }),
  updateUser: (id: string, body: Record<string, unknown>) =>
    call<User>(`/users/${id}`, { method: "PATCH", body }),
  revokeSessions: (id: string) =>
    call<{ revoked: number }>(`/users/${id}/revoke-sessions`, { method: "POST" }),

  systems: (includeInactive = true) =>
    call<Listing<ConnectedSystem>>(`/connected-systems${qs({ includeInactive })}`),
  saveSystem: (body: Partial<ConnectedSystem> & { queueIds?: string[] }) =>
    call<ConnectedSystem>(body.id ? `/connected-systems/${body.id}` : "/connected-systems", {
      method: body.id ? "PATCH" : "POST",
      body,
    }),
  rotateSecret: (id: string) =>
    call<{ webhookSecret: string; note: string }>(`/connected-systems/${id}/rotate-secret`, { method: "POST" }),

  tokens: (params: Record<string, unknown> = {}) => call<Listing<ApiToken>>(`/tokens${qs(params)}`),
  issueToken: (body: Record<string, unknown>) =>
    call<{ token: string; record: ApiToken; note: string }>("/tokens", { method: "POST", body }),
  revokeToken: (id: string) => call<void>(`/tokens/${id}`, { method: "DELETE" }),

  // ---- communications ---------------------------------------------------
  templates: () => call<Listing<NotificationTemplate>>("/notification-templates"),
  saveTemplate: (body: Partial<NotificationTemplate>) =>
    call<NotificationTemplate>(body.id ? `/notification-templates/${body.id}` : "/notification-templates", {
      method: body.id ? "PATCH" : "POST",
      body,
    }),
  deleteTemplate: (id: string) => call<void>(`/notification-templates/${id}`, { method: "DELETE" }),

  notifications: (params: Record<string, unknown> = {}) =>
    call<Page<NotificationRecord>>(`/notifications${qs(params)}`),
  notificationStats: () =>
    call<{ pending: number; sent: number; suppressed: number; failed: number; overdue: number }>("/notifications/stats"),
  sendNotification: (body: Record<string, unknown>) =>
    call<{ status: string }>("/notifications", { method: "POST", body }),
  retryNotification: (id: string) =>
    call<{ status: string }>(`/notifications/${id}/retry`, { method: "POST" }),

  webhooks: (params: Record<string, unknown> = {}) =>
    call<Page<WebhookDelivery>>(`/webhooks${qs(params)}`),
  replayWebhook: (id: string) => call<WebhookDelivery>(`/webhooks/${id}/replay`, { method: "POST" }),
  replayDeadWebhooks: (systemId?: string) =>
    call<{ requeued: number }>(`/webhooks/replay-dead${qs({ systemId })}`, { method: "POST" }),

  // ---- reporting --------------------------------------------------------
  volumeReport: (params: Record<string, unknown> = {}) =>
    call<VolumeReport>(`/reports/volume${qs(params)}`),
  slaReport: (params: Record<string, unknown> = {}) => call<SLAReport>(`/reports/sla${qs(params)}`),
  agentReport: (params: Record<string, unknown> = {}) =>
    call<AgentReport>(`/reports/agents${qs(params)}`),
  csatReport: (params: Record<string, unknown> = {}) =>
    call<Record<string, unknown>>(`/reports/csat${qs(params)}`),
  geoReport: (params: Record<string, unknown> = {}) =>
    call<Record<string, unknown>>(`/reports/geo${qs(params)}`),
  exportUrl: (name: string, params: Record<string, unknown> = {}) =>
    `${BASE}/reports/${name}/export.csv${qs(params)}`,

  // ---- misc -------------------------------------------------------------
  search: (q: string, type?: string) => call<Listing<SearchResult>>(`/search${qs({ q, type })}`),
  savedViews: (entity?: string) => call<Listing<SavedView>>(`/saved-views${qs({ entity })}`),
  saveSavedView: (body: Partial<SavedView>) =>
    call<SavedView>(body.id ? `/saved-views/${body.id}` : "/saved-views", {
      method: body.id ? "PATCH" : "POST",
      body,
    }),
  deleteSavedView: (id: string) => call<void>(`/saved-views/${id}`, { method: "DELETE" }),

  audit: (params: Record<string, unknown> = {}) => call<Page<AuditEntry>>(`/audit${qs(params)}`),
  verifyAudit: () =>
    call<{ valid: boolean; checked: number; brokenAtSeq?: number; brokenReason?: string }>("/audit/verify"),

  jobs: () => call<{ items: JobStatus[]; enabled: boolean }>("/jobs"),
  runJob: (name: string) => call<{ result: unknown }>(`/jobs/${name}/run`, { method: "POST" }),
};
