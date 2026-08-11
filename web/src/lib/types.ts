// Shapes mirrored from internal/domain. Kept hand-written rather than
// generated so the client can carry the small conveniences the console needs
// (optional expansions, discriminated unions) without fighting a generator.

export type Role = "readonly" | "agent" | "supervisor" | "admin";
export type UserStatus = "invited" | "active" | "suspended";

export type RequestStatus =
  | "new"
  | "triaged"
  | "assigned"
  | "in_progress"
  | "waiting_citizen"
  | "waiting_third_party"
  | "resolved"
  | "closed"
  | "cancelled";

export type Priority = "low" | "normal" | "high" | "urgent" | "critical";
export type Visibility = "internal" | "citizen";

export interface Department {
  id: string;
  name: string;
  code: string;
  publicName?: string;
  contactEmail?: string;
  contactPhone?: string;
  address1?: string;
  city?: string;
  state?: string;
  postalCode?: string;
  active: boolean;
  sortOrder: number;
  description?: string;
}

export interface User {
  id: string;
  c2Sub?: string;
  email: string;
  name: string;
  title?: string;
  phone?: string;
  status: UserStatus;
  role: Role;
  departmentId?: string;
  crossDepartment: boolean;
  lastLoginAt?: string;
  department?: Department;
  queues?: Queue[];
}

export interface Queue {
  id: string;
  name: string;
  code: string;
  description?: string;
  departmentId?: string;
  kind: "human" | "system";
  assignmentStrategy: "manual" | "round_robin" | "least_loaded";
  escalationQueueId?: string;
  active: boolean;
  sortOrder: number;
  openCount?: number;
  department?: Department;
  members?: User[];
}

export interface ServiceType {
  id: string;
  code: string;
  name: string;
  category?: string;
  description?: string;
  departmentId?: string;
  defaultQueueId?: string;
  slaPolicyId?: string;
  defaultPriority: Priority;
  requiresLocation: boolean;
  allowsAttachments: boolean;
  publicVisible: boolean;
  active: boolean;
  intakeForm?: { fields?: FormField[] };
  department?: Department;
  slaPolicy?: SLAPolicy;
}

export interface FormField {
  key: string;
  label: string;
  type: "text" | "textarea" | "number" | "date" | "select" | "multiselect" | "checkbox";
  required?: boolean;
  options?: string[];
  help?: string;
}

export interface SLAPolicy {
  id: string;
  name: string;
  description?: string;
  calendarId?: string;
  firstResponseMinutes: number;
  resolutionMinutes: number;
  pauseStatuses: string[];
  warnAtPercent: number;
  active: boolean;
}

export interface BusinessCalendar {
  id: string;
  name: string;
  timeZone: string;
  hours?: Record<string, { start: string; end: string }[]>;
  holidays?: string[];
  alwaysOpen: boolean;
  isDefault: boolean;
}

export interface Contact {
  id: string;
  displayName: string;
  givenName?: string;
  familyName?: string;
  organization?: string;
  status: "active" | "inactive" | "merged";
  primaryEmail?: string;
  primaryPhone?: string;
  preferredLanguage: string;
  doNotContact: boolean;
  c2Reachable: boolean;
  c2UnreachableCode?: string;
  address1?: string;
  address2?: string;
  city?: string;
  state?: string;
  postalCode?: string;
  ward?: string;
  notes?: string;
  tags: string[];
  version: number;
  mergedIntoId?: string;
  identities?: ContactIdentity[];
  channels?: ContactChannel[];
  createdAt: string;
  updatedAt: string;
}

export interface ContactIdentity {
  id: string;
  contactId: string;
  provider: string;
  externalId: string;
  label?: string;
  verified: boolean;
}

export interface ContactChannel {
  id: string;
  contactId: string;
  kind: "email" | "phone" | "sms" | "address";
  value: string;
  label?: string;
  verified: boolean;
  isPrimary: boolean;
}

export interface ServiceRequest {
  id: string;
  reference: string;
  contactId: string;
  serviceTypeId: string;
  departmentId?: string;
  queueId?: string;
  assigneeUserId?: string;
  assigneeSystemId?: string;
  source: string;
  originSystem?: string;
  externalRef?: string;
  status: RequestStatus;
  priority: Priority;
  subject: string;
  description?: string;
  address1?: string;
  city?: string;
  postalCode?: string;
  ward?: string;
  latitude?: number;
  longitude?: number;
  formData?: Record<string, unknown>;
  tags: string[];
  openedAt: string;
  firstResponseAt?: string;
  resolvedAt?: string;
  closedAt?: string;
  dueAt?: string;
  responseDueAt?: string;
  slaBreached: boolean;
  slaWarned: boolean;
  resolutionCode?: string;
  resolutionNote?: string;
  csatScore?: number;
  reopenCount: number;
  lastActivityAt: string;
  mergedIntoId?: string;
  version: number;
  contact?: Contact;
  serviceType?: ServiceType;
  queue?: Queue;
  department?: Department;
  assigneeUser?: User;
}

export interface RequestComment {
  id: string;
  requestId: string;
  authorId?: string;
  authorType: string;
  authorName?: string;
  visibility: Visibility;
  body: string;
  createdAt: string;
}

export interface RequestEvent {
  id: string;
  requestId: string;
  kind: string;
  actorName?: string;
  summary?: string;
  fromValue?: string;
  toValue?: string;
  detail?: Record<string, unknown>;
  citizenVisible: boolean;
  createdAt: string;
}

export interface RequestLink {
  id: string;
  requestId: string;
  targetId: string;
  kind: "duplicate_of" | "related_to" | "child_of";
  note?: string;
  targetRef?: string;
  targetSubject?: string;
}

export interface Attachment {
  id: string;
  requestId: string;
  filename: string;
  contentType: string;
  sizeBytes: number;
  visibility: Visibility;
  scanStatus: string;
  createdAt: string;
}

export interface Interaction {
  id: string;
  contactId: string;
  requestId?: string;
  userId?: string;
  kind: "call" | "email" | "meeting" | "sms" | "portal" | "note" | "walk_in";
  direction: "inbound" | "outbound" | "internal";
  subject?: string;
  summary?: string;
  occurredAt: string;
  durationSeconds?: number;
  outcome?: string;
  tags: string[];
  user?: User;
}

export interface TimelineEntry {
  id: string;
  type: "interaction" | "request_event" | "comment" | "notification";
  at: string;
  title: string;
  body?: string;
  actorName?: string;
  requestId?: string;
  reference?: string;
  kind?: string;
}

export interface Macro {
  id: string;
  name: string;
  description?: string;
  departmentId?: string;
  body: string;
  visibility: Visibility;
  setStatus?: string;
  setPriority?: Priority;
  addTags: string[];
  notifyCitizen: boolean;
  active: boolean;
  usageCount: number;
}

export interface RoutingRule {
  id: string;
  name: string;
  description?: string;
  priority: number;
  active: boolean;
  continue: boolean;
  conditions?: { all?: Condition[]; any?: Condition[] };
  actions?: RuleActions;
  matchCount: number;
  lastMatchedAt?: string;
}

export interface Condition {
  field: string;
  op: string;
  value?: string;
  list?: string[];
}

export interface RuleActions {
  queueId?: string;
  assigneeUserId?: string;
  assigneeSystemId?: string;
  departmentId?: string;
  priority?: string;
  slaPolicyId?: string;
  addTags?: string[];
  setStatus?: string;
  notify?: boolean;
}

export interface ConnectedSystem {
  id: string;
  name: string;
  code: string;
  description?: string;
  departmentId?: string;
  baseUrl?: string;
  webhookUrl?: string;
  webhookEvents: string[];
  contactEmail?: string;
  active: boolean;
}

export interface ApiToken {
  id: string;
  name: string;
  prefix: string;
  ownerUserId?: string;
  systemId?: string;
  scopes: string[];
  readOnly: boolean;
  expiresAt?: string;
  lastUsedAt?: string;
  createdAt: string;
}

export interface NotificationRecord {
  id: string;
  contactId: string;
  requestId?: string;
  c2Sub: string;
  event?: string;
  subject: string;
  body: string;
  state: "pending" | "sent" | "failed" | "suppressed";
  attempts: number;
  sentAt?: string;
  c2NotificationId?: string;
  channels: string[];
  lastStatusCode?: number;
  lastError?: string;
  suppressReason?: string;
  createdAt: string;
}

export interface WebhookDelivery {
  id: string;
  systemId: string;
  event: string;
  requestId?: string;
  url: string;
  state: "pending" | "sent" | "failed" | "dead";
  attempts: number;
  deliveredAt?: string;
  lastStatusCode?: number;
  lastError?: string;
  createdAt: string;
}

export interface NotificationTemplate {
  id: string;
  event: string;
  language: string;
  serviceTypeId?: string;
  subject: string;
  body: string;
  shortBody?: string;
  category: string;
  active: boolean;
  description?: string;
}

export interface SavedView {
  id: string;
  name: string;
  entity: string;
  ownerId?: string;
  shared: boolean;
  filters?: Record<string, unknown>;
  sortBy?: string;
  sortDir?: string;
  isDefault: boolean;
}

export interface AuditEntry {
  id: string;
  seq: number;
  actorType: string;
  actorLabel?: string;
  action: string;
  targetType?: string;
  targetId?: string;
  summary?: string;
  changes?: Record<string, unknown>;
  createdAt: string;
}

export interface JobStatus {
  name: string;
  lastRun: string;
  duration: string;
  error?: string;
  detail?: unknown;
}

export interface Me {
  user: User | null;
  department?: Department;
  queues: Queue[];
  permissions: string[];
  isSystem: boolean;
  crossDepartment: boolean;
}

export interface Page<T> {
  items: T[];
  total: number;
  limit: number;
  offset: number;
  hasMore: boolean;
}

export interface Listing<T> {
  items: T[];
}

// Reports

export interface VolumeReport {
  range: { from: string; to: string };
  series: { day: string; opened: number; closed: number }[];
  totalOpen: number;
  totalNew: number;
  totalClosed: number;
  byServiceType: Breakdown[];
  byStatus: Breakdown[];
  bySource: Breakdown[];
  byPriority: Breakdown[];
  byQueue: Breakdown[];
}

export interface Breakdown {
  label: string;
  count: number;
}

export interface SLAReport {
  range: { from: string; to: string };
  total: number;
  met: number;
  breached: number;
  compliancePct: number;
  responseBreached: number;
  avgResolutionHours: number;
  p90ResolutionHours: number;
  avgFirstResponseHours: number;
  byServiceType: {
    label: string;
    total: number;
    breached: number;
    compliancePct: number;
  }[];
  openBreached: number;
  atRisk: number;
}

export interface AgentReport {
  range: { from: string; to: string };
  note: string;
  rows: {
    userId: string;
    name: string;
    assigned: number;
    closed: number;
    openNow: number;
    breached: number;
    avgResolutionHours: number;
    csatAverage?: number;
    csatResponses?: number;
  }[];
}

export interface SimulationResult {
  sampled: number;
  changed: number;
  unrouted: number;
  ruleHits: Record<string, number>;
  truncated: boolean;
  cases: {
    requestId: string;
    reference: string;
    subject: string;
    currentQueueId?: string;
    proposedQueueId?: string;
    changed: boolean;
    unrouted: boolean;
    matchedRules?: string[];
    proposedPriority?: string;
  }[];
}

export interface DuplicateCandidate {
  contact: Contact;
  score: number;
  reasons: string[];
}

export interface SearchResult {
  type: "request" | "contact";
  id: string;
  title: string;
  subtitle?: string;
  reference?: string;
  status?: string;
}

// ---------------------------------------------------------------------------
// Citizen portal
// ---------------------------------------------------------------------------

export interface CatalogEntry {
  id: string;
  code: string;
  name: string;
  category?: string;
  description?: string;
  department?: string;
  requiresLocation: boolean;
  fields: FormField[];
}

export interface MyRequest {
  reference: string;
  subject: string;
  description?: string;
  serviceType?: string;
  department?: string;
  status: RequestStatus;
  statusLabel: string;
  open: boolean;
  address?: string;
  openedAt: string;
  updatedAt: string;
  expectedBy?: string;
  resolvedAt?: string;
  resolution?: string;
  canCancel: boolean;
  canComment: boolean;
  canRate: boolean;
  csatScore?: number;
  updates?: MyUpdate[];
}

export interface MyUpdate {
  at: string;
  kind: "note" | "status";
  body: string;
  author?: string;
  mine: boolean;
}

export interface PortalProfile {
  name: string;
  email?: string;
  phone?: string;
  address?: string;
  openRequests: number;
}
