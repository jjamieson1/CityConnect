import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";

import { api } from "@/lib/api";
import type { Condition, RoutingRule } from "@/lib/types";
import { Badge, Button, ErrorNote, Field, Input, Select, Spinner } from "./ui";

const FIELDS = [
  { value: "service_type", label: "Service type code" },
  { value: "category", label: "Category" },
  { value: "priority", label: "Priority" },
  { value: "source", label: "Channel" },
  { value: "ward", label: "Ward" },
  { value: "postal_code", label: "Postal code" },
  { value: "city", label: "City" },
  { value: "subject", label: "Subject text" },
  { value: "description", label: "Description text" },
  { value: "tag", label: "Tag" },
];

const OPS = [
  { value: "eq", label: "is" },
  { value: "neq", label: "is not" },
  { value: "contains", label: "contains" },
  { value: "not_contains", label: "does not contain" },
  { value: "starts_with", label: "starts with" },
  { value: "in", label: "is one of" },
  { value: "not_in", label: "is none of" },
  { value: "gt", label: "is greater than" },
  { value: "lt", label: "is less than" },
  { value: "exists", label: "is set" },
  { value: "not_exists", label: "is not set" },
];

/**
 * The rule editor deliberately offers a fixed predicate set rather than an
 * expression box. A routing DSL becomes unmaintainable and unauditable within
 * a year, and nobody can answer "why did this land in Bylaw?" from one.
 */
export default function RuleEditor({ rule, onSave, onCancel, saving, error }: {
  rule: Partial<RoutingRule>;
  onSave: (rule: Partial<RoutingRule>) => void;
  onCancel: () => void;
  saving: boolean;
  error: unknown;
}) {
  const [draft, setDraft] = useState<Partial<RoutingRule>>({
    ...rule,
    conditions: rule.conditions ?? { all: [] },
    actions: rule.actions ?? {},
  });

  const queues = useQuery({ queryKey: ["queues"], queryFn: () => api.queues() });
  const departments = useQuery({ queryKey: ["departments"], queryFn: () => api.departments() });
  const users = useQuery({ queryKey: ["users"], queryFn: () => api.users({ limit: 200, status: "active" }) });

  const simulate = useMutation({
    mutationFn: () => api.simulate({ rules: [draft], sample: 200 }),
  });

  const conditions = draft.conditions?.all ?? [];

  function setCondition(index: number, patch: Partial<Condition>) {
    const next = [...conditions];
    next[index] = { ...next[index], ...patch };
    setDraft({ ...draft, conditions: { ...draft.conditions, all: next } });
  }

  function addCondition() {
    setDraft({
      ...draft,
      conditions: { ...draft.conditions, all: [...conditions, { field: "service_type", op: "eq", value: "" }] },
    });
  }

  function removeCondition(index: number) {
    setDraft({
      ...draft,
      conditions: { ...draft.conditions, all: conditions.filter((_, i) => i !== index) },
    });
  }

  const sim = simulate.data;

  return (
    <div className="space-y-4">
      {error ? <ErrorNote error={error} /> : null}

      <div className="grid gap-3 sm:grid-cols-3">
        <div className="sm:col-span-2">
          <Field label="Rule name">
            <Input
              value={draft.name ?? ""}
              onChange={(e) => setDraft({ ...draft, name: e.target.value })}
              placeholder="Potholes to the Roads queue"
            />
          </Field>
        </div>
        <Field label="Order" hint="Lower runs first">
          <Input
            type="number"
            value={draft.priority ?? 100}
            onChange={(e) => setDraft({ ...draft, priority: Number(e.target.value) })}
          />
        </Field>
      </div>

      <Field label="Description">
        <Input
          value={draft.description ?? ""}
          onChange={(e) => setDraft({ ...draft, description: e.target.value })}
        />
      </Field>

      <fieldset className="rounded-md border p-3" style={{ borderColor: "var(--border)" }}>
        <legend className="px-1 text-xs font-medium uppercase tracking-wide text-ink-muted">
          When all of these are true
        </legend>

        {conditions.length === 0 ? (
          <p className="py-2 text-sm text-ink-muted">
            No conditions — this rule matches every request. That is rarely what you want except
            as a final catch-all.
          </p>
        ) : (
          <ul className="space-y-2">
            {conditions.map((c, i) => (
              <li key={i} className="grid grid-cols-[1fr_1fr_1.5fr_auto] items-end gap-2">
                <Select value={c.field} onChange={(e) => setCondition(i, { field: e.target.value })}>
                  {FIELDS.map((f) => <option key={f.value} value={f.value}>{f.label}</option>)}
                </Select>
                <Select value={c.op} onChange={(e) => setCondition(i, { op: e.target.value })}>
                  {OPS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
                </Select>
                {c.op === "in" || c.op === "not_in" ? (
                  <Input
                    placeholder="Comma separated"
                    value={(c.list ?? []).join(", ")}
                    onChange={(e) =>
                      setCondition(i, { list: e.target.value.split(",").map((v) => v.trim()).filter(Boolean) })
                    }
                  />
                ) : c.op === "exists" || c.op === "not_exists" ? (
                  <span className="pb-2 text-sm text-ink-faint">no value needed</span>
                ) : (
                  <Input value={c.value ?? ""} onChange={(e) => setCondition(i, { value: e.target.value })} />
                )}
                <Button size="sm" onClick={() => removeCondition(i)} aria-label="Remove condition">✕</Button>
              </li>
            ))}
          </ul>
        )}

        <Button size="sm" className="mt-2" onClick={addCondition}>Add condition</Button>
      </fieldset>

      <fieldset className="rounded-md border p-3" style={{ borderColor: "var(--border)" }}>
        <legend className="px-1 text-xs font-medium uppercase tracking-wide text-ink-muted">Then</legend>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Send to queue">
            <Select
              value={draft.actions?.queueId ?? ""}
              onChange={(e) => setDraft({ ...draft, actions: { ...draft.actions, queueId: e.target.value } })}
            >
              <option value="">No change</option>
              {(queues.data?.items ?? []).map((q) => <option key={q.id} value={q.id}>{q.name}</option>)}
            </Select>
          </Field>
          <Field label="Move to department">
            <Select
              value={draft.actions?.departmentId ?? ""}
              onChange={(e) => setDraft({ ...draft, actions: { ...draft.actions, departmentId: e.target.value } })}
            >
              <option value="">No change</option>
              {(departments.data?.items ?? []).map((d) => <option key={d.id} value={d.id}>{d.name}</option>)}
            </Select>
          </Field>
          <Field label="Assign to">
            <Select
              value={draft.actions?.assigneeUserId ?? ""}
              onChange={(e) => setDraft({ ...draft, actions: { ...draft.actions, assigneeUserId: e.target.value } })}
            >
              <option value="">Leave for the queue</option>
              {(users.data?.items ?? []).map((u) => <option key={u.id} value={u.id}>{u.name || u.email}</option>)}
            </Select>
          </Field>
          <Field label="Set priority">
            <Select
              value={draft.actions?.priority ?? ""}
              onChange={(e) => setDraft({ ...draft, actions: { ...draft.actions, priority: e.target.value } })}
            >
              <option value="">No change</option>
              {["low", "normal", "high", "urgent", "critical"].map((p) => (
                <option key={p} value={p}>{p}</option>
              ))}
            </Select>
          </Field>
          <Field label="Add tags" hint="Comma separated">
            <Input
              value={(draft.actions?.addTags ?? []).join(", ")}
              onChange={(e) =>
                setDraft({
                  ...draft,
                  actions: {
                    ...draft.actions,
                    addTags: e.target.value.split(",").map((t) => t.trim()).filter(Boolean),
                  },
                })
              }
            />
          </Field>
        </div>

        <label className="mt-3 flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={Boolean(draft.continue)}
            onChange={(e) => setDraft({ ...draft, continue: e.target.checked })}
          />
          Keep evaluating later rules after this one matches
        </label>
      </fieldset>

      {/* Simulation before activation. This is the difference between a rule
          you understand and one you find out about three weeks later. */}
      <div className="rounded-md border p-3" style={{ borderColor: "var(--border)" }}>
        <div className="flex items-center justify-between gap-3">
          <div>
            <h3 className="text-sm font-semibold">Test against recent requests</h3>
            <p className="text-xs text-ink-muted">
              Replays the last 200 requests through this rule. Nothing is changed.
            </p>
          </div>
          <Button onClick={() => simulate.mutate()} disabled={simulate.isPending}>
            {simulate.isPending ? "Running…" : "Simulate"}
          </Button>
        </div>

        {simulate.error && <div className="mt-3"><ErrorNote error={simulate.error} /></div>}
        {simulate.isPending && <Spinner label="Replaying" />}

        {sim && (
          <div className="mt-3 space-y-3">
            <div className="flex flex-wrap gap-2">
              <Badge>{sim.sampled} requests replayed</Badge>
              <Badge tone={sim.changed > 0 ? "warning" : "neutral"}>
                {sim.changed} would move queue
              </Badge>
              <Badge>{sim.unrouted} unmatched</Badge>
            </div>

            {sim.changed > sim.sampled * 0.5 && (
              <p
                className="rounded px-3 py-2 text-sm"
                style={{ background: "var(--status-warning-bg)", color: "var(--status-warning)" }}
              >
                ⚑ This rule reroutes more than half of recent requests. Check the conditions are
                as narrow as you intended before activating it.
              </p>
            )}

            {sim.cases.filter((c) => c.changed).length > 0 && (
              <div className="max-h-48 overflow-y-auto">
                <table className="cc-table">
                  <thead>
                    <tr><th>Reference</th><th>Subject</th><th>Would move to</th></tr>
                  </thead>
                  <tbody>
                    {sim.cases.filter((c) => c.changed).slice(0, 25).map((c) => (
                      <tr key={c.requestId}>
                        <td className="font-mono text-xs">{c.reference}</td>
                        <td className="max-w-xs truncate">{c.subject}</td>
                        <td>
                          {queues.data?.items.find((q) => q.id === c.proposedQueueId)?.name ?? "—"}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        )}
      </div>

      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={Boolean(draft.active)}
          onChange={(e) => setDraft({ ...draft, active: e.target.checked })}
        />
        Active — this rule runs on new requests
      </label>

      <div className="flex justify-end gap-2">
        <Button onClick={onCancel}>Cancel</Button>
        <Button variant="primary" disabled={saving || !draft.name?.trim()} onClick={() => onSave(draft)}>
          {saving ? "Saving…" : "Save rule"}
        </Button>
      </div>
    </div>
  );
}
