import { useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api } from "@/lib/api";
import { useAuth } from "@/hooks/useAuth";
import {
  Badge, Button, Card, Empty, ErrorNote, Field, formatDate, Input, Modal, Pager, Select, Spinner,
} from "@/components/ui";

export default function ContactList() {
  const [params, setParams] = useSearchParams();
  const { can } = useAuth();
  const queryClient = useQueryClient();
  const [newOpen, setNewOpen] = useState(false);

  const limit = 50;
  const offset = Number(params.get("offset") ?? 0);

  const filters = {
    q: params.get("q") ?? undefined,
    ward: params.get("ward") ?? undefined,
    hasC2Link: params.get("hasC2Link") || undefined,
    c2Reachable: params.get("c2Reachable") || undefined,
    limit,
    offset,
  };

  const list = useQuery({ queryKey: ["contacts", filters], queryFn: () => api.contacts(filters) });

  function setFilter(key: string, value: string | null) {
    const next = new URLSearchParams(params);
    if (!value) next.delete(key);
    else next.set(key, value);
    next.delete("offset");
    setParams(next);
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold">Contacts</h1>
          <p className="text-sm text-ink-muted">
            {list.data ? `${list.data.total.toLocaleString()} records` : "Loading…"}
          </p>
        </div>
        {can("contact:write") && (
          <Button variant="primary" onClick={() => setNewOpen(true)}>New contact</Button>
        )}
      </div>

      <Card>
        <div className="grid gap-3 md:grid-cols-4">
          <Field label="Search">
            <Input
              defaultValue={params.get("q") ?? ""}
              placeholder="Name, email, phone, organisation"
              onKeyDown={(e) => {
                if (e.key === "Enter") setFilter("q", (e.target as HTMLInputElement).value);
              }}
            />
          </Field>
          <Field label="Ward">
            <Input defaultValue={params.get("ward") ?? ""} onKeyDown={(e) => {
              if (e.key === "Enter") setFilter("ward", (e.target as HTMLInputElement).value);
            }} />
          </Field>
          <Field label="C2 account">
            <Select value={params.get("hasC2Link") ?? ""} onChange={(e) => setFilter("hasC2Link", e.target.value)}>
              <option value="">Any</option>
              <option value="true">Linked to C2</option>
              <option value="false">No C2 account</option>
            </Select>
          </Field>
          <Field label="Reachability">
            <Select value={params.get("c2Reachable") ?? ""} onChange={(e) => setFilter("c2Reachable", e.target.value)}>
              <option value="">Any</option>
              <option value="true">Reachable</option>
              <option value="false">Not reachable through C2</option>
            </Select>
          </Field>
        </div>
      </Card>

      <Card className="overflow-hidden !p-0">
        {list.isLoading ? (
          <Spinner />
        ) : list.error ? (
          <ErrorNote error={list.error} onRetry={() => void list.refetch()} />
        ) : (list.data?.items.length ?? 0) === 0 ? (
          <Empty title="No contacts match" hint="Try a different search term." />
        ) : (
          <div className="overflow-x-auto">
            <table className="cc-table">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Email</th>
                  <th>Phone</th>
                  <th>Ward</th>
                  <th>Flags</th>
                  <th>Added</th>
                </tr>
              </thead>
              <tbody>
                {list.data!.items.map((c) => (
                  <tr key={c.id}>
                    <td>
                      <Link to={`/contacts/${c.id}`} className="font-medium hover:underline">
                        {c.displayName}
                      </Link>
                      {c.organization && (
                        <span className="block text-xs text-ink-faint">{c.organization}</span>
                      )}
                    </td>
                    <td className="text-ink-muted">{c.primaryEmail || "—"}</td>
                    <td className="text-ink-muted">{c.primaryPhone || "—"}</td>
                    <td>{c.ward || "—"}</td>
                    <td>
                      <div className="flex flex-wrap gap-1">
                        {c.doNotContact && <Badge tone="critical">Do not contact</Badge>}
                        {!c.c2Reachable && <Badge tone="warning">⚑ Not reachable</Badge>}
                        {c.status === "merged" && <Badge>Merged</Badge>}
                      </div>
                    </td>
                    <td className="whitespace-nowrap text-ink-muted">{formatDate(c.createdAt)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {list.data && (
          <Pager total={list.data.total} limit={limit} offset={offset}
            onChange={(next) => setFilter("offset", String(next))} />
        )}
      </Card>

      <NewContactDialog
        open={newOpen}
        onClose={() => setNewOpen(false)}
        onCreated={() => {
          setNewOpen(false);
          void queryClient.invalidateQueries({ queryKey: ["contacts"] });
        }}
      />
    </div>
  );
}

function NewContactDialog({ open, onClose, onCreated }: {
  open: boolean;
  onClose: () => void;
  onCreated: () => void;
}) {
  const [form, setForm] = useState({
    displayName: "", givenName: "", familyName: "", organization: "",
    primaryEmail: "", primaryPhone: "", address1: "", city: "", postalCode: "", ward: "",
  });

  const create = useMutation({
    mutationFn: () => api.createContact(form),
    onSuccess: onCreated,
  });

  const set = (key: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement>) =>
    setForm((prev) => ({ ...prev, [key]: e.target.value }));

  return (
    <Modal open={open} onClose={onClose} title="New contact" wide>
      <div className="space-y-3">
        {create.error && <ErrorNote error={create.error} />}
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Given name"><Input value={form.givenName} onChange={set("givenName")} /></Field>
          <Field label="Family name"><Input value={form.familyName} onChange={set("familyName")} /></Field>
          <Field label="Display name" hint="Filled in from the names above if left blank">
            <Input value={form.displayName} onChange={set("displayName")} />
          </Field>
          <Field label="Organisation"><Input value={form.organization} onChange={set("organization")} /></Field>
          <Field label="Email"><Input type="email" value={form.primaryEmail} onChange={set("primaryEmail")} /></Field>
          <Field label="Phone"><Input value={form.primaryPhone} onChange={set("primaryPhone")} /></Field>
          <Field label="Address"><Input value={form.address1} onChange={set("address1")} /></Field>
          <Field label="City"><Input value={form.city} onChange={set("city")} /></Field>
          <Field label="Postal code"><Input value={form.postalCode} onChange={set("postalCode")} /></Field>
          <Field label="Ward"><Input value={form.ward} onChange={set("ward")} /></Field>
        </div>
        <div className="flex justify-end gap-2 pt-1">
          <Button onClick={onClose}>Cancel</Button>
          <Button
            variant="primary"
            disabled={create.isPending || !(form.displayName || form.givenName || form.familyName || form.organization)}
            onClick={() => create.mutate()}
          >
            {create.isPending ? "Creating…" : "Create contact"}
          </Button>
        </div>
      </div>
    </Modal>
  );
}
