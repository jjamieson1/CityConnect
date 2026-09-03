// Only what a citizen's own view needs. The console's full domain types are
// deliberately absent: this bundle is public, and every extra type is a
// description of the internal model handed to anyone who reads it.

export type RequestStatus =
  | "new" | "triaged" | "assigned" | "in_progress"
  | "waiting_citizen" | "waiting_third_party"
  | "resolved" | "closed" | "cancelled";

export interface FormField {
  key: string;
  label: string;
  type: "text" | "textarea" | "number" | "date" | "select" | "multiselect" | "checkbox";
  required?: boolean;
  options?: string[];
  help?: string;
}

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

export interface MyUpdate {
  at: string;
  kind: "note" | "status";
  body: string;
  author?: string;
  mine: boolean;
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
  /** False for an anonymous report: nothing to verify a later lookup against. */
  trackable: boolean;
  canCancel: boolean;
  canComment: boolean;
  canRate: boolean;
  csatScore?: number;
  updates?: MyUpdate[];
}

export interface PortalProfile {
  name: string;
  email?: string;
  phone?: string;
  address?: string;
  openRequests: number;
}
