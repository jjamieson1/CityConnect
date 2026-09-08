import { useState } from "react";
import { NavLink, Route, Routes } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api } from "@/lib/api";
import { useAuth } from "@/hooks/useAuth";
import type { Role, RoutingRule, ServiceType } from "@/lib/types";
import {
  Badge, Button, Card, cx, Empty, ErrorNote, Field, formatDateTime, Input, Modal, relativeTime,
  Select, Spinner, Textarea,
} from "@/components/ui";
import RuleEditor from "@/components/RuleEditor";

const TABS = [
  { to: "", label: "Departments" },
  { to: "queues", label: "Queues" },
  { to: "services", label: "Service catalogue" },
  { to: "rules", label: "Routing" },
  { to: "users", label: "People" },
  { to: "systems", label: "Integrations" },
  { to: "notifications", label: "Delivery log" },
  { to: "operations", label: "Operations" },
] as const;

export default function Admin() {
  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-xl font-semibold">Administration</h1>
        <p className="text-sm text-ink-muted">Configure how CityConnect routes, measures and communicates.</p>
      </div>

      <nav className="flex flex-wrap gap-1 border-b" style={{ borderColor: "var(--border)" }} aria-label="Administration">
        {TABS.map((tab) => (
          <NavLink
            key={tab.to}
            to={tab.to}
            end={tab.to === ""}
            className={({ isActive }) =>
              cx(
                "-mb-px border-b-2 px-3 py-2 text-sm font-medium",
                isActive ? "text-ink" : "border-transparent text-ink-muted hover:text-ink",
              )
            }
            style={({ isActive }) => (isActive ? { borderColor: "var(--accent)" } : undefined)}
          >
            {tab.label}
          </NavLink>
        ))}
      </nav>

      <Routes>
        <Route index element={<Departments />} />
        <Route path="queues" element={<Queues />} />
        <Route path="services" element={<Services />} />
        <Route path="rules" element={<Rules />} />
        <Route path="users" element={<People />} />
        <Route path="systems" element={<Integrations />} />
        <Route path="notifications" element={<DeliveryLog />} />
        <Route path="operations" element={<Operations />} />
      </Routes>
    </div>
  );
}

// ---------------------------------------------------------------------------

function Departments() {
  const queryClient = useQueryClient();
  const { can } = useAuth();
  const [editing, setEditing] = useState<Record<string, unknown> | null>(null);

  const list = useQuery({ queryKey: ["departments", true], queryFn: () => api.departments(true) });
  const save = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.saveDepartment(body),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["departments"] });
      setEditing(null);
    },
  });

  return (
    <Card
      title="Departments"
      actions={can("config:write") && <Button onClick={() => setEditing({ active: true })}>Add</Button>}
    >
      <p className="mb-3 text-sm text-ink-muted">
        Departments scope queues, service types and staff. They are a filter, not a wall — a
        supervisor can see and move work across all of them.
      </p>

      {list.isLoading ? <Spinner /> : (
        <table className="cc-table">
          <thead>
            <tr>
              <th>Code</th><th>Name</th><th>Public name</th><th>Contact</th><th>Status</th><th />
            </tr>
          </thead>
          <tbody>
            {(list.data?.items ?? []).map((d) => (
              <tr key={d.id}>
                <td className="font-mono text-xs">{d.code}</td>
                <td>{d.name}</td>
                <td className="text-ink-muted">{d.publicName || "—"}</td>
                <td className="text-ink-muted">{d.contactEmail || d.contactPhone || "—"}</td>
                <td>{d.active ? <Badge tone="good">Active</Badge> : <Badge>Inactive</Badge>}</td>
                <td className="text-right">
                  {can("config:write") && (
                    <Button size="sm" onClick={() => setEditing(d as unknown as Record<string, unknown>)}>Edit</Button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <Modal open={editing !== null} onClose={() => setEditing(null)} title="Department" wide>
        {editing && (
          <RecordForm
            value={editing}
            error={save.error}
            pending={save.isPending}
            onCancel={() => setEditing(null)}
            onSave={(v) => save.mutate(v)}
            fields={[
              { key: "code", label: "Code", hint: "Short identifier, e.g. PW" },
              { key: "name", label: "Name" },
              { key: "publicName", label: "Public name", hint: "Shown to citizens on their Service Card" },
              { key: "contactEmail", label: "Contact email" },
              { key: "contactPhone", label: "Contact phone" },
              { key: "address1", label: "Address" },
              { key: "city", label: "City" },
              { key: "postalCode", label: "Postal code" },
              { key: "active", label: "Active", type: "checkbox" },
            ]}
          />
        )}
      </Modal>
    </Card>
  );
}

function Queues() {
  const queryClient = useQueryClient();
  const { can } = useAuth();
  const [editing, setEditing] = useState<Record<string, unknown> | null>(null);

  const list = useQuery({ queryKey: ["queues", "all"], queryFn: () => api.queues({ includeInactive: true }) });
  const departments = useQuery({ queryKey: ["departments"], queryFn: () => api.departments() });

  const save = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.saveQueue(body),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["queues"] });
      setEditing(null);
    },
  });

  return (
    <Card
      title="Queues"
      actions={can("config:write") && <Button onClick={() => setEditing({ active: true, kind: "human", assignmentStrategy: "manual" })}>Add</Button>}
    >
      {list.isLoading ? <Spinner /> : (
        <table className="cc-table">
          <thead>
            <tr>
              <th>Code</th><th>Name</th><th>Department</th><th>Assignment</th><th className="text-right">Open</th><th />
            </tr>
          </thead>
          <tbody>
            {(list.data?.items ?? []).map((q) => (
              <tr key={q.id}>
                <td className="font-mono text-xs">{q.code}</td>
                <td>{q.name}</td>
                <td className="text-ink-muted">{q.department?.name ?? "—"}</td>
                <td>
                  <Badge>{q.assignmentStrategy.replace(/_/g, " ")}</Badge>
                </td>
                <td className="text-right tabular-nums">{q.openCount ?? 0}</td>
                <td className="text-right">
                  {can("config:write") && (
                    <Button size="sm" onClick={() => setEditing(q as unknown as Record<string, unknown>)}>Edit</Button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <Modal open={editing !== null} onClose={() => setEditing(null)} title="Queue" wide>
        {editing && (
          <RecordForm
            value={editing}
            error={save.error}
            pending={save.isPending}
            onCancel={() => setEditing(null)}
            onSave={(v) => save.mutate(v)}
            fields={[
              { key: "code", label: "Code" },
              { key: "name", label: "Name" },
              {
                key: "departmentId", label: "Department", type: "select",
                options: (departments.data?.items ?? []).map((d) => ({ value: d.id, label: d.name })),
              },
              {
                key: "assignmentStrategy", label: "Assignment", type: "select",
                hint: "Manual leaves work in the queue until somebody takes it",
                options: [
                  { value: "manual", label: "Manual — agents pull work" },
                  { value: "round_robin", label: "Round robin" },
                  { value: "least_loaded", label: "Least loaded" },
                ],
              },
              { key: "description", label: "Description", type: "textarea" },
              { key: "active", label: "Active", type: "checkbox" },
            ]}
          />
        )}
      </Modal>
    </Card>
  );
}

function Services() {
  const queryClient = useQueryClient();
  const { can } = useAuth();
  const [editing, setEditing] = useState<Record<string, unknown> | null>(null);

  const list = useQuery({
    queryKey: ["service-types", "all"],
    queryFn: () => api.serviceTypes({ includeInactive: true }),
  });
  const departments = useQuery({ queryKey: ["departments"], queryFn: () => api.departments() });
  const queues = useQuery({ queryKey: ["queues"], queryFn: () => api.queues() });
  const policies = useQuery({ queryKey: ["sla-policies"], queryFn: () => api.slaPolicies() });

  const save = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.saveServiceType(body),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["service-types"] });
      setEditing(null);
    },
  });

  return (
    <div className="space-y-4">
    <Card
      title="Service catalogue"
      actions={can("config:write") && <Button onClick={() => setEditing({ publishState: "draft", publicVisible: true, defaultPriority: "normal" })}>Add</Button>}
    >
      {list.isLoading ? <Spinner /> : (
        <table className="cc-table">
          <thead>
            <tr>
              <th>Code</th><th>Name</th><th>Category</th><th>Department</th><th>SLA</th><th>Status</th><th />
            </tr>
          </thead>
          <tbody>
            {(list.data?.items ?? []).map((st) => (
              <tr key={st.id}>
                <td className="font-mono text-xs">{st.code}</td>
                <td>{st.name}</td>
                <td className="text-ink-muted">{st.category ?? "—"}</td>
                <td className="text-ink-muted">{st.department?.name ?? "—"}</td>
                <td className="text-ink-muted">{st.slaPolicy?.name ?? "None"}</td>
                <td><PublishBadge st={st} /></td>
                <td className="text-right">
                  {can("config:write") && (
                    <Button size="sm" onClick={() => setEditing(st as unknown as Record<string, unknown>)}>Edit</Button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <Modal open={editing !== null} onClose={() => setEditing(null)} title="Service type" wide>
        {editing && (
          <RecordForm
            value={editing}
            error={save.error}
            pending={save.isPending}
            onCancel={() => setEditing(null)}
            onSave={(v) => save.mutate(v)}
            fields={[
              { key: "code", label: "Code" },
              { key: "name", label: "Name" },
              { key: "category", label: "Category" },
              { key: "description", label: "Description", type: "textarea" },
              {
                key: "departmentId", label: "Department", type: "select",
                options: (departments.data?.items ?? []).map((d) => ({ value: d.id, label: d.name })),
              },
              {
                key: "defaultQueueId", label: "Default queue", type: "select",
                options: (queues.data?.items ?? []).map((q) => ({ value: q.id, label: q.name })),
              },
              {
                key: "slaPolicyId", label: "SLA policy", type: "select",
                options: (policies.data?.items ?? []).map((p) => ({ value: p.id, label: p.name })),
              },
              {
                key: "defaultPriority", label: "Default priority", type: "select",
                options: ["low", "normal", "high", "urgent", "critical"].map((p) => ({ value: p, label: p })),
              },
              { key: "requiresLocation", label: "Requires a location", type: "checkbox" },
              { key: "publicVisible", label: "Visible to citizens", type: "checkbox" },
              {
                key: "publishState", label: "Publish state", type: "select", required: true,
                hint: "Drafts are invisible to residents. Archived keeps old requests readable.",
                options: [
                  { value: "draft", label: "Draft" },
                  { value: "published", label: "Published" },
                  { value: "archived", label: "Archived" },
                ],
              },
              {
                key: "effectiveStart", label: "Available from", type: "date",
                hint: "Leave blank for a service that runs all year.",
              },
              {
                key: "effectiveEnd", label: "Available until", type: "date",
                hint: "The last day it can be requested.",
              },
            ]}
          />
        )}
      </Modal>
    </Card>

    <PromotedServices services={list.data?.items ?? []} />
    </div>
  );
}

/**
 * Three states and a season, in one column. "Active"/"Retired" could not say
 * that a service is written but not live, or live but out of season, and an
 * operator who cannot see why a service is missing from the portal goes looking
 * in the code.
 */
function PublishBadge({ st }: { st: ServiceType }) {
  if (st.publishState === "draft") return <Badge tone="warning">Draft</Badge>;
  if (st.publishState === "archived") return <Badge>Archived</Badge>;

  const now = Date.now();
  if (st.effectiveStart && new Date(st.effectiveStart).getTime() > now) {
    return <Badge tone="warning">Starts {formatDate(st.effectiveStart)}</Badge>;
  }
  if (st.effectiveEnd && new Date(st.effectiveEnd).getTime() <= now) {
    return <Badge tone="warning">Ended {formatDate(st.effectiveEnd, true)}</Badge>;
  }
  if (st.effectiveEnd) {
    return <Badge tone="good">Live until {formatDate(st.effectiveEnd, true)}</Badge>;
  }
  return <Badge tone="good">Published</Badge>;
}

function formatDate(iso: string, exclusive = false): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  if (exclusive) d.setTime(d.getTime() - 1);
  return d.toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" });
}

/**
 * The portal's landing-view shortcuts.
 *
 * Move buttons rather than drag: a drag handle alone is unusable by keyboard
 * (WCAG 2.5.7), and building both a drag surface and a keyboard path is two
 * implementations of the same feature. Buttons are the one that works for
 * everybody.
 */
function PromotedServices({ services }: { services: ServiceType[] }) {
  const queryClient = useQueryClient();
  const { can } = useAuth();

  const promoted = services
    .filter((st) => st.promoted)
    .sort((a, b) => (a.promotedOrder ?? 0) - (b.promotedOrder ?? 0));

  // Only what SetPromoted would accept, so the console never offers a choice
  // the server will refuse.
  const eligible = services.filter(
    (st) => st.publishState === "published" && st.publicVisible && !st.promoted,
  );

  const save = useMutation({
    mutationFn: (ids: string[]) => api.setPromotedServices(ids),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["service-types"] }),
  });

  const ids = promoted.map((st) => st.id);
  const commit = (next: string[]) => save.mutate(next);
  const move = (from: number, to: number) => {
    if (to < 0 || to >= ids.length) return;
    const next = [...ids];
    [next[from], next[to]] = [next[to], next[from]];
    commit(next);
  };

  return (
    <Card title="Promoted on the portal">
      <p className="mb-3 text-sm text-ink-muted">
        Shortcuts on the portal's landing page, in this order. A city knows the three or four
        things it is asked for most; making a resident search for them helps nobody.
      </p>
      {save.error ? <ErrorNote error={save.error} /> : null}

      {promoted.length === 0 ? (
        <Empty title="Nothing is promoted" hint="Residents see the full catalogue only." />
      ) : (
        <ol className="space-y-2">
          {promoted.map((st, i) => (
            <li key={st.id} className="flex items-center gap-2 text-sm">
              <span className="w-5 text-right text-ink-faint">{i + 1}.</span>
              <span className="flex-1">{st.name}</span>
              {can("config:write") && (
                <>
                  <Button size="sm" disabled={i === 0 || save.isPending}
                    aria-label={`Move ${st.name} up`} onClick={() => move(i, i - 1)}>↑</Button>
                  <Button size="sm" disabled={i === ids.length - 1 || save.isPending}
                    aria-label={`Move ${st.name} down`} onClick={() => move(i, i + 1)}>↓</Button>
                  <Button size="sm" disabled={save.isPending}
                    aria-label={`Remove ${st.name} from the landing page`}
                    onClick={() => commit(ids.filter((id) => id !== st.id))}>Remove</Button>
                </>
              )}
            </li>
          ))}
        </ol>
      )}

      {can("config:write") && eligible.length > 0 && (
        <div className="mt-4 max-w-sm">
          <Field label="Add a shortcut">
            <Select
              value=""
              disabled={save.isPending}
              onChange={(e) => e.target.value && commit([...ids, e.target.value])}
            >
              <option value="">Choose a service…</option>
              {eligible.map((st) => (
                <option key={st.id} value={st.id}>{st.name}</option>
              ))}
            </Select>
          </Field>
        </div>
      )}
    </Card>
  );
}

function Rules() {
  const queryClient = useQueryClient();
  const { can } = useAuth();
  const [editing, setEditing] = useState<Partial<RoutingRule> | null>(null);

  const list = useQuery({ queryKey: ["rules"], queryFn: () => api.rules(true) });

  const save = useMutation({
    mutationFn: (body: Partial<RoutingRule>) => api.saveRule(body),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["rules"] });
      setEditing(null);
    },
  });

  const remove = useMutation({
    mutationFn: (id: string) => api.deleteRule(id),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["rules"] }),
  });

  return (
    <div className="space-y-4">
      <Card
        title="Routing rules"
        actions={
          can("config:write") && (
            <Button onClick={() => setEditing({ name: "", priority: 100, active: false, conditions: { all: [] }, actions: {} })}>
              Add rule
            </Button>
          )
        }
      >
        <p className="mb-3 text-sm text-ink-muted">
          Rules run in priority order on every new request; the first match wins unless it is
          marked to continue. Always simulate before activating one — a rule that matches more
          than you expect will quietly redirect a queue's entire workload.
        </p>

        {list.isLoading ? <Spinner /> : (list.data?.items.length ?? 0) === 0 ? (
          <Empty title="No routing rules" hint="Requests fall through to their service type's default queue." />
        ) : (
          <table className="cc-table">
            <thead>
              <tr>
                <th className="w-16">Order</th><th>Name</th><th>Status</th>
                <th className="text-right">Matches</th><th>Last fired</th><th />
              </tr>
            </thead>
            <tbody>
              {(list.data?.items ?? []).map((rule) => (
                <tr key={rule.id}>
                  <td className="tabular-nums">{rule.priority}</td>
                  <td>
                    <span className="font-medium">{rule.name}</span>
                    {rule.description && (
                      <span className="block text-xs text-ink-faint">{rule.description}</span>
                    )}
                  </td>
                  <td>
                    {rule.active ? <Badge tone="good">Active</Badge> : <Badge tone="warning">Draft</Badge>}
                    {rule.continue && <Badge>Continues</Badge>}
                  </td>
                  <td className="text-right tabular-nums">{rule.matchCount}</td>
                  <td className="whitespace-nowrap text-ink-muted">
                    {rule.lastMatchedAt ? relativeTime(rule.lastMatchedAt) : "Never"}
                  </td>
                  <td className="space-x-1 text-right">
                    {can("config:write") && (
                      <>
                        <Button size="sm" onClick={() => setEditing(rule)}>Edit</Button>
                        <Button
                          size="sm"
                          onClick={() => {
                            if (confirm(`Delete the rule "${rule.name}"?`)) remove.mutate(rule.id);
                          }}
                        >
                          Delete
                        </Button>
                      </>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>

      <Modal open={editing !== null} onClose={() => setEditing(null)} title="Routing rule" wide>
        {editing && (
          <RuleEditor
            rule={editing}
            saving={save.isPending}
            error={save.error}
            onCancel={() => setEditing(null)}
            onSave={(r) => save.mutate(r)}
          />
        )}
      </Modal>
    </div>
  );
}

function People() {
  const queryClient = useQueryClient();
  const { can } = useAuth();
  const [inviteOpen, setInviteOpen] = useState(false);

  const list = useQuery({ queryKey: ["users", "admin"], queryFn: () => api.users({ limit: 200 }) });
  const departments = useQuery({ queryKey: ["departments"], queryFn: () => api.departments() });

  const update = useMutation({
    mutationFn: ({ id, body }: { id: string; body: Record<string, unknown> }) => api.updateUser(id, body),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["users"] }),
  });

  return (
    <Card
      title="People"
      actions={can("user:manage") && <Button onClick={() => setInviteOpen(true)}>Invite</Button>}
    >
      <p className="mb-3 text-sm text-ink-muted">
        Staff sign in with C2 single sign-on. An invitation creates the record; it binds to their
        C2 identity the first time they sign in. There is no password to set or reset.
      </p>

      {update.error && <ErrorNote error={update.error} />}

      {list.isLoading ? <Spinner /> : (
        <div className="overflow-x-auto">
          <table className="cc-table">
            <thead>
              <tr>
                <th>Name</th><th>Email</th><th>Role</th><th>Department</th>
                <th>Status</th><th>Last signed in</th>
              </tr>
            </thead>
            <tbody>
              {(list.data?.items ?? []).map((u) => (
                <tr key={u.id}>
                  <td>{u.name || "—"}</td>
                  <td className="text-ink-muted">{u.email}</td>
                  <td>
                    {can("user:manage") ? (
                      <Select
                        className="!w-auto !py-1 text-xs"
                        value={u.role}
                        onChange={(e) => update.mutate({ id: u.id, body: { role: e.target.value as Role } })}
                      >
                        {["readonly", "agent", "supervisor", "admin"].map((r) => (
                          <option key={r} value={r}>{r}</option>
                        ))}
                      </Select>
                    ) : (
                      <Badge>{u.role}</Badge>
                    )}
                  </td>
                  <td className="text-ink-muted">{u.department?.name ?? "—"}</td>
                  <td>
                    {u.status === "active" && <Badge tone="good">Active</Badge>}
                    {u.status === "invited" && <Badge tone="warning">Invited — not yet signed in</Badge>}
                    {u.status === "suspended" && <Badge tone="critical">Suspended</Badge>}
                  </td>
                  <td className="whitespace-nowrap text-ink-muted">
                    {u.lastLoginAt ? relativeTime(u.lastLoginAt) : "Never"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <InviteDialog
        open={inviteOpen}
        departments={(departments.data?.items ?? []).map((d) => ({ value: d.id, label: d.name }))}
        onClose={() => setInviteOpen(false)}
        onInvited={() => {
          setInviteOpen(false);
          void queryClient.invalidateQueries({ queryKey: ["users"] });
        }}
      />
    </Card>
  );
}

function InviteDialog({ open, departments, onClose, onInvited }: {
  open: boolean;
  departments: { value: string; label: string }[];
  onClose: () => void;
  onInvited: () => void;
}) {
  const [form, setForm] = useState({ email: "", name: "", role: "agent", departmentId: "" });

  const invite = useMutation({
    mutationFn: () => api.invite(form),
    onSuccess: onInvited,
  });

  return (
    <Modal open={open} onClose={onClose} title="Invite a colleague">
      <div className="space-y-3">
        {invite.error && <ErrorNote error={invite.error} />}
        <p className="text-sm text-ink-muted">
          Use the email address on their C2 account. CityConnect matches it once, at their first
          sign-in, and then remembers their C2 identity permanently.
        </p>

        <Field label="Email">
          <Input type="email" value={form.email} onChange={(e) => setForm({ ...form, email: e.target.value })} />
        </Field>
        <Field label="Name">
          <Input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
        </Field>
        <Field label="Role">
          <Select value={form.role} onChange={(e) => setForm({ ...form, role: e.target.value })}>
            <option value="readonly">Read only — can view, cannot change</option>
            <option value="agent">Agent — works requests</option>
            <option value="supervisor">Supervisor — can transfer and merge</option>
            <option value="admin">Administrator — full configuration</option>
          </Select>
        </Field>
        <Field label="Department">
          <Select value={form.departmentId} onChange={(e) => setForm({ ...form, departmentId: e.target.value })}>
            <option value="">No department</option>
            {departments.map((d) => <option key={d.value} value={d.value}>{d.label}</option>)}
          </Select>
        </Field>

        <div className="flex justify-end gap-2 pt-1">
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" disabled={!form.email.includes("@") || invite.isPending} onClick={() => invite.mutate()}>
            {invite.isPending ? "Inviting…" : "Invite"}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function Integrations() {
  const queryClient = useQueryClient();
  const [secret, setSecret] = useState<string | null>(null);

  const systems = useQuery({ queryKey: ["systems"], queryFn: () => api.systems() });
  const webhooks = useQuery({
    queryKey: ["webhooks", "dead"],
    queryFn: () => api.webhooks({ state: "dead", limit: 25 }),
  });

  const rotate = useMutation({
    mutationFn: (id: string) => api.rotateSecret(id),
    onSuccess: (res) => setSecret(res.webhookSecret),
  });

  const replay = useMutation({
    mutationFn: (id: string) => api.replayWebhook(id),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["webhooks"] }),
  });

  return (
    <div className="space-y-4">
      <Card title="Connected systems">
        <p className="mb-3 text-sm text-ink-muted">
          Line-of-business applications that receive requests and event webhooks. A connected
          system can own a request exactly as a person can.
        </p>
        {systems.isLoading ? <Spinner /> : (systems.data?.items.length ?? 0) === 0 ? (
          <Empty title="No connected systems yet" />
        ) : (
          <table className="cc-table">
            <thead>
              <tr><th>Code</th><th>Name</th><th>Webhook</th><th>Events</th><th>Status</th><th /></tr>
            </thead>
            <tbody>
              {systems.data!.items.map((s) => (
                <tr key={s.id}>
                  <td className="font-mono text-xs">{s.code}</td>
                  <td>{s.name}</td>
                  <td className="max-w-xs truncate text-ink-muted">{s.webhookUrl || "—"}</td>
                  <td>
                    <div className="flex flex-wrap gap-1">
                      {s.webhookEvents.map((e) => <Badge key={e}>{e}</Badge>)}
                    </div>
                  </td>
                  <td>{s.active ? <Badge tone="good">Active</Badge> : <Badge>Disabled</Badge>}</td>
                  <td className="text-right">
                    <Button size="sm" onClick={() => rotate.mutate(s.id)}>Rotate secret</Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>

      <Card
        title="Failed webhook deliveries"
        actions={
          (webhooks.data?.total ?? 0) > 0 && (
            <Button
              size="sm"
              onClick={() => {
                if (confirm("Re-queue every failed delivery?")) {
                  void api.replayDeadWebhooks().then(() =>
                    queryClient.invalidateQueries({ queryKey: ["webhooks"] }),
                  );
                }
              }}
            >
              Replay all
            </Button>
          )
        }
      >
        <p className="mb-3 text-sm text-ink-muted">
          Deliveries that exhausted their retries. They are kept so a partner outage does not
          silently lose the events that happened during it — replay once their endpoint is back.
        </p>
        {webhooks.isLoading ? <Spinner /> : (webhooks.data?.items.length ?? 0) === 0 ? (
          <Empty title="Nothing in the dead-letter queue" />
        ) : (
          <table className="cc-table">
            <thead>
              <tr><th>Event</th><th>URL</th><th>Attempts</th><th>Last error</th><th>When</th><th /></tr>
            </thead>
            <tbody>
              {webhooks.data!.items.map((d) => (
                <tr key={d.id}>
                  <td>{d.event}</td>
                  <td className="max-w-xs truncate text-ink-muted">{d.url}</td>
                  <td className="tabular-nums">{d.attempts}</td>
                  <td className="max-w-xs truncate text-ink-muted">{d.lastError}</td>
                  <td className="whitespace-nowrap text-ink-muted">{relativeTime(d.createdAt)}</td>
                  <td className="text-right">
                    <Button size="sm" disabled={replay.isPending} onClick={() => replay.mutate(d.id)}>
                      Replay
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>

      <Modal open={secret !== null} onClose={() => setSecret(null)} title="New webhook secret">
        <p className="text-sm text-ink-muted">
          Copy this now — it is stored only as a hash and cannot be shown again.
        </p>
        <code className="mt-3 block break-all rounded p-3 text-sm" style={{ background: "var(--surface-0)" }}>
          {secret}
        </code>
        <div className="mt-3 flex justify-end">
          <Button variant="primary" onClick={() => setSecret(null)}>Done</Button>
        </div>
      </Modal>
    </div>
  );
}

function DeliveryLog() {
  const queryClient = useQueryClient();
  const [state, setState] = useState("");

  const stats = useQuery({ queryKey: ["notification-stats"], queryFn: () => api.notificationStats() });
  const list = useQuery({
    queryKey: ["notifications", state],
    queryFn: () => api.notifications({ state: state || undefined, limit: 50 }),
  });

  const retry = useMutation({
    mutationFn: (id: string) => api.retryNotification(id),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["notifications"] }),
  });

  return (
    <div className="space-y-4">
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-5">
        <Stat label="Sent" value={stats.data?.sent} />
        <Stat label="Waiting" value={stats.data?.pending} />
        <Stat label="Suppressed" value={stats.data?.suppressed} tone="warning" />
        <Stat label="Failed" value={stats.data?.failed} tone="critical" />
        <Stat label="Overdue" value={stats.data?.overdue} tone={(stats.data?.overdue ?? 0) > 0 ? "critical" : "neutral"} />
      </div>

      <Card
        title="Citizen notifications"
        actions={
          <Select className="!w-auto !py-1 text-xs" value={state} onChange={(e) => setState(e.target.value)}>
            <option value="">All</option>
            <option value="sent">Sent</option>
            <option value="pending">Waiting</option>
            <option value="suppressed">Suppressed</option>
            <option value="failed">Failed</option>
          </Select>
        }
      >
        <p className="mb-3 text-sm text-ink-muted">
          Suppressed messages were refused by C2's consent gate. That is expected, not an error —
          the citizen must re-consent in their own portal before anything can be delivered.
        </p>

        {retry.error && <ErrorNote error={retry.error} />}

        {list.isLoading ? <Spinner /> : (
          <div className="overflow-x-auto">
            <table className="cc-table">
              <thead>
                <tr><th>Subject</th><th>Event</th><th>State</th><th>Channels</th><th>When</th><th /></tr>
              </thead>
              <tbody>
                {(list.data?.items ?? []).map((n) => (
                  <tr key={n.id}>
                    <td className="max-w-xs truncate">{n.subject}</td>
                    <td className="text-ink-muted">{n.event ?? "—"}</td>
                    <td>
                      {n.state === "sent" && <Badge tone="good">Sent</Badge>}
                      {n.state === "pending" && <Badge>Waiting</Badge>}
                      {n.state === "failed" && <Badge tone="critical">Failed</Badge>}
                      {n.state === "suppressed" && (
                        <Badge tone="warning">
                          {n.suppressReason === "no_consent" ? "No consent"
                            : n.suppressReason === "unknown_sub" ? "Unknown to C2"
                            : n.suppressReason === "no_c2_identity" ? "No C2 account"
                            : "Suppressed"}
                        </Badge>
                      )}
                    </td>
                    <td className="text-ink-muted">{n.channels?.join(", ") || "—"}</td>
                    <td className="whitespace-nowrap text-ink-muted">
                      {formatDateTime(n.sentAt ?? n.createdAt)}
                    </td>
                    <td className="text-right">
                      {n.state === "failed" && (
                        <Button size="sm" disabled={retry.isPending} onClick={() => retry.mutate(n.id)}>
                          Retry
                        </Button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </div>
  );
}

function Operations() {
  const queryClient = useQueryClient();

  const jobs = useQuery({ queryKey: ["jobs"], queryFn: () => api.jobs(), refetchInterval: 30_000 });
  const auditCheck = useQuery({ queryKey: ["audit-verify"], queryFn: () => api.verifyAudit() });
  const audit = useQuery({ queryKey: ["audit"], queryFn: () => api.audit({ limit: 25 }) });

  const run = useMutation({
    mutationFn: (name: string) => api.runJob(name),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["jobs"] }),
  });

  return (
    <div className="space-y-4">
      <Card title="Background jobs">
        {!jobs.data?.enabled && (
          <p className="mb-3 text-sm" style={{ color: "var(--status-warning)" }}>
            ⚑ Background jobs are disabled. SLA deadlines, notifications and reporting rollups
            will not run.
          </p>
        )}
        {jobs.isLoading ? <Spinner /> : (
          <table className="cc-table">
            <thead>
              <tr><th>Job</th><th>Last run</th><th>Took</th><th>Result</th><th /></tr>
            </thead>
            <tbody>
              {(jobs.data?.items ?? []).map((j) => (
                <tr key={j.name}>
                  <td className="font-medium">{j.name}</td>
                  <td className="whitespace-nowrap text-ink-muted">{relativeTime(j.lastRun)}</td>
                  <td className="text-ink-muted">{j.duration}</td>
                  <td>
                    {j.error
                      ? <Badge tone="critical">{j.error}</Badge>
                      : <Badge tone="good">OK</Badge>}
                  </td>
                  <td className="text-right">
                    <Button size="sm" disabled={run.isPending} onClick={() => run.mutate(j.name)}>
                      Run now
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>

      <Card title="Audit log">
        {auditCheck.data && (
          <p
            className="mb-3 rounded px-3 py-2 text-sm"
            style={{
              background: auditCheck.data.valid ? "var(--status-good-bg)" : "var(--status-critical-bg)",
              color: auditCheck.data.valid ? "var(--status-good)" : "var(--status-critical)",
            }}
          >
            {auditCheck.data.valid
              ? `✓ Chain intact — ${auditCheck.data.checked.toLocaleString()} entries verified.`
              : `⚠ Chain broken at entry ${auditCheck.data.brokenAtSeq}: ${auditCheck.data.brokenReason}`}
          </p>
        )}

        {audit.isLoading ? <Spinner /> : (
          <table className="cc-table">
            <thead>
              <tr><th className="w-16">#</th><th>Action</th><th>Who</th><th>What</th><th>When</th></tr>
            </thead>
            <tbody>
              {(audit.data?.items ?? []).map((e) => (
                <tr key={e.id}>
                  <td className="tabular-nums text-ink-faint">{e.seq}</td>
                  <td className="font-mono text-xs">{e.action}</td>
                  <td className="text-ink-muted">{e.actorLabel || e.actorType}</td>
                  <td className="max-w-md truncate">{e.summary}</td>
                  <td className="whitespace-nowrap text-ink-muted">{formatDateTime(e.createdAt)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>
    </div>
  );
}

function Stat({ label, value, tone = "neutral" }: {
  label: string;
  value?: number;
  tone?: "neutral" | "warning" | "critical";
}) {
  const color =
    tone === "critical" ? "var(--status-critical)"
    : tone === "warning" ? "var(--status-warning)"
    : "var(--text-primary)";
  return (
    <div className="cc-card p-3">
      <p className="text-xs uppercase tracking-wide text-ink-muted">{label}</p>
      <p className="mt-1 text-2xl font-semibold tabular-nums" style={{ color }}>
        {value?.toLocaleString() ?? "—"}
      </p>
    </div>
  );
}

// ---------------------------------------------------------------------------
// A small declarative record form, so each admin screen describes its fields
// rather than repeating the same markup eight times.
// ---------------------------------------------------------------------------

interface FormFieldSpec {
  key: string;
  label: string;
  hint?: string;
  type?: "text" | "textarea" | "checkbox" | "select" | "date";
  options?: { value: string; label: string }[];
  /**
   * Drops the blank "None" option. A select whose empty value is meaningless —
   * publish state, say — should not offer it, because picking it looks like a
   * choice and behaves like a default.
   */
  required?: boolean;
}

/**
 * The server stores availability dates as instants; an operator thinks in days.
 * These convert between the two in the operator's own time zone, so "April 1"
 * means midnight where they are rather than midnight in UTC — which for a
 * Pacific city would start the service at five in the afternoon of March 31.
 */
function dateInputValue(iso: unknown, exclusive = false): string {
  if (typeof iso !== "string" || iso === "") return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  // An exclusive bound is stored as the first instant *after* the last day it
  // covers, so step back inside the window before reading the date off it.
  // Stepping back a millisecond rather than a whole day is right for both the
  // midnight this console writes and an arbitrary instant set through the API.
  if (exclusive) d.setTime(d.getTime() - 1);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

function startOfLocalDay(value: string): string | null {
  if (!value) return null;
  const [y, m, d] = value.split("-").map(Number);
  return new Date(y, m - 1, d, 0, 0, 0, 0).toISOString();
}

/**
 * The closing date is inclusive to the operator and exclusive to the server, so
 * "available until September 30" is stored as the first instant of October 1.
 * Storing the start of the 30th instead would withdraw the service a day early,
 * which is the kind of off-by-one nobody notices until a resident complains.
 */
function endOfLocalDay(value: string): string | null {
  if (!value) return null;
  const [y, m, d] = value.split("-").map(Number);
  return new Date(y, m - 1, d + 1, 0, 0, 0, 0).toISOString();
}

function RecordForm({ value, fields, onSave, onCancel, pending, error }: {
  value: Record<string, unknown>;
  fields: FormFieldSpec[];
  onSave: (value: Record<string, unknown>) => void;
  onCancel: () => void;
  pending: boolean;
  error: unknown;
}) {
  const [form, setForm] = useState(value);
  const set = (key: string, v: unknown) => setForm((prev) => ({ ...prev, [key]: v }));

  return (
    <div className="space-y-3">
      {error ? <ErrorNote error={error} /> : null}
      <div className="grid gap-3 sm:grid-cols-2">
        {fields.map((f) => {
          if (f.type === "checkbox") {
            return (
              <label key={f.key} className="flex items-center gap-2 self-end pb-2 text-sm">
                <input
                  type="checkbox"
                  checked={Boolean(form[f.key])}
                  onChange={(e) => set(f.key, e.target.checked)}
                />
                {f.label}
              </label>
            );
          }
          if (f.type === "select") {
            return (
              <Field key={f.key} label={f.label} hint={f.hint}>
                <Select value={String(form[f.key] ?? "")} onChange={(e) => set(f.key, e.target.value)}>
                  {!f.required && <option value="">None</option>}
                  {(f.options ?? []).map((o) => (
                    <option key={o.value} value={o.value}>{o.label}</option>
                  ))}
                </Select>
              </Field>
            );
          }
          if (f.type === "date") {
            return (
              <Field key={f.key} label={f.label} hint={f.hint}>
                <Input
                  type="date"
                  value={dateInputValue(form[f.key], f.key === "effectiveEnd")}
                  onChange={(e) =>
                    set(f.key, f.key === "effectiveEnd"
                      ? endOfLocalDay(e.target.value)
                      : startOfLocalDay(e.target.value))
                  }
                />
              </Field>
            );
          }
          if (f.type === "textarea") {
            return (
              <div key={f.key} className="sm:col-span-2">
                <Field label={f.label} hint={f.hint}>
                  <Textarea rows={2} value={String(form[f.key] ?? "")} onChange={(e) => set(f.key, e.target.value)} />
                </Field>
              </div>
            );
          }
          return (
            <Field key={f.key} label={f.label} hint={f.hint}>
              <Input value={String(form[f.key] ?? "")} onChange={(e) => set(f.key, e.target.value)} />
            </Field>
          );
        })}
      </div>

      <div className="flex justify-end gap-2 pt-1">
        <Button onClick={onCancel}>Cancel</Button>
        <Button variant="primary" disabled={pending} onClick={() => onSave(form)}>
          {pending ? "Saving…" : "Save"}
        </Button>
      </div>
    </div>
  );
}
