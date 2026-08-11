import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";

import { api } from "@/lib/api";
import type { FormField, ServiceType } from "@/lib/types";
import { Button, ErrorNote, Field, Input, Select, Textarea } from "./ui";

/**
 * Intake form. The extra fields are rendered from the service type's own
 * schema, so adding a question to "Pothole repair" is an admin change rather
 * than a front-end release.
 */
export default function NewRequestForm({
  contactId: fixedContactId,
  onCreated,
}: {
  contactId?: string;
  onCreated?: (id: string) => void;
}) {
  const navigate = useNavigate();

  const [contactId, setContactId] = useState(fixedContactId ?? "");
  const [contactSearch, setContactSearch] = useState("");
  const [serviceTypeId, setServiceTypeId] = useState("");
  const [subject, setSubject] = useState("");
  const [description, setDescription] = useState("");
  const [priority, setPriority] = useState("");
  const [address1, setAddress1] = useState("");
  const [city, setCity] = useState("");
  const [postalCode, setPostalCode] = useState("");
  const [ward, setWard] = useState("");
  const [formData, setFormData] = useState<Record<string, unknown>>({});

  const serviceTypes = useQuery({ queryKey: ["service-types"], queryFn: () => api.serviceTypes() });

  const contacts = useQuery({
    queryKey: ["contact-search", contactSearch],
    queryFn: () => api.contacts({ q: contactSearch, limit: 10 }),
    enabled: !fixedContactId && contactSearch.trim().length >= 2,
  });

  const selectedType: ServiceType | undefined = serviceTypes.data?.items.find((t) => t.id === serviceTypeId);
  const fields: FormField[] = selectedType?.intakeForm?.fields ?? [];

  const create = useMutation({
    mutationFn: () =>
      api.createRequest({
        contactId, serviceTypeId, subject, description,
        priority: priority || undefined,
        address1, city, postalCode, ward,
        formData,
        source: "agent",
      }),
    onSuccess: (req) => {
      if (onCreated) onCreated(req.id);
      else navigate(`/requests/${req.id}`);
    },
  });

  const needsLocation = selectedType?.requiresLocation ?? false;
  const ready = contactId && serviceTypeId && (!needsLocation || address1.trim().length > 0);

  return (
    <form
      className="space-y-4"
      onSubmit={(e) => {
        e.preventDefault();
        create.mutate();
      }}
    >
      {create.error && <ErrorNote error={create.error} />}

      {!fixedContactId && (
        <Field label="Contact" hint="Search by name, email or phone. Create the contact first if they are new.">
          <Input
            value={contactSearch}
            onChange={(e) => { setContactSearch(e.target.value); setContactId(""); }}
            placeholder="Start typing a name…"
          />
          {contacts.data && contactSearch.length >= 2 && !contactId && (
            <ul className="mt-1 max-h-40 overflow-y-auto rounded-md border" style={{ borderColor: "var(--border)" }}>
              {contacts.data.items.length === 0 ? (
                <li className="px-3 py-2 text-sm text-ink-muted">No matches.</li>
              ) : (
                contacts.data.items.map((c) => (
                  <li key={c.id}>
                    <button
                      type="button"
                      className="block w-full px-3 py-2 text-left text-sm hover:bg-[var(--surface-0)]"
                      onClick={() => { setContactId(c.id); setContactSearch(c.displayName); }}
                    >
                      {c.displayName}
                      <span className="ml-2 text-xs text-ink-faint">{c.primaryEmail ?? c.primaryPhone}</span>
                    </button>
                  </li>
                ))
              )}
            </ul>
          )}
        </Field>
      )}

      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Service type">
          <Select
            required
            value={serviceTypeId}
            onChange={(e) => {
              setServiceTypeId(e.target.value);
              setFormData({});
              const st = serviceTypes.data?.items.find((t) => t.id === e.target.value);
              if (st && !subject) setSubject(st.name);
            }}
          >
            <option value="">Choose a service…</option>
            {(serviceTypes.data?.items ?? []).map((st) => (
              <option key={st.id} value={st.id}>
                {st.category ? `${st.category} — ${st.name}` : st.name}
              </option>
            ))}
          </Select>
        </Field>

        <Field label="Priority" hint={selectedType ? `Default: ${selectedType.defaultPriority}` : undefined}>
          <Select value={priority} onChange={(e) => setPriority(e.target.value)}>
            <option value="">Use the service default</option>
            {["low", "normal", "high", "urgent", "critical"].map((p) => (
              <option key={p} value={p}>{p}</option>
            ))}
          </Select>
        </Field>
      </div>

      <Field label="Subject">
        <Input value={subject} onChange={(e) => setSubject(e.target.value)} placeholder="Short summary" />
      </Field>

      <Field label="Description">
        <Textarea rows={3} value={description} onChange={(e) => setDescription(e.target.value)} />
      </Field>

      <fieldset className="rounded-md border p-3" style={{ borderColor: "var(--border)" }}>
        <legend className="px-1 text-xs font-medium uppercase tracking-wide text-ink-muted">
          Location {needsLocation && <span style={{ color: "var(--status-critical)" }}>· required</span>}
        </legend>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Address">
            <Input value={address1} onChange={(e) => setAddress1(e.target.value)} required={needsLocation} />
          </Field>
          <Field label="City">
            <Input value={city} onChange={(e) => setCity(e.target.value)} />
          </Field>
          <Field label="Postal code">
            <Input value={postalCode} onChange={(e) => setPostalCode(e.target.value)} />
          </Field>
          <Field label="Ward">
            <Input value={ward} onChange={(e) => setWard(e.target.value)} />
          </Field>
        </div>
      </fieldset>

      {fields.length > 0 && (
        <fieldset className="rounded-md border p-3" style={{ borderColor: "var(--border)" }}>
          <legend className="px-1 text-xs font-medium uppercase tracking-wide text-ink-muted">
            {selectedType?.name} details
          </legend>
          <div className="grid gap-3 sm:grid-cols-2">
            {fields.map((f) => (
              <DynamicField
                key={f.key}
                field={f}
                value={formData[f.key]}
                onChange={(v) => setFormData((prev) => ({ ...prev, [f.key]: v }))}
              />
            ))}
          </div>
        </fieldset>
      )}

      <div className="flex justify-end gap-2">
        <Button type="submit" variant="primary" disabled={!ready || create.isPending}>
          {create.isPending ? "Creating…" : "Create request"}
        </Button>
      </div>
    </form>
  );
}

function DynamicField({ field, value, onChange }: {
  field: FormField;
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const label = field.required ? `${field.label} *` : field.label;

  switch (field.type) {
    case "checkbox":
      return (
        <label className="flex items-center gap-2 self-end pb-2 text-sm">
          <input
            type="checkbox"
            checked={Boolean(value)}
            onChange={(e) => onChange(e.target.checked)}
          />
          {label}
        </label>
      );
    case "select":
      return (
        <Field label={label} hint={field.help}>
          <Select required={field.required} value={String(value ?? "")} onChange={(e) => onChange(e.target.value)}>
            <option value="">Choose…</option>
            {(field.options ?? []).map((o) => (
              <option key={o} value={o}>{o}</option>
            ))}
          </Select>
        </Field>
      );
    case "textarea":
      return (
        <Field label={label} hint={field.help}>
          <Textarea rows={2} required={field.required} value={String(value ?? "")} onChange={(e) => onChange(e.target.value)} />
        </Field>
      );
    case "number":
      return (
        <Field label={label} hint={field.help}>
          <Input type="number" required={field.required} value={String(value ?? "")} onChange={(e) => onChange(Number(e.target.value))} />
        </Field>
      );
    case "date":
      return (
        <Field label={label} hint={field.help}>
          <Input type="date" required={field.required} value={String(value ?? "")} onChange={(e) => onChange(e.target.value)} />
        </Field>
      );
    default:
      return (
        <Field label={label} hint={field.help}>
          <Input required={field.required} value={String(value ?? "")} onChange={(e) => onChange(e.target.value)} />
        </Field>
      );
  }
}
