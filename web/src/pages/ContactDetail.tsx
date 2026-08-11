import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api } from "@/lib/api";
import { useAuth } from "@/hooks/useAuth";
import {
  Badge, Button, Card, Empty, ErrorNote, Field, formatDateTime, Input, Modal, relativeTime,
  Select, Spinner, StatusBadge, Textarea,
} from "@/components/ui";
import NewRequestForm from "@/components/NewRequestForm";

export default function ContactDetail() {
  const { id = "" } = useParams();
  const { can } = useAuth();
  const queryClient = useQueryClient();

  const [newRequestOpen, setNewRequestOpen] = useState(false);
  const [logOpen, setLogOpen] = useState(false);
  const [mergeOpen, setMergeOpen] = useState(false);

  const contact = useQuery({ queryKey: ["contact", id], queryFn: () => api.contact(id) });
  const timeline = useQuery({ queryKey: ["timeline", id], queryFn: () => api.timeline(id) });
  const requests = useQuery({
    queryKey: ["contact-requests", id],
    queryFn: () => api.requests({ contactId: id, openOnly: false, limit: 50 }),
  });
  const duplicates = useQuery({
    queryKey: ["duplicates", id],
    queryFn: () => api.duplicates(id),
    enabled: can("contact:merge"),
  });

  if (contact.isLoading) return <Spinner label="Loading contact" />;
  if (contact.error) return <ErrorNote error={contact.error} onRetry={() => void contact.refetch()} />;

  const c = contact.data!;
  const c2 = c.identities?.find((i) => i.provider === "c2");
  const dupes = duplicates.data?.items ?? [];

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold">{c.displayName}</h1>
          <p className="text-sm text-ink-muted">
            {c.organization && `${c.organization} · `}
            {[c.primaryEmail, c.primaryPhone].filter(Boolean).join(" · ") || "No contact details"}
          </p>
          <div className="mt-2 flex flex-wrap gap-1">
            {c.doNotContact && <Badge tone="critical">Do not contact</Badge>}
            {!c.c2Reachable && <Badge tone="warning">⚑ Not reachable through C2</Badge>}
            {c2 ? <Badge tone="good">C2 linked</Badge> : <Badge>No C2 account</Badge>}
            {c.ward && <Badge>{c.ward}</Badge>}
            {c.tags.map((t) => <Badge key={t}>{t}</Badge>)}
          </div>
        </div>

        <div className="flex flex-wrap gap-2">
          {can("contact:write") && <Button onClick={() => setLogOpen(true)}>Log interaction</Button>}
          {can("request:write") && (
            <Button variant="primary" onClick={() => setNewRequestOpen(true)}>New request</Button>
          )}
        </div>
      </div>

      {/* Duplicate detection is surfaced where the agent already is, rather
          than hidden in an admin screen nobody opens. */}
      {dupes.length > 0 && (
        <div
          className="flex flex-wrap items-center gap-3 rounded-md px-4 py-3 text-sm"
          style={{ background: "var(--status-warning-bg)", color: "var(--status-warning)" }}
        >
          <span aria-hidden>⚑</span>
          <span className="flex-1">
            {dupes.length} possible duplicate{dupes.length === 1 ? "" : "s"} of this contact.
          </span>
          <Button size="sm" onClick={() => setMergeOpen(true)}>Review</Button>
        </div>
      )}

      <div className="grid gap-4 lg:grid-cols-3">
        <div className="space-y-4 lg:col-span-2">
          <Card
            title={`Requests (${requests.data?.total ?? 0})`}
            actions={
              <Link className="text-sm underline underline-offset-2" to={`/requests?contactId=${id}`}>
                All
              </Link>
            }
          >
            {requests.isLoading ? (
              <Spinner />
            ) : (requests.data?.items.length ?? 0) === 0 ? (
              <Empty title="No service requests" hint="This contact has never filed one." />
            ) : (
              <table className="cc-table">
                <thead>
                  <tr>
                    <th>Reference</th>
                    <th>Subject</th>
                    <th>Status</th>
                    <th>Opened</th>
                  </tr>
                </thead>
                <tbody>
                  {requests.data!.items.map((r) => (
                    <tr key={r.id}>
                      <td>
                        <Link className="font-mono text-xs underline underline-offset-2" to={`/requests/${r.id}`}>
                          {r.reference}
                        </Link>
                      </td>
                      <td className="max-w-xs truncate">{r.subject}</td>
                      <td><StatusBadge status={r.status} /></td>
                      <td className="whitespace-nowrap text-ink-muted">{relativeTime(r.openedAt)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </Card>

          <Card title="History">
            {timeline.isLoading ? (
              <Spinner />
            ) : (timeline.data?.items.length ?? 0) === 0 ? (
              <Empty title="Nothing recorded yet" />
            ) : (
              <ol className="space-y-3">
                {timeline.data!.items.map((entry) => (
                  <li key={`${entry.type}-${entry.id}`} className="flex gap-3">
                    <span
                      className="mt-1.5 h-1.5 w-1.5 shrink-0 rounded-full"
                      style={{ background: "var(--border-strong)" }}
                      aria-hidden
                    />
                    <div className="min-w-0 flex-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="text-sm font-medium">{entry.title}</span>
                        <Badge>{entry.type.replace(/_/g, " ")}</Badge>
                        {entry.reference && (
                          <Link
                            className="font-mono text-xs underline underline-offset-2"
                            to={`/requests/${entry.requestId}`}
                          >
                            {entry.reference}
                          </Link>
                        )}
                      </div>
                      {entry.body && (
                        <p className="mt-0.5 whitespace-pre-wrap text-sm text-ink-muted">{entry.body}</p>
                      )}
                      <p className="text-xs text-ink-faint">
                        {entry.actorName ? `${entry.actorName} · ` : ""}{formatDateTime(entry.at)}
                      </p>
                    </div>
                  </li>
                ))}
              </ol>
            )}
          </Card>
        </div>

        <div className="space-y-4">
          <Card title="Details">
            <dl className="space-y-2 text-sm">
              <Row label="Email" value={c.primaryEmail || "—"} />
              <Row label="Phone" value={c.primaryPhone || "—"} />
              <Row label="Address" value={[c.address1, c.city, c.postalCode].filter(Boolean).join(", ") || "—"} />
              <Row label="Ward" value={c.ward || "—"} />
              <Row label="Language" value={c.preferredLanguage} />
            </dl>
            {c.notes && (
              <>
                <p className="cc-label mt-4">Notes</p>
                <p className="whitespace-pre-wrap text-sm">{c.notes}</p>
              </>
            )}
          </Card>

          <Card title="Identities">
            {(c.identities?.length ?? 0) === 0 ? (
              <p className="text-sm text-ink-faint">
                No external identities. Without a C2 link this citizen cannot be notified
                electronically.
              </p>
            ) : (
              <ul className="space-y-2 text-sm">
                {c.identities!.map((ident) => (
                  <li key={ident.id}>
                    <div className="flex items-center gap-2">
                      <Badge tone={ident.provider === "c2" ? "accent" : "neutral"}>
                        {ident.provider.toUpperCase()}
                      </Badge>
                      {ident.verified && <Badge tone="good">Verified</Badge>}
                    </div>
                    <code className="mt-0.5 block truncate text-xs text-ink-muted" title={ident.externalId}>
                      {ident.externalId}
                    </code>
                  </li>
                ))}
              </ul>
            )}
          </Card>

          {!c.c2Reachable && (
            <Card title="Reachability">
              <p className="text-sm text-ink-muted">
                C2 last refused a notification for this citizen
                {c.c2UnreachableCode === "no_consent" && " because they have withdrawn consent for CityConnect"}
                {c.c2UnreachableCode === "unknown_sub" && " because it does not recognise the linked identity"}
                . Reach them by phone or post until they re-consent in their own portal.
              </p>
            </Card>
          )}
        </div>
      </div>

      <Modal open={newRequestOpen} onClose={() => setNewRequestOpen(false)} title="New service request" wide>
        <NewRequestForm contactId={id} onCreated={() => setNewRequestOpen(false)} />
      </Modal>

      <LogInteractionDialog
        contactId={id}
        open={logOpen}
        onClose={() => setLogOpen(false)}
        onLogged={() => {
          setLogOpen(false);
          void queryClient.invalidateQueries({ queryKey: ["timeline", id] });
        }}
      />

      <MergeDialog
        contactId={id}
        candidates={dupes}
        open={mergeOpen}
        onClose={() => setMergeOpen(false)}
        onMerged={() => {
          setMergeOpen(false);
          void queryClient.invalidateQueries({ queryKey: ["contact", id] });
          void queryClient.invalidateQueries({ queryKey: ["duplicates", id] });
        }}
      />
    </div>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between gap-3">
      <dt className="shrink-0 text-ink-muted">{label}</dt>
      <dd className="text-right">{value}</dd>
    </div>
  );
}

function LogInteractionDialog({ contactId, open, onClose, onLogged }: {
  contactId: string;
  open: boolean;
  onClose: () => void;
  onLogged: () => void;
}) {
  const [kind, setKind] = useState("call");
  const [direction, setDirection] = useState("inbound");
  const [subject, setSubject] = useState("");
  const [summary, setSummary] = useState("");

  const log = useMutation({
    mutationFn: () =>
      api.createInteraction({
        contactId,
        kind: kind as never,
        direction: direction as never,
        subject,
        summary,
      }),
    onSuccess: () => { setSubject(""); setSummary(""); onLogged(); },
  });

  return (
    <Modal open={open} onClose={onClose} title="Log an interaction">
      <div className="space-y-3">
        {log.error && <ErrorNote error={log.error} />}
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Type">
            <Select value={kind} onChange={(e) => setKind(e.target.value)}>
              <option value="call">Phone call</option>
              <option value="email">Email</option>
              <option value="meeting">Meeting</option>
              <option value="walk_in">Counter visit</option>
              <option value="sms">Text message</option>
              <option value="note">Note</option>
            </Select>
          </Field>
          <Field label="Direction">
            <Select value={direction} onChange={(e) => setDirection(e.target.value)}>
              <option value="inbound">They contacted us</option>
              <option value="outbound">We contacted them</option>
              <option value="internal">Internal</option>
            </Select>
          </Field>
        </div>
        <Field label="Subject">
          <Input value={subject} onChange={(e) => setSubject(e.target.value)} />
        </Field>
        <Field label="What happened">
          <Textarea rows={4} value={summary} onChange={(e) => setSummary(e.target.value)} />
        </Field>
        <div className="flex justify-end gap-2 pt-1">
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" disabled={log.isPending} onClick={() => log.mutate()}>
            {log.isPending ? "Saving…" : "Log it"}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function MergeDialog({ contactId, candidates, open, onClose, onMerged }: {
  contactId: string;
  candidates: { contact: { id: string; displayName: string; primaryEmail?: string; primaryPhone?: string }; score: number; reasons: string[] }[];
  open: boolean;
  onClose: () => void;
  onMerged: () => void;
}) {
  const [chosen, setChosen] = useState("");

  const merge = useMutation({
    mutationFn: () => api.merge(contactId, { mergedId: chosen }),
    onSuccess: onMerged,
  });

  return (
    <Modal open={open} onClose={onClose} title="Possible duplicates" wide>
      <div className="space-y-3">
        {merge.error && <ErrorNote error={merge.error} />}
        <p className="text-sm text-ink-muted">
          Merging moves the other record's requests, interactions and identities onto this
          contact. It can be undone from the merge history.
        </p>

        <ul className="space-y-2">
          {candidates.map((cand) => (
            <li key={cand.contact.id}>
              <label
                className="flex cursor-pointer items-start gap-3 rounded-md border p-3"
                style={{
                  borderColor: chosen === cand.contact.id ? "var(--accent)" : "var(--border)",
                }}
              >
                <input
                  type="radio"
                  name="duplicate"
                  className="mt-1"
                  checked={chosen === cand.contact.id}
                  onChange={() => setChosen(cand.contact.id)}
                />
                <div className="min-w-0 flex-1">
                  <p className="font-medium">{cand.contact.displayName}</p>
                  <p className="text-sm text-ink-muted">
                    {[cand.contact.primaryEmail, cand.contact.primaryPhone].filter(Boolean).join(" · ")}
                  </p>
                  <div className="mt-1 flex flex-wrap gap-1">
                    <Badge tone={cand.score >= 60 ? "warning" : "neutral"}>{cand.score}% match</Badge>
                    {cand.reasons.map((reason) => <Badge key={reason}>{reason}</Badge>)}
                  </div>
                </div>
              </label>
            </li>
          ))}
        </ul>

        <div className="flex justify-end gap-2 pt-1">
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" disabled={!chosen || merge.isPending} onClick={() => merge.mutate()}>
            {merge.isPending ? "Merging…" : "Merge into this contact"}
          </Button>
        </div>
      </div>
    </Modal>
  );
}
