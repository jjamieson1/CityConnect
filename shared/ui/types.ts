// The few types the shared primitives depend on. Each application keeps its
// own full API types; only what the UI kit touches lives here.

export type RequestStatus =
  | "new" | "triaged" | "assigned" | "in_progress"
  | "waiting_citizen" | "waiting_third_party"
  | "resolved" | "closed" | "cancelled";

export type Priority = "low" | "normal" | "high" | "urgent" | "critical";
