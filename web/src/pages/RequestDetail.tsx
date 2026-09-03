import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, ApiError } from "@/lib/api";
import type { RequestStatus, Visibility } from "@/lib/types";
import { useAuth } from "@/hooks/useAuth";
import {
  Badge, Button, Card, Empty, ErrorNote, Field, formatBytes, formatDateTime, Input, Modal,
  PriorityBadge, relativeTime, Select, SLABadge, Spinner, StatusBadge, STATUS_LABELS, Textarea,
} from "@/components/ui";

/** Legal moves out of each status, mirroring the server's state machine so the
 *  UI only offers transitions that will actually be accepted. The server
 *  remains the authority: an illegal move still returns 409. */
const NEXT: Record<RequestStatus, RequestStatus[]> = {
  new: ["triaged", "assigned", "in_progress", "cancelled"],
  triaged: ["assigned", "in_progress", "waiting_citizen", "waiting_third_party", "cancelled"],
  assigned: ["in_progress", "triaged", "waiting_citizen", "waiting_third_party", "resolved", "cancelled"],
  in_progress: ["waiting_citizen", "waiting_third_party", "resolved", "assigned", "cancelled"],
  waiting_citizen: ["in_progress", "resolved", "cancelled"],
  waiting_third_party: ["in_progress", "resolved", "cancelled"],
  resolved: ["closed", "in_progress"],
  closed: ["in_progress"],
  cancelled: ["in_progress"],
};

export default function RequestDetail() {
  const { id = "" } = useParams();
  const { me, can } = useAuth();
  const queryClient = useQueryClient();

  const [transitionTo, setTransitionTo] = useState<RequestStatus | null>(null);
  const [transferOpen, setTransferOpen] = useState(false);
  const [linkOpen, setLinkOpen] = useState(false);

  const request = useQuery({ queryKey: ["request", id], queryFn: () => api.request(id) });
  const events = useQuery({ queryKey: ["request-events", id], queryFn: () => api.requestEvents(id) });
  const comments = useQuery({ queryKey: ["comments", id], queryFn: () => api.comments(id) });
  const links = useQuery({ queryKey: ["links", id], queryFn: () => api.links(id) });
  const attachments = useQuery({ queryKey: ["attachments", id], queryFn: () => api.attachments(id) });

  function refresh() {
    void queryClient.invalidateQueries({ queryKey: ["request", id] });
    void queryClient.invalidateQueries({ queryKey: ["request-events", id] });
    void queryClient.invalidateQueries({ queryKey: ["comments", id] });
  }

  const assign = useMutation({
    mutationFn: (userId: string) => api.assign(id, { userId, version: request.data?.version }),
    onSuccess: refresh,
  });

  if (request.isLoading) return <Spinner label="Loading request" />;
  if (request.error) return <ErrorNote error={request.error} onRetry={() => void request.refetch()} />;

  const r = request.data!;
  const writable = can("request:write") && !r.mergedIntoId;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-sm text-ink-muted">{r.reference}</span>
            <StatusBadge status={r.status} />
            <PriorityBadge priority={r.priority} />
            <SLABadge dueAt={r.dueAt} breached={r.slaBreached} warned={r.slaWarned} status={r.status} />
            {r.reopenCount > 0 && <Badge tone="warning">Reopened ×{r.reopenCount}</Badge>}
            {r.mergedIntoId && <Badge tone="neutral">Merged as duplicate</Badge>}
          </div>
          <h1 className="mt-1 text-xl font-semibold">{r.subject}</h1>
          <p className="text-sm text-ink-muted">
            {r.serviceType?.name}
            {r.department && ` · ${r.department.name}`}
            {r.queue && ` · ${r.queue.name}`}
            {" · opened "}{relativeTime(r.openedAt)}
          </p>
        </div>

        {writable && (
          <div className="flex flex-wrap items-center gap-2">
            {NEXT[r.status]?.slice(0, 3).map((next) => (
              <Button
                key={next}
                variant={next === "resolved" ? "primary" : "secondary"}
                onClick={() => setTransitionTo(next)}
              >
                {STATUS_LABELS[next]}
              </Button>
            ))}
            {can("request:transfer") && (
              <Button onClick={() => setTransferOpen(true)}>Transfer</Button>
            )}
          </div>
        )}
      </div>

      <div className="grid gap-4 lg:grid-cols-3">
        <div className="space-y-4 lg:col-span-2">
          {r.description && (
            <Card title="Description">
              <p className="whitespace-pre-wrap text-sm">{r.description}</p>
            </Card>
          )}

          {r.formData && Object.keys(r.formData).length > 0 && (
            <Card title="Details supplied">
              <dl className="grid gap-x-6 gap-y-2 sm:grid-cols-2">
                {Object.entries(r.formData).map(([key, value]) => (
                  <div key={key}>
                    <dt className="text-xs uppercase tracking-wide text-ink-muted">
                      {r.serviceType?.intakeForm?.fields?.find((f) => f.key === key)?.label ?? key}
                    </dt>
                    <dd className="text-sm">
                      {typeof value === "boolean" ? (value ? "Yes" : "No") : String(value)}
                    </dd>
                  </div>
                ))}
              </dl>
            </Card>
          )}

          <CommentComposer requestId={id} onPosted={refresh} disabled={!writable} />

          <Card title="Activity">
            {events.isLoading || comments.isLoading ? (
              <Spinner />
            ) : (
              <Timeline
                events={events.data?.items ?? []}
                comments={comments.data?.items ?? []}
              />
            )}
          </Card>
        </div>

        <div className="space-y-4">
          <Card title="Assignment">
            <div className="space-y-3">
              <div>
                <p className="cc-label">Owner</p>
                {r.assigneeUser ? (
                  <p className="text-sm">{r.assigneeUser.name}</p>
                ) : (
                  <p className="text-sm text-ink-faint">Unassigned</p>
                )}
              </div>
              {writable && can("request:assign") && (
                <>
                  <AssigneePicker
                    value={r.assigneeUserId ?? ""}
                    onChange={(userId) => assign.mutate(userId)}
                    pending={assign.isPending}
                  />
                  {r.assigneeUserId !== me?.user?.id && (
                    <Button size="sm" className="w-full" onClick={() => assign.mutate(me!.user!.id)}>
                      Assign to me
                    </Button>
                  )}
                </>
              )}
              {assign.error && <ErrorNote error={assign.error} />}
            </div>
          </Card>

          <Card title="Contact">
            {r.contact ? (
              <div className="space-y-2 text-sm">
                <Link to={`/contacts/${r.contactId}`} className="font-medium underline underline-offset-2">
                  {r.contact.displayName}
                </Link>
                {r.contact.primaryEmail && <p className="text-ink-muted">{r.contact.primaryEmail}</p>}
                {r.contact.primaryPhone && <p className="text-ink-muted">{r.contact.primaryPhone}</p>}

                {/* Reachability is surfaced here because it changes how the
                    agent should follow up, not just what the system does. */}
                {!r.contact.c2Reachable && (
                  <p
                    className="rounded px-2 py-1.5 text-xs"
                    style={{ background: "var(--status-warning-bg)", color: "var(--status-warning)" }}
                  >
                    ⚑ Not reachable through C2
                    {r.contact.c2UnreachableCode === "no_consent" && " — consent withdrawn"}
                    {r.contact.c2UnreachableCode === "unknown_sub" && " — identity link is stale"}
                    . Follow up by phone or post.
                  </p>
                )}
                {r.contact.doNotContact && <Badge tone="critical">Do not contact</Badge>}
              </div>
            ) : (
              <p className="text-sm text-ink-faint">No contact linked.</p>
            )}
          </Card>

          {(r.address1 || r.ward) && (
            <Card title="Location">
              <address className="text-sm not-italic">
                {r.address1 && <p>{r.address1}</p>}
                {(r.city || r.postalCode) && <p className="text-ink-muted">{[r.city, r.postalCode].filter(Boolean).join(" ")}</p>}
                {r.ward && <p className="mt-1"><Badge>{r.ward}</Badge></p>}
              </address>
            </Card>
          )}

          <Card title="Service level">
            <dl className="space-y-2 text-sm">
              <Row label="Opened" value={formatDateTime(r.openedAt)} />
              <Row label="First response" value={r.firstResponseAt ? formatDateTime(r.firstResponseAt) : "Not yet"} />
              <Row label="Resolution due" value={r.dueAt ? formatDateTime(r.dueAt) : "No target"} />
              {r.resolvedAt && <Row label="Resolved" value={formatDateTime(r.resolvedAt)} />}
              {r.closedAt && <Row label="Closed" value={formatDateTime(r.closedAt)} />}
              {r.csatScore != null && <Row label="Satisfaction" value={`${r.csatScore} / 5`} />}
            </dl>
          </Card>

          <Card
            title="Linked requests"
            actions={writable && <Button size="sm" onClick={() => setLinkOpen(true)}>Link</Button>}
          >
            {(links.data?.items.length ?? 0) === 0 ? (
              <p className="text-sm text-ink-faint">No links.</p>
            ) : (
              <ul className="space-y-1 text-sm">
                {links.data!.items.map((l) => (
                  <li key={l.id} className="flex items-center justify-between gap-2">
                    <Link to={`/requests/${l.targetId}`} className="min-w-0 truncate hover:underline">
                      <span className="font-mono text-xs">{l.targetRef}</span>{" "}
                      <span className="text-ink-muted">{l.targetSubject}</span>
                    </Link>
                    <Badge>{l.kind.replace(/_/g, " ")}</Badge>
                  </li>
                ))}
              </ul>
            )}
          </Card>

          <Attachments requestId={id} writable={writable} items={attachments.data?.items ?? []} />

          {r.tags.length > 0 && (
            <Card title="Tags">
              <div className="flex flex-wrap gap-1">
                {r.tags.map((t) => <Badge key={t}>{t}</Badge>)}
              </div>
            </Card>
          )}
        </div>
      </div>

      <TransitionDialog
        requestId={id}
        version={r.version}
        to={transitionTo}
        onClose={() => setTransitionTo(null)}
        onDone={() => { setTransitionTo(null); refresh(); }}
      />
      <TransferDialog
        requestId={id}
        open={transferOpen}
        onClose={() => setTransferOpen(false)}
        onDone={() => { setTransferOpen(false); refresh(); }}
      />
      <LinkDialog
        requestId={id}
        open={linkOpen}
        onClose={() => setLinkOpen(false)}
        onDone={() => {
          setLinkOpen(false);
          void queryClient.invalidateQueries({ queryKey: ["links", id] });
        }}
      />
    </div>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between gap-3">
      <dt className="text-ink-muted">{label}</dt>
      <dd className="text-right">{value}</dd>
    </div>
  );
}

function AssigneePicker({ value, onChange, pending }: {
  value: string;
  onChange: (id: string) => void;
  pending: boolean;
}) {
  const users = useQuery({ queryKey: ["users"], queryFn: () => api.users({ limit: 200, status: "active" }) });
  return (
    <Field label="Reassign">
      <Select value={value} disabled={pending} onChange={(e) => onChange(e.target.value)}>
        <option value="">Unassigned</option>
        {(users.data?.items ?? []).map((u) => (
          <option key={u.id} value={u.id}>{u.name || u.email}</option>
        ))}
      </Select>
    </Field>
  );
}

/**
 * The composer defaults to an internal note. A citizen-visible comment is an
 * explicit choice because it is what C2 renders on the Service Card and what a
 * notification quotes.
 */
function CommentComposer({ requestId, onPosted, disabled }: {
  requestId: string;
  onPosted: () => void;
  disabled: boolean;
}) {
  const [body, setBody] = useState("");
  const [visibility, setVisibility] = useState<Visibility>("internal");
  const [notify, setNotify] = useState(true);
  const { me } = useAuth();

  const macros = useQuery({
    queryKey: ["macros", me?.department?.id],
    queryFn: () => api.macros(me?.department?.id),
  });

  const post = useMutation({
    mutationFn: () => api.addComment(requestId, { body, visibility, notifyCitizen: visibility === "citizen" && notify }),
    onSuccess: () => { setBody(""); onPosted(); },
  });

  const applyMacro = useMutation({
    mutationFn: (macroId: string) => api.applyMacro(requestId, macroId),
    onSuccess: onPosted,
  });

  if (disabled) return null;

  return (
    <Card title="Add a note">
      <div className="space-y-3">
        {post.error && <ErrorNote error={post.error} />}
        {applyMacro.error && <ErrorNote error={applyMacro.error} />}

        {(macros.data?.items.length ?? 0) > 0 && (
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-xs text-ink-muted">Macros:</span>
            {macros.data!.items.slice(0, 6).map((m) => (
              <Button
                key={m.id}
                size="sm"
                disabled={applyMacro.isPending}
                title={m.description}
                onClick={() => applyMacro.mutate(m.id)}
              >
                {m.name}
              </Button>
            ))}
          </div>
        )}

        <Textarea
          rows={3}
          value={body}
          onChange={(e) => setBody(e.target.value)}
          placeholder={visibility === "citizen" ? "This will be visible to the citizen…" : "Internal note…"}
        />

        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex items-center gap-3">
            <Select
              className="w-auto"
              value={visibility}
              onChange={(e) => setVisibility(e.target.value as Visibility)}
            >
              <option value="internal">Internal note</option>
              <option value="citizen">Visible to citizen</option>
            </Select>
            {visibility === "citizen" && (
              <label className="flex items-center gap-2 text-sm">
                <input type="checkbox" checked={notify} onChange={(e) => setNotify(e.target.checked)} />
                Notify them
              </label>
            )}
          </div>
          <Button
            variant="primary"
            disabled={!body.trim() || post.isPending}
            onClick={() => post.mutate()}
          >
            {post.isPending ? "Posting…" : "Post"}
          </Button>
        </div>

        {visibility === "citizen" && (
          <p className="text-xs text-ink-faint">
            Citizen-visible notes appear on their Service Card in C2. Notifications are only
            delivered while the citizen holds active consent.
          </p>
        )}
      </div>
    </Card>
  );
}

function Timeline({ events, comments }: {
  events: { id: string; kind: string; summary?: string; actorName?: string; createdAt: string; fromValue?: string; toValue?: string }[];
  comments: { id: string; body: string; authorName?: string; visibility: Visibility; createdAt: string }[];
}) {
  const merged = [
    ...comments.map((c) => ({ kind: "comment" as const, at: c.createdAt, data: c })),
    ...events.filter((e) => e.kind !== "commented").map((e) => ({ kind: "event" as const, at: e.createdAt, data: e })),
  ].sort((a, b) => new Date(b.at).getTime() - new Date(a.at).getTime());

  if (merged.length === 0) return <Empty title="Nothing has happened yet" />;

  return (
    <ol className="space-y-3">
      {merged.map((item, i) =>
        item.kind === "comment" ? (
          <li key={`c-${item.data.id}-${i}`} className="rounded-md border p-3" style={{ borderColor: "var(--border)" }}>
            <div className="mb-1 flex flex-wrap items-center gap-2 text-xs text-ink-muted">
              <span className="font-medium text-ink">{item.data.authorName ?? "Staff"}</span>
              {item.data.visibility === "citizen"
                ? <Badge tone="accent">Sent to citizen</Badge>
                : <Badge>Internal</Badge>}
              <span>{formatDateTime(item.at)}</span>
            </div>
            <p className="whitespace-pre-wrap text-sm">{item.data.body}</p>
          </li>
        ) : (
          <li key={`e-${item.data.id}-${i}`} className="flex gap-3 px-1 text-sm">
            <span className="mt-1.5 h-1.5 w-1.5 shrink-0 rounded-full" style={{ background: "var(--border-strong)" }} aria-hidden />
            <div className="min-w-0 flex-1">
              <p className="text-ink">
                {item.data.summary || item.data.kind.replace(/_/g, " ")}
              </p>
              <p className="text-xs text-ink-faint">
                {item.data.actorName ?? "System"} · {formatDateTime(item.at)}
              </p>
            </div>
          </li>
        ),
      )}
    </ol>
  );
}

function TransitionDialog({ requestId, version, to, onClose, onDone }: {
  requestId: string;
  version: number;
  to: RequestStatus | null;
  onClose: () => void;
  onDone: () => void;
}) {
  const [note, setNote] = useState("");
  const [resolutionCode, setResolutionCode] = useState("");
  const [notify, setNotify] = useState(true);

  const move = useMutation({
    mutationFn: () =>
      api.transition(requestId, {
        to: to!, note, resolutionCode: resolutionCode || undefined,
        notifyCitizen: notify, version,
      }),
    onSuccess: () => { setNote(""); onDone(); },
  });

  const resolving = to === "resolved";

  return (
    <Modal open={to !== null} onClose={onClose} title={`Move to ${to ? STATUS_LABELS[to] : ""}`}>
      <div className="space-y-3">
        {move.error && (
          <ErrorNote
            error={
              move.error instanceof ApiError && move.error.isStale
                ? new Error("Somebody else changed this request. Close this and reload the page.")
                : move.error
            }
          />
        )}

        {resolving && (
          <Field label="Resolution code" hint="How was it resolved? Used in reporting.">
            <Select value={resolutionCode} onChange={(e) => setResolutionCode(e.target.value)}>
              <option value="">Choose…</option>
              <option value="repaired">Repaired</option>
              <option value="completed">Completed</option>
              <option value="no_action_required">No action required</option>
              <option value="duplicate">Duplicate</option>
              <option value="referred">Referred elsewhere</option>
              <option value="unable_to_locate">Unable to locate</option>
            </Select>
          </Field>
        )}

        <Field label={resolving ? "What was done" : "Note (optional)"}>
          <Textarea rows={3} value={note} onChange={(e) => setNote(e.target.value)} />
        </Field>

        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={notify} onChange={(e) => setNotify(e.target.checked)} />
          Tell the citizen about this change
        </label>

        <div className="flex justify-end gap-2 pt-1">
          <Button onClick={onClose}>Cancel</Button>
          <Button
            variant="primary"
            disabled={move.isPending || (resolving && !resolutionCode && !note.trim())}
            onClick={() => move.mutate()}
          >
            {move.isPending ? "Saving…" : "Confirm"}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function TransferDialog({ requestId, open, onClose, onDone }: {
  requestId: string;
  open: boolean;
  onClose: () => void;
  onDone: () => void;
}) {
  const [departmentId, setDepartmentId] = useState("");
  const [queueId, setQueueId] = useState("");
  const [note, setNote] = useState("");

  const departments = useQuery({ queryKey: ["departments"], queryFn: () => api.departments(), enabled: open });
  const queues = useQuery({ queryKey: ["queues", departmentId], queryFn: () => api.queues({ departmentId }), enabled: open });

  const transfer = useMutation({
    mutationFn: () => api.transfer(requestId, { departmentId, queueId, note }),
    onSuccess: onDone,
  });

  return (
    <Modal open={open} onClose={onClose} title="Transfer request">
      <div className="space-y-3">
        {transfer.error && <ErrorNote error={transfer.error} />}
        <p className="text-sm text-ink-muted">
          Transferring clears the current owner so the receiving team picks it up fresh.
        </p>

        <Field label="Department">
          <Select value={departmentId} onChange={(e) => { setDepartmentId(e.target.value); setQueueId(""); }}>
            <option value="">Keep current</option>
            {(departments.data?.items ?? []).map((d) => (
              <option key={d.id} value={d.id}>{d.name}</option>
            ))}
          </Select>
        </Field>

        <Field label="Queue">
          <Select value={queueId} onChange={(e) => setQueueId(e.target.value)}>
            <option value="">Keep current</option>
            {(queues.data?.items ?? []).map((q) => (
              <option key={q.id} value={q.id}>{q.name}</option>
            ))}
          </Select>
        </Field>

        <Field label="Reason">
          <Input value={note} onChange={(e) => setNote(e.target.value)} placeholder="Why is it moving?" />
        </Field>

        <div className="flex justify-end gap-2 pt-1">
          <Button onClick={onClose}>Cancel</Button>
          <Button
            variant="primary"
            disabled={transfer.isPending || (!departmentId && !queueId)}
            onClick={() => transfer.mutate()}
          >
            {transfer.isPending ? "Transferring…" : "Transfer"}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function LinkDialog({ requestId, open, onClose, onDone }: {
  requestId: string;
  open: boolean;
  onClose: () => void;
  onDone: () => void;
}) {
  const [reference, setReference] = useState("");
  const [kind, setKind] = useState("related_to");
  const [note, setNote] = useState("");

  const link = useMutation({
    mutationFn: () => api.addLink(requestId, { targetReference: reference, kind, note }),
    onSuccess: () => { setReference(""); onDone(); },
  });

  return (
    <Modal open={open} onClose={onClose} title="Link to another request">
      <div className="space-y-3">
        {link.error && <ErrorNote error={link.error} />}

        <Field label="Reference" hint="The reference number of the other request">
          <Input
            value={reference}
            onChange={(e) => setReference(e.target.value.toUpperCase())}
            placeholder="SR-7K4M-2QX9"
          />
        </Field>

        <Field label="Relationship">
          <Select value={kind} onChange={(e) => setKind(e.target.value)}>
            <option value="related_to">Related to</option>
            <option value="duplicate_of">This is a duplicate of</option>
            <option value="child_of">This is part of</option>
          </Select>
        </Field>

        {kind === "duplicate_of" && (
          <p
            className="rounded px-2 py-1.5 text-xs"
            style={{ background: "var(--status-warning-bg)", color: "var(--status-warning)" }}
          >
            This request will be closed as a duplicate. The reporter still receives updates on
            their own request.
          </p>
        )}

        <Field label="Note">
          <Input value={note} onChange={(e) => setNote(e.target.value)} />
        </Field>

        <div className="flex justify-end gap-2 pt-1">
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" disabled={!reference.trim() || link.isPending} onClick={() => link.mutate()}>
            {link.isPending ? "Linking…" : "Link"}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function Attachments({ requestId, writable, items }: {
  requestId: string;
  writable: boolean;
  items: { id: string; filename: string; sizeBytes: number; visibility: Visibility; scanStatus: string }[];
}) {
  const queryClient = useQueryClient();
  const [uploading, setUploading] = useState(false);
  const [error, setError] = useState<unknown>(null);

  async function upload(file: File) {
    setUploading(true);
    setError(null);
    try {
      await api.upload(requestId, file, "internal");
      void queryClient.invalidateQueries({ queryKey: ["attachments", requestId] });
    } catch (e) {
      setError(e);
    } finally {
      setUploading(false);
    }
  }

  return (
    <Card title="Attachments">
      {error ? <ErrorNote error={error} /> : null}
      {items.length === 0 ? (
        <p className="text-sm text-ink-faint">No files.</p>
      ) : (
        <ul className="space-y-1 text-sm">
          {items.map((a) => (
            <li key={a.id} className="flex items-center justify-between gap-2">
              <a
                href={api.attachmentUrl(requestId, a.id)}
                className="min-w-0 truncate underline underline-offset-2"
                download
              >
                {a.filename}
              </a>
              <span className="shrink-0 text-xs text-ink-faint">{formatBytes(a.sizeBytes)}</span>
            </li>
          ))}
        </ul>
      )}
      {writable && (
        <label className="mt-3 block">
          <span className="cc-btn cc-btn-secondary w-full cursor-pointer">
            {uploading ? "Uploading…" : "Add a file"}
          </span>
          <input
            type="file"
            className="sr-only"
            disabled={uploading}
            onChange={(e) => {
              const file = e.target.files?.[0];
              if (file) void upload(file);
              e.target.value = "";
            }}
          />
        </label>
      )}
    </Card>
  );
}
