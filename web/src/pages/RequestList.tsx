import { useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api } from "@/lib/api";
import type { RequestStatus } from "@/lib/types";
import { useAuth } from "@/hooks/useAuth";
import {
  Button, Card, Empty, ErrorNote, Field, Input, Modal, Pager, PriorityBadge,
  relativeTime, Select, SLABadge, Spinner, StatusBadge, STATUS_LABELS,
} from "@/components/ui";
import NewRequestForm from "@/components/NewRequestForm";

const OPEN_STATUSES: RequestStatus[] = [
  "new", "triaged", "assigned", "in_progress", "waiting_citizen", "waiting_third_party",
];

export default function RequestList() {
  const [params, setParams] = useSearchParams();
  const { me, can } = useAuth();
  const queryClient = useQueryClient();

  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [bulkOpen, setBulkOpen] = useState(false);
  const [newOpen, setNewOpen] = useState(false);

  const limit = 50;
  const offset = Number(params.get("offset") ?? 0);

  const filters = useMemo(() => {
    const status = params.get("status");
    return {
      q: params.get("q") ?? undefined,
      queueId: params.get("queueId") ?? undefined,
      departmentId: params.get("departmentId") ?? undefined,
      serviceTypeId: params.get("serviceTypeId") ?? undefined,
      assigneeUserId: params.get("mine") === "1" ? me?.user?.id : (params.get("assigneeUserId") ?? undefined),
      status: status ? (status.split(",") as RequestStatus[]) : undefined,
      unassigned: params.get("unassigned") === "1",
      breached: params.get("breached") === "1",
      openOnly: params.get("openOnly") !== "0",
      sort: params.get("sort") ?? "updatedAt",
      dir: (params.get("dir") as "asc" | "desc") ?? "desc",
      limit,
      offset,
    };
  }, [params, me?.user?.id, offset]);

  const list = useQuery({
    queryKey: ["requests", filters],
    queryFn: () => api.requests(filters),
  });

  const queues = useQuery({ queryKey: ["queues"], queryFn: () => api.queues() });
  const serviceTypes = useQuery({ queryKey: ["service-types"], queryFn: () => api.serviceTypes() });

  const bulk = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.bulk({ ...body, requestIds: [...selected] }),
    onSuccess: (res) => {
      void queryClient.invalidateQueries({ queryKey: ["requests"] });
      setSelected(new Set());
      setBulkOpen(false);
      if (Object.keys(res.failed).length > 0) {
        // A partial failure is reported, not swallowed — the agent needs to
        // know which items did not move.
        alert(
          `${res.succeeded.length} updated, ${Object.keys(res.failed).length} failed:\n` +
            Object.entries(res.failed).map(([id, msg]) => `${id}: ${msg}`).join("\n"),
        );
      }
    },
  });

  function setFilter(key: string, value: string | null) {
    const next = new URLSearchParams(params);
    if (value === null || value === "") next.delete(key);
    else next.set(key, value);
    next.delete("offset");
    setParams(next);
  }

  const items = list.data?.items ?? [];
  const allSelected = items.length > 0 && items.every((r) => selected.has(r.id));

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold">Requests</h1>
          <p className="text-sm text-ink-muted">
            {list.data ? `${list.data.total.toLocaleString()} matching` : "Loading…"}
          </p>
        </div>
        <div className="flex items-center gap-2">
          {can("request:write") && (
            <Button variant="primary" onClick={() => setNewOpen(true)}>
              New request
            </Button>
          )}
          <a className="cc-btn cc-btn-secondary" href={api.exportUrl("requests", filters)}>
            Export CSV
          </a>
        </div>
      </div>

      {/* Filters sit in one row above the results, so the shape of the query
          is visible without opening a panel. */}
      <Card>
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-5">
          <Field label="Search">
            <Input
              defaultValue={params.get("q") ?? ""}
              placeholder="Reference, subject, description"
              onKeyDown={(e) => {
                if (e.key === "Enter") setFilter("q", (e.target as HTMLInputElement).value);
              }}
            />
          </Field>
          <Field label="Queue">
            <Select value={params.get("queueId") ?? ""} onChange={(e) => setFilter("queueId", e.target.value)}>
              <option value="">All queues</option>
              {(queues.data?.items ?? []).map((q) => (
                <option key={q.id} value={q.id}>{q.name}</option>
              ))}
            </Select>
          </Field>
          <Field label="Service type">
            <Select
              value={params.get("serviceTypeId") ?? ""}
              onChange={(e) => setFilter("serviceTypeId", e.target.value)}
            >
              <option value="">All types</option>
              {(serviceTypes.data?.items ?? []).map((st) => (
                <option key={st.id} value={st.id}>{st.name}</option>
              ))}
            </Select>
          </Field>
          <Field label="Status">
            <Select value={params.get("status") ?? ""} onChange={(e) => setFilter("status", e.target.value)}>
              <option value="">All open</option>
              {OPEN_STATUSES.map((s) => (
                <option key={s} value={s}>{STATUS_LABELS[s]}</option>
              ))}
              <option value="resolved">Resolved</option>
              <option value="closed">Closed</option>
            </Select>
          </Field>
          <Field label="Sort">
            <Select
              value={`${params.get("sort") ?? "updatedAt"}:${params.get("dir") ?? "desc"}`}
              onChange={(e) => {
                const [sort, dir] = e.target.value.split(":");
                const next = new URLSearchParams(params);
                next.set("sort", sort);
                next.set("dir", dir);
                setParams(next);
              }}
            >
              <option value="updatedAt:desc">Recently updated</option>
              <option value="openedAt:desc">Newest first</option>
              <option value="openedAt:asc">Oldest first</option>
              <option value="dueAt:asc">Due soonest</option>
              <option value="priority:desc">Highest priority</option>
            </Select>
          </Field>
        </div>

        <div className="mt-3 flex flex-wrap items-center gap-2">
          <FilterChip active={params.get("mine") === "1"} onClick={() => setFilter("mine", params.get("mine") === "1" ? null : "1")}>
            Mine
          </FilterChip>
          <FilterChip active={params.get("unassigned") === "1"} onClick={() => setFilter("unassigned", params.get("unassigned") === "1" ? null : "1")}>
            Unassigned
          </FilterChip>
          <FilterChip active={params.get("breached") === "1"} onClick={() => setFilter("breached", params.get("breached") === "1" ? null : "1")}>
            Overdue
          </FilterChip>
          <FilterChip active={params.get("openOnly") === "0"} onClick={() => setFilter("openOnly", params.get("openOnly") === "0" ? null : "0")}>
            Include closed
          </FilterChip>
          {[...params.keys()].length > 0 && (
            <Button size="sm" variant="ghost" onClick={() => setParams(new URLSearchParams())}>
              Clear all
            </Button>
          )}
        </div>
      </Card>

      {selected.size > 0 && can("request:write") && (
        <div
          className="flex items-center gap-3 rounded-md px-4 py-2.5 text-sm"
          style={{ background: "var(--surface-2)", border: "1px solid var(--border-strong)" }}
        >
          <span className="font-medium">{selected.size} selected</span>
          <Button size="sm" onClick={() => setBulkOpen(true)}>Bulk action</Button>
          <Button size="sm" variant="ghost" onClick={() => setSelected(new Set())}>Clear</Button>
        </div>
      )}

      <Card className="overflow-hidden !p-0">
        {list.isLoading ? (
          <Spinner />
        ) : list.error ? (
          <ErrorNote error={list.error} onRetry={() => void list.refetch()} />
        ) : items.length === 0 ? (
          <Empty title="No requests match these filters" hint="Try widening the search or clearing a filter." />
        ) : (
          <div className="overflow-x-auto">
            <table className="cc-table">
              <thead>
                <tr>
                  {can("request:write") && (
                    <th className="w-8">
                      <input
                        type="checkbox"
                        aria-label="Select all on this page"
                        checked={allSelected}
                        onChange={(e) =>
                          setSelected(e.target.checked ? new Set(items.map((r) => r.id)) : new Set())
                        }
                      />
                    </th>
                  )}
                  <th>Reference</th>
                  <th>Subject</th>
                  <th>Contact</th>
                  <th>Queue</th>
                  <th>Assignee</th>
                  <th>Status</th>
                  <th>Priority</th>
                  <th>SLA</th>
                  <th>Updated</th>
                </tr>
              </thead>
              <tbody>
                {items.map((r) => (
                  <tr key={r.id}>
                    {can("request:write") && (
                      <td>
                        <input
                          type="checkbox"
                          aria-label={`Select ${r.reference}`}
                          checked={selected.has(r.id)}
                          onChange={(e) => {
                            const next = new Set(selected);
                            if (e.target.checked) next.add(r.id);
                            else next.delete(r.id);
                            setSelected(next);
                          }}
                        />
                      </td>
                    )}
                    <td>
                      <Link className="font-mono text-xs underline underline-offset-2" to={`/requests/${r.id}`}>
                        {r.reference}
                      </Link>
                    </td>
                    <td className="max-w-xs">
                      <Link to={`/requests/${r.id}`} className="block truncate hover:underline">
                        {r.subject}
                      </Link>
                      {r.serviceType && (
                        <span className="block truncate text-xs text-ink-faint">{r.serviceType.name}</span>
                      )}
                    </td>
                    <td className="max-w-[10rem] truncate">
                      {r.contact ? (
                        <Link to={`/contacts/${r.contactId}`} className="hover:underline">
                          {r.contact.displayName}
                        </Link>
                      ) : "—"}
                    </td>
                    <td className="whitespace-nowrap text-ink-muted">{r.queue?.name ?? "—"}</td>
                    <td className="whitespace-nowrap">
                      {r.assigneeUser?.name ?? <span className="text-ink-faint">Unassigned</span>}
                    </td>
                    <td><StatusBadge status={r.status} /></td>
                    <td><PriorityBadge priority={r.priority} /></td>
                    <td>
                      <SLABadge dueAt={r.dueAt} breached={r.slaBreached} warned={r.slaWarned} status={r.status} />
                    </td>
                    <td className="whitespace-nowrap text-ink-muted">{relativeTime(r.lastActivityAt)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {list.data && (
          <Pager
            total={list.data.total}
            limit={limit}
            offset={offset}
            onChange={(next) => setFilter("offset", String(next))}
          />
        )}
      </Card>

      <BulkDialog
        open={bulkOpen}
        count={selected.size}
        onClose={() => setBulkOpen(false)}
        onApply={(body) => bulk.mutate(body)}
        pending={bulk.isPending}
      />

      <Modal open={newOpen} onClose={() => setNewOpen(false)} title="New service request" wide>
        <NewRequestForm
          onCreated={() => {
            setNewOpen(false);
            void queryClient.invalidateQueries({ queryKey: ["requests"] });
          }}
        />
      </Modal>
    </div>
  );
}

function FilterChip({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      onClick={onClick}
      aria-pressed={active}
      className="rounded-full border px-3 py-1 text-xs font-medium transition-colors"
      style={{
        borderColor: active ? "var(--accent)" : "var(--border-strong)",
        background: active ? "var(--surface-0)" : "transparent",
        color: active ? "var(--accent)" : "var(--text-secondary)",
      }}
    >
      {children}
    </button>
  );
}

function BulkDialog({ open, count, onClose, onApply, pending }: {
  open: boolean;
  count: number;
  onClose: () => void;
  onApply: (body: Record<string, unknown>) => void;
  pending: boolean;
}) {
  const [operation, setOperation] = useState("assign");
  const [value, setValue] = useState("");
  const [note, setNote] = useState("");

  const users = useQuery({ queryKey: ["users"], queryFn: () => api.users({ limit: 200 }), enabled: open });
  const queues = useQuery({ queryKey: ["queues"], queryFn: () => api.queues(), enabled: open });

  return (
    <Modal open={open} onClose={onClose} title={`Bulk action on ${count} request${count === 1 ? "" : "s"}`}>
      <div className="space-y-3">
        <Field label="Action">
          <Select value={operation} onChange={(e) => { setOperation(e.target.value); setValue(""); }}>
            <option value="assign">Assign to</option>
            <option value="transition">Change status</option>
            <option value="priority">Set priority</option>
            <option value="queue">Move to queue</option>
            <option value="tag">Add tags</option>
          </Select>
        </Field>

        {operation === "assign" && (
          <Field label="Assignee">
            <Select value={value} onChange={(e) => setValue(e.target.value)}>
              <option value="">Unassign</option>
              {(users.data?.items ?? []).map((u) => (
                <option key={u.id} value={u.id}>{u.name || u.email}</option>
              ))}
            </Select>
          </Field>
        )}
        {operation === "transition" && (
          <Field label="New status">
            <Select value={value} onChange={(e) => setValue(e.target.value)}>
              <option value="">Choose…</option>
              {Object.entries(STATUS_LABELS).map(([k, label]) => (
                <option key={k} value={k}>{label}</option>
              ))}
            </Select>
          </Field>
        )}
        {operation === "priority" && (
          <Field label="Priority">
            <Select value={value} onChange={(e) => setValue(e.target.value)}>
              <option value="">Choose…</option>
              {["low", "normal", "high", "urgent", "critical"].map((p) => (
                <option key={p} value={p}>{p}</option>
              ))}
            </Select>
          </Field>
        )}
        {operation === "queue" && (
          <Field label="Queue">
            <Select value={value} onChange={(e) => setValue(e.target.value)}>
              <option value="">Choose…</option>
              {(queues.data?.items ?? []).map((q) => (
                <option key={q.id} value={q.id}>{q.name}</option>
              ))}
            </Select>
          </Field>
        )}
        {operation === "tag" && (
          <Field label="Tags" hint="Comma separated">
            <Input value={value} onChange={(e) => setValue(e.target.value)} placeholder="storm, ward-3" />
          </Field>
        )}

        <Field label="Note (optional)">
          <Input value={note} onChange={(e) => setNote(e.target.value)} />
        </Field>

        <div className="flex justify-end gap-2 pt-2">
          <Button onClick={onClose}>Cancel</Button>
          <Button
            variant="primary"
            disabled={pending || (operation !== "assign" && !value)}
            onClick={() => {
              const body: Record<string, unknown> = { operation, note };
              if (operation === "assign") body.userId = value;
              if (operation === "transition") body.status = value;
              if (operation === "priority") body.priority = value;
              if (operation === "queue") body.queueId = value;
              if (operation === "tag") body.tags = value.split(",").map((t) => t.trim()).filter(Boolean);
              onApply(body);
            }}
          >
            {pending ? "Applying…" : `Apply to ${count}`}
          </Button>
        </div>
      </div>
    </Modal>
  );
}
