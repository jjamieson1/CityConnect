import { useEffect, useMemo, useState } from "react";
import { Link, Navigate, Route, Routes, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { portalApi } from "./lib/api";
import type { CatalogEntry, FormField, MyRequest } from "./lib/types";
import {
  Badge, Button, Empty, ErrorNote, Field, formatDate, Input, Select, Spinner, Textarea, cx,
} from "@shared/ui";
import { ErrorBoundary } from "@shared/ui/ErrorBoundary";

/**
 * The citizen portal.
 *
 * Written for someone who uses it twice a year, not an agent who lives in it:
 * bigger targets, plainer words, one task per screen, and no CityConnect
 * vocabulary — a resident should never see "queue", "SLA" or "triaged".
 */
export default function Portal() {
  const profile = useQuery({
    queryKey: ["portal", "me"],
    queryFn: () => portalApi.me(),
    retry: false,
  });

  const signedIn = !!profile.data && !profile.isError;

  // Once signed in, reset the silent-SSO guard so a later sign-out can probe
  // again on the next visit.
  useEffect(() => {
    if (signedIn) sessionStorage.removeItem(SILENT_SSO_TRIED);
  }, [signedIn]);

  return (
    <div className="min-h-screen" style={{ background: "var(--surface-0)" }}>
      <header className="border-b" style={{ background: "var(--surface-1)", borderColor: "var(--border)" }}>
        <div className="mx-auto flex max-w-4xl items-center gap-4 px-4 py-3">
          <Link to="/" className="flex items-center gap-2 font-semibold">
            <span
              className="grid h-8 w-8 place-items-center rounded text-xs font-bold text-white"
              style={{ background: "var(--accent)" }}
              aria-hidden
            >
              CC
            </span>
            City services
          </Link>

          <nav className="ml-auto flex items-center gap-1 text-sm" aria-label="Portal">
            {signedIn && (
              <Link className="rounded px-3 py-1.5 hover:bg-[var(--surface-0)]" to="/">
                Report something
              </Link>
            )}
            {/* Always offered, signed in or not. Someone who reported a pothole
                without an account is exactly who needs this, and hiding it
                behind sign-in is how they end up phoning instead. */}
            <Link className="rounded px-3 py-1.5 hover:bg-[var(--surface-0)]" to="/track">
              Check a report
            </Link>
            {signedIn && (
              <>
                <Link className="rounded px-3 py-1.5 hover:bg-[var(--surface-0)]" to="/requests">
                  My reports
                </Link>
                <Button size="sm" onClick={() => void signOut()}>
                  Sign out
                </Button>
              </>
            )}
          </nav>
        </div>
      </header>

      <main className="mx-auto max-w-4xl px-4 py-6">
        <ErrorBoundary area="page">
          {profile.isLoading ? (
            <Spinner label="Loading" />
          ) : (
            <Routes>
              <Route index element={<Landing signedIn={signedIn} />} />
              <Route path="/new/:code" element={<Report signedIn={signedIn} />} />
              {/* Public by design: no signedIn prop, because needing an account
                  is precisely what this route exists to avoid. */}
              <Route path="/track" element={<Track />} />
              {/* Where an anonymous report lands. It has no detail page,
                  because there is nothing to authorise showing it again. */}
              <Route path="/submitted/:reference" element={<AnonymousSubmitted />} />
              <Route path="/requests" element={<MyReports signedIn={signedIn} />} />
              <Route path="/requests/:reference" element={<ReportDetail signedIn={signedIn} />} />
              <Route path="*" element={<Navigate to="/" replace />} />
            </Routes>
          )}
        </ErrorBoundary>
      </main>

      <footer
        className="border-t px-4 py-4 text-center text-xs"
        style={{ borderColor: "var(--border)", color: "var(--text-muted)" }}
      >
        Reports you make here are handled by City staff. In an emergency, call 911.
      </footer>
    </div>
  );
}

async function signOut() {
  let endSessionUrl: string | undefined;
  try {
    ({ endSessionUrl } = await portalApi.logout());
  } catch {
    /* a failed sign-out still clears the local view */
  }
  window.location.href = endSessionUrl ?? import.meta.env.BASE_URL;
}

// Per-tab, one-shot guard so a silent SSO that finds no C2 session cannot loop.
const SILENT_SSO_TRIED = "cc.portalSilentSsoTried";

/** SignInPrompt is shown wherever an action needs an identity. */
function SignInPrompt({ what }: { what: string }) {
  // The resident may already hold a live C2 session. Try a silent (prompt=none)
  // SSO once per tab before asking them to click — a signed-in resident is
  // carried straight through with no screen. If C2 has no session it answers
  // login_required and the callback returns here, where the button shows.
  const [probing] = useState(() => !sessionStorage.getItem(SILENT_SSO_TRIED));
  useEffect(() => {
    if (!sessionStorage.getItem(SILENT_SSO_TRIED)) {
      sessionStorage.setItem(SILENT_SSO_TRIED, "1");
      // Full-page navigation: the authorization flow is a browser redirect
      // through C2 and back. replace() keeps it out of history.
      window.location.replace(
        portalApi.loginUrl(location.pathname + location.search, { silent: true }),
      );
    }
  }, []);

  if (probing) {
    return (
      <div className="cc-card p-6 text-center">
        <Spinner label="Signing you in" />
      </div>
    );
  }

  return (
    <div className="cc-card p-6 text-center">
      <h2 className="text-lg font-semibold">Sign in to {what}</h2>
      <p className="mx-auto mt-2 max-w-md text-sm text-ink-muted">
        We use your City account so we can tell you what happens next, and so only you can see
        what you have reported.
      </p>
      <Button
        variant="primary"
        className="mt-4"
        onClick={() => {
          window.location.href = portalApi.loginUrl(location.pathname + location.search);
        }}
      >
        Sign in
      </Button>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Landing — the service catalogue
// ---------------------------------------------------------------------------

function Landing({ signedIn }: { signedIn: boolean }) {
  const [params] = useSearchParams();
  const [search, setSearch] = useState("");

  const catalog = useQuery({ queryKey: ["portal", "catalog"], queryFn: () => portalApi.catalog() });
  const mine = useQuery({
    queryKey: ["portal", "requests", "open"],
    queryFn: () => portalApi.myRequests(true),
    enabled: signedIn,
  });

  // Grouped by category, because a resident scans for the area of life their
  // problem belongs to rather than reading an alphabetical list.
  const grouped = useMemo(() => {
    const items = (catalog.data?.items ?? []).filter((c) =>
      search.trim() === ""
        ? true
        : `${c.name} ${c.description ?? ""} ${c.category ?? ""}`
            .toLowerCase()
            .includes(search.toLowerCase()),
    );
    const byCategory = new Map<string, CatalogEntry[]>();
    for (const item of items) {
      const key = item.category || "Other";
      byCategory.set(key, [...(byCategory.get(key) ?? []), item]);
    }
    return [...byCategory.entries()].sort(([a], [b]) => a.localeCompare(b));
  }, [catalog.data, search]);

  return (
    <div className="space-y-6">
      {params.get("reason") && (
        <div
          role="alert"
          className="rounded-md px-4 py-3 text-sm"
          style={{ background: "var(--status-warning-bg)", color: "var(--status-warning)" }}
        >
          We could not complete that sign-in. Please try again.
        </div>
      )}

      <div>
        <h1 className="text-2xl font-semibold">What would you like to report?</h1>
        <p className="mt-1 text-ink-muted">
          Choose a service below. We will confirm we have it and keep you posted.
        </p>
      </div>

      {signedIn && (mine.data?.items.length ?? 0) > 0 && (
        <div className="cc-card p-4">
          <div className="flex items-center justify-between gap-3">
            <p className="text-sm">
              You have{" "}
              <strong>
                {mine.data!.items.length} open {mine.data!.items.length === 1 ? "report" : "reports"}
              </strong>
              .
            </p>
            <Link className="text-sm underline underline-offset-2" to="/requests">
              View them
            </Link>
          </div>
        </div>
      )}

      <Input
        value={search}
        onChange={(e) => setSearch(e.target.value)}
        placeholder="Search services — pothole, garbage, noise…"
        aria-label="Search services"
      />

      {catalog.isLoading ? (
        <Spinner />
      ) : catalog.error ? (
        <ErrorNote error={catalog.error} onRetry={() => void catalog.refetch()} />
      ) : grouped.length === 0 ? (
        <Empty title="Nothing matches that" hint="Try a different word, or browse the full list." />
      ) : (
        grouped.map(([category, items]) => (
          <section key={category}>
            <h2 className="mb-2 text-sm font-semibold uppercase tracking-wide text-ink-muted">
              {category}
            </h2>
            <div className="grid gap-3 sm:grid-cols-2">
              {items.map((entry) => (
                <Link
                  key={entry.id}
                  to={`/new/${entry.code}`}
                  className="cc-card block p-4 transition-colors hover:border-[var(--accent)]"
                >
                  <p className="font-medium">{entry.name}</p>
                  {entry.description && (
                    <p className="mt-1 text-sm text-ink-muted">{entry.description}</p>
                  )}
                  {entry.department && (
                    <p className="mt-2 text-xs text-ink-faint">Handled by {entry.department}</p>
                  )}
                </Link>
              ))}
            </div>
          </section>
        ))
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Report — the intake form
// ---------------------------------------------------------------------------

function Report({ signedIn }: { signedIn: boolean }) {
  const { code = "" } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();

  const catalog = useQuery({ queryKey: ["portal", "catalog"], queryFn: () => portalApi.catalog() });
  const entry = catalog.data?.items.find((c) => c.code === code);

  const [form, setForm] = useState({
    subject: "", description: "", address1: "", city: "", postalCode: "",
  });
  const [extra, setExtra] = useState<Record<string, unknown>>({});
  // The honeypot. Never shown, never focusable, never announced — so any value
  // here came from something filling in fields it could not see.
  const [websiteUrl, setWebsiteUrl] = useState("");

  // Fetched on mount, not on submit: the server checks that a plausible amount
  // of time passed between the two, which is what distinguishes a person
  // filling in a form from a script posting one.
  const formToken = useQuery({
    queryKey: ["portal", "form-token", code],
    queryFn: () => portalApi.formToken(),
    enabled: !signedIn,
    staleTime: Infinity,
    gcTime: 0,
    retry: false,
  });

  // Stable for the life of this form, so a double click replays the first
  // result rather than filing a second report. A new key per attempt would
  // defeat the point.
  const [submissionKey] = useState(() => crypto.randomUUID());

  const submit = useMutation({
    mutationFn: () =>
      portalApi.report({
        serviceTypeId: entry!.id,
        subject: form.subject || entry!.name,
        description: form.description,
        address1: form.address1, city: form.city, postalCode: form.postalCode,
        formData: extra,
        formToken: formToken.data?.token,
        websiteUrl,
      }, submissionKey),
    onSuccess: (created) => {
      void queryClient.invalidateQueries({ queryKey: ["portal", "requests"] });
      // An anonymous report cannot be reopened by reference, so there is no
      // detail page to send them to. The confirmation route carries everything
      // they will ever be able to see about it.
      if (created.trackable) navigate(`/requests/${created.reference}?new=1`);
      else navigate(`/submitted/${created.reference}`);
    },
  });

  if (catalog.isLoading) return <Spinner />;
  if (!entry) {
    return (
      <Empty
        title="We could not find that service"
        hint="It may have been renamed."
        action={<Link className="cc-btn cc-btn-primary mt-3" to="/">Back to services</Link>}
      />
    );
  }

  const locationGiven = !entry.requiresLocation || form.address1.trim().length > 0;
  // Signed out, the submission also needs its token. Waiting for it here means
  // a resident sees a disabled button for a moment rather than filling the
  // whole form and being refused at the end.
  const ready = locationGiven && (signedIn || !!formToken.data?.token);

  return (
    <div className="space-y-4">
      <Link className="text-sm underline underline-offset-2" to="/">
        ← All services
      </Link>

      <div>
        <h1 className="text-2xl font-semibold">{entry.name}</h1>
        {entry.description && <p className="mt-1 text-ink-muted">{entry.description}</p>}
      </div>

      <form
        className="cc-card space-y-4 p-5"
        onSubmit={(e) => {
          e.preventDefault();
          submit.mutate();
        }}
      >
        {submit.error ? <ErrorNote error={submit.error} /> : null}

        {/*
          The honeypot.

          Hidden three ways on purpose, because one is not enough: off-screen so
          nobody sees it, tabIndex -1 so it is not in the keyboard order, and
          aria-hidden so a screen reader never announces it. A field a resident
          could reach by any route would be a trap for them rather than for a
          bot, which is precisely the failure mode a CAPTCHA has.

          Not display:none — some form fillers skip hidden inputs, and the point
          is to be filled in.
        */}
        <div aria-hidden="true" className="absolute h-px w-px overflow-hidden" style={{ left: -9999 }}>
          <label htmlFor="website-url">Leave this field empty</label>
          <input
            id="website-url"
            name="websiteUrl"
            type="text"
            tabIndex={-1}
            autoComplete="off"
            value={websiteUrl}
            onChange={(e) => setWebsiteUrl(e.target.value)}
          />
        </div>

        <Field label="What is the problem?" hint="A short summary helps us route it quickly.">
          <Input
            value={form.subject}
            onChange={(e) => setForm({ ...form, subject: e.target.value })}
            placeholder={entry.name}
          />
        </Field>

        <Field label="Tell us more">
          <Textarea
            rows={4}
            value={form.description}
            onChange={(e) => setForm({ ...form, description: e.target.value })}
            placeholder="Anything that would help the crew find it or understand the problem."
          />
        </Field>

        <fieldset className="rounded-md border p-4" style={{ borderColor: "var(--border)" }}>
          <legend className="px-1 text-sm font-medium">
            Where is it?{" "}
            {entry.requiresLocation && (
              <span style={{ color: "var(--status-critical)" }}>Required</span>
            )}
          </legend>
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="sm:col-span-2">
              <Field label="Street address or nearest intersection">
                <Input
                  required={entry.requiresLocation}
                  value={form.address1}
                  onChange={(e) => setForm({ ...form, address1: e.target.value })}
                />
              </Field>
            </div>
            <Field label="City">
              <Input value={form.city} onChange={(e) => setForm({ ...form, city: e.target.value })} />
            </Field>
            <Field label="Postal code">
              <Input
                value={form.postalCode}
                onChange={(e) => setForm({ ...form, postalCode: e.target.value })}
              />
            </Field>
          </div>
        </fieldset>

        {entry.fields.length > 0 && (
          <fieldset className="rounded-md border p-4" style={{ borderColor: "var(--border)" }}>
            <legend className="px-1 text-sm font-medium">A few details</legend>
            <div className="grid gap-3 sm:grid-cols-2">
              {entry.fields.map((f) => (
                <PortalField
                  key={f.key}
                  field={f}
                  value={extra[f.key]}
                  onChange={(v) => setExtra((prev) => ({ ...prev, [f.key]: v }))}
                />
              ))}
            </div>
          </fieldset>
        )}

        {!signedIn && (
          <div
            className="rounded-md px-4 py-3 text-sm"
            style={{ background: "var(--surface-0)" }}
          >
            <p className="font-medium">You are reporting without an account</p>
            <p className="mt-1 text-ink-muted">
              We will still act on it, and you will get a reference number. But with no way to
              reach you we cannot confirm it, send updates, or let you check on it later — not
              even with the reference.{" "}
              <a className="underline underline-offset-2" href={portalApi.loginUrl(window.location.pathname)}>
                Sign in first
              </a>{" "}
              if you would like to be kept posted.
            </p>
          </div>
        )}

        <div className="flex items-center justify-between gap-3">
          <p className="text-xs text-ink-faint">
            {signedIn
              ? "We will send you a reference number and keep you updated."
              : "We will give you a reference number when you submit."}
          </p>
          <Button type="submit" variant="primary" disabled={!ready || submit.isPending}>
            {submit.isPending ? "Sending…" : "Send report"}
          </Button>
        </div>
      </form>
    </div>
  );
}

/** PortalField renders one configured intake field. */
function PortalField({ field, value, onChange }: {
  field: FormField;
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const label = field.required ? `${field.label} *` : field.label;

  switch (field.type) {
    case "checkbox":
      return (
        <label className="flex items-center gap-2 self-end pb-2 text-sm sm:col-span-2">
          <input type="checkbox" checked={Boolean(value)} onChange={(e) => onChange(e.target.checked)} />
          {field.label}
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
        <div className="sm:col-span-2">
          <Field label={label} hint={field.help}>
            <Textarea rows={3} required={field.required} value={String(value ?? "")} onChange={(e) => onChange(e.target.value)} />
          </Field>
        </div>
      );
    case "date":
      return (
        <Field label={label} hint={field.help}>
          <Input type="date" required={field.required} value={String(value ?? "")} onChange={(e) => onChange(e.target.value)} />
        </Field>
      );
    case "number":
      return (
        <Field label={label} hint={field.help}>
          <Input type="number" required={field.required} value={String(value ?? "")} onChange={(e) => onChange(Number(e.target.value))} />
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

// ---------------------------------------------------------------------------
// My reports
// ---------------------------------------------------------------------------

function MyReports({ signedIn }: { signedIn: boolean }) {
  const [openOnly, setOpenOnly] = useState(false);
  const list = useQuery({
    queryKey: ["portal", "requests", openOnly],
    queryFn: () => portalApi.myRequests(openOnly),
    enabled: signedIn,
  });

  if (!signedIn) return <SignInPrompt what="see your reports" />;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h1 className="text-2xl font-semibold">My reports</h1>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={openOnly} onChange={(e) => setOpenOnly(e.target.checked)} />
          Only show open ones
        </label>
      </div>

      {list.isLoading ? (
        <Spinner />
      ) : list.error ? (
        <ErrorNote error={list.error} onRetry={() => void list.refetch()} />
      ) : (list.data?.items.length ?? 0) === 0 ? (
        <Empty
          title="You have not reported anything yet"
          hint="When you do, it will appear here with everything we have done about it."
          action={<Link className="cc-btn cc-btn-primary mt-3" to="/">Report something</Link>}
        />
      ) : (
        <ul className="space-y-3">
          {list.data!.items.map((r) => (
            <li key={r.reference}>
              <Link to={`/requests/${r.reference}`} className="cc-card block p-4 hover:border-[var(--accent)]">
                <div className="flex flex-wrap items-center gap-2">
                  <StatusPill request={r} />
                  <span className="font-mono text-xs text-ink-muted">{r.reference}</span>
                </div>
                <p className="mt-1 font-medium">{r.subject}</p>
                <p className="mt-0.5 text-sm text-ink-muted">
                  {r.serviceType}
                  {r.address ? ` · ${r.address}` : ""}
                  {` · reported ${formatDate(r.openedAt)}`}
                </p>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/**
 * StatusPill shows the citizen-facing label. It never shows the internal
 * status value, and pairs its colour with words so state is not carried by
 * colour alone.
 */
function StatusPill({ request }: { request: MyRequest }) {
  const tone =
    request.status === "resolved" || request.status === "closed" ? "good"
    : request.status === "waiting_citizen" ? "serious"
    : request.status === "cancelled" ? "neutral"
    : "accent";
  return <Badge tone={tone}>{request.statusLabel}</Badge>;
}

// ---------------------------------------------------------------------------
// Report detail
// ---------------------------------------------------------------------------

function ReportDetail({ signedIn }: { signedIn: boolean }) {
  const { reference = "" } = useParams();
  const [params] = useSearchParams();
  const queryClient = useQueryClient();

  const detail = useQuery({
    queryKey: ["portal", "request", reference],
    queryFn: () => portalApi.request(reference),
    enabled: signedIn,
  });

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ["portal", "request", reference] });
    void queryClient.invalidateQueries({ queryKey: ["portal", "requests"] });
  };

  if (!signedIn) return <SignInPrompt what="see this report" />;
  if (detail.isLoading) return <Spinner />;
  if (detail.error) {
    return (
      <Empty
        title="We could not find that report"
        hint="Check the reference number, or see all of your reports."
        action={<Link className="cc-btn cc-btn-primary mt-3" to="/requests">My reports</Link>}
      />
    );
  }

  const r = detail.data!;

  return (
    <div className="space-y-4">
      <Link className="text-sm underline underline-offset-2" to="/requests">
        ← My reports
      </Link>

      {params.get("new") && (
        <div
          className="rounded-md px-4 py-3 text-sm"
          style={{ background: "var(--status-good-bg)", color: "var(--status-good)" }}
        >
          Thank you — we have your report. Quote <strong>{r.reference}</strong> if you contact us
          about it.
        </div>
      )}

      <div className="cc-card p-5">
        <div className="flex flex-wrap items-center gap-2">
          <StatusPill request={r} />
          <span className="font-mono text-xs text-ink-muted">{r.reference}</span>
        </div>
        <h1 className="mt-2 text-2xl font-semibold">{r.subject}</h1>
        <p className="mt-1 text-sm text-ink-muted">
          {r.serviceType}
          {r.department ? ` · handled by ${r.department}` : ""}
        </p>

        <dl className="mt-4 grid gap-3 text-sm sm:grid-cols-2">
          <div>
            <dt className="text-xs uppercase tracking-wide text-ink-muted">Reported</dt>
            <dd>{formatDate(r.openedAt)}</dd>
          </div>
          {r.address && (
            <div>
              <dt className="text-xs uppercase tracking-wide text-ink-muted">Location</dt>
              <dd>{r.address}</dd>
            </div>
          )}
          {r.expectedBy && (
            <div>
              <dt className="text-xs uppercase tracking-wide text-ink-muted">Expected by</dt>
              <dd>{formatDate(r.expectedBy)}</dd>
            </div>
          )}
          {r.resolvedAt && (
            <div>
              <dt className="text-xs uppercase tracking-wide text-ink-muted">Completed</dt>
              <dd>{formatDate(r.resolvedAt)}</dd>
            </div>
          )}
        </dl>

        {r.description && <p className="mt-4 whitespace-pre-wrap text-sm">{r.description}</p>}
        {r.resolution && (
          <div className="mt-4 rounded-md p-3 text-sm" style={{ background: "var(--surface-0)" }}>
            <p className="font-medium">What we did</p>
            <p className="mt-1 whitespace-pre-wrap">{r.resolution}</p>
          </div>
        )}
      </div>

      {r.canRate && <RatingCard reference={r.reference} onDone={refresh} />}

      <section className="cc-card p-5">
        <h2 className="text-sm font-semibold">Progress</h2>
        {(r.updates?.length ?? 0) === 0 ? (
          <p className="mt-2 text-sm text-ink-muted">Nothing to report yet. We will update you here.</p>
        ) : (
          <ol className="mt-3 space-y-3">
            {r.updates!.map((u, i) => (
              <li
                key={i}
                className={cx("rounded-md p-3 text-sm", u.mine && "ml-6")}
                style={{ background: u.mine ? "var(--surface-0)" : "var(--surface-2)" }}
              >
                <div className="mb-1 flex items-center gap-2 text-xs text-ink-muted">
                  <span className="font-medium text-ink">{u.author}</span>
                  <span>{formatDate(u.at)}</span>
                </div>
                <p className="whitespace-pre-wrap">{u.body}</p>
              </li>
            ))}
          </ol>
        )}
      </section>

      {r.canComment && <ReplyBox reference={r.reference} onSent={refresh} />}
      {r.canCancel && <WithdrawBox reference={r.reference} onDone={refresh} />}
    </div>
  );
}

/**
 * The end of the road for an anonymous report.
 *
 * This screen exists because the reference is about to stop being useful and
 * the resident does not know that yet. Everywhere else in the portal a
 * reference is a key; here it is a courtesy — there is no contact detail to
 * verify a later lookup against, so nobody can open this report again, us
 * included.
 *
 * Saying so plainly at the one moment it can still be acted on is the honest
 * design. Burying it would be worse than not offering the path at all: the
 * resident would come back in a week, fail to find their report, and conclude
 * the City lost it.
 */
function AnonymousSubmitted() {
  const { reference = "" } = useParams();

  return (
    <div className="space-y-4">
      <div
        className="rounded-md px-4 py-3 text-sm"
        style={{ background: "var(--status-good-bg)", color: "var(--status-good)" }}
      >
        Thank you — we have your report and it is on its way to the right team.
      </div>

      <div className="cc-card p-5">
        <p className="text-sm text-ink-muted">Your reference number</p>
        <p className="mt-1 font-mono text-2xl font-semibold">{reference}</p>

        <div className="mt-5 rounded-md p-4" style={{ background: "var(--surface-0)" }}>
          <p className="font-medium">This report cannot be checked on later</p>
          <p className="mt-1 text-sm text-ink-muted">
            You reported without giving us any contact details, so there is nothing for us to
            check a later enquiry against — the reference on its own is not enough, and we have
            no way to send you updates. Quote it if you contact the City about this report.
          </p>
        </div>

        <div className="mt-5 flex flex-wrap gap-3">
          <Link className="cc-btn cc-btn-primary" to="/">
            Report something else
          </Link>
          <a className="cc-btn" href={portalApi.loginUrl("/")}>
            Sign in for next time
          </a>
        </div>
      </div>

      <p className="text-sm text-ink-muted">
        Reporting with an account, or with an email address, means we can confirm we have it,
        tell you when it is done, and let you{" "}
        <Link className="underline underline-offset-2" to="/track">
          check on it whenever you like
        </Link>
        .
      </p>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Track — the "where is my report" loop, for people with no account
// ---------------------------------------------------------------------------

/**
 * Tracking by reference and contact detail.
 *
 * This is the path most people take. Someone reports a pothole once, never
 * makes an account, and comes back a week later wanting to know what happened —
 * and if the only answer is "sign in", they phone the service centre instead.
 *
 * Two deliberate choices here. The limits are stated up front rather than
 * discovered through a failed lookup: a report filed anonymously cannot be
 * tracked at all, and saying so before someone types is kinder than a
 * not-found afterwards. And the failure message is identical whatever went
 * wrong, because the server cannot distinguish "no such reference" from
 * "wrong email" without telling a stranger which references are real.
 */
function Track() {
  const [reference, setReference] = useState("");
  const [verification, setVerification] = useState("");

  const lookup = useMutation({
    mutationFn: () => portalApi.track(reference.trim(), verification.trim()),
  });

  const found = lookup.data;

  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-2xl font-semibold">Check on a report</h1>
        <p className="mt-1 text-ink-muted">
          Enter the reference number from your confirmation message, and the email address or
          phone number you gave when you reported it.
        </p>
      </div>

      <form
        className="cc-card space-y-4 p-5"
        onSubmit={(e) => {
          e.preventDefault();
          lookup.mutate();
        }}
      >
        <Field label="Reference number" hint="On your confirmation message, for example SR-7K4M-2QX9">
          <Input
            value={reference}
            onChange={(e) => setReference(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            required
          />
        </Field>

        <Field label="Email or phone number" hint="The one you gave when you made the report">
          <Input
            value={verification}
            onChange={(e) => setVerification(e.target.value)}
            autoComplete="off"
            required
          />
        </Field>

        {lookup.error && (
          <p role="alert" className="text-sm" style={{ color: "var(--status-bad)" }}>
            {(lookup.error as Error).message}
          </p>
        )}

        <Button type="submit" variant="primary" disabled={lookup.isPending}>
          {lookup.isPending ? "Looking…" : "Check my report"}
        </Button>
      </form>

      <p className="text-sm text-ink-muted">
        Reports made without giving your details cannot be checked here — there is nothing to
        match them against. If you have an account,{" "}
        <Link className="underline underline-offset-2" to="/requests">
          all of your reports are listed here
        </Link>
        .
      </p>

      {found && (
        <div className="cc-card p-5">
          <div className="flex flex-wrap items-center gap-2">
            <StatusPill request={found} />
            <span className="font-mono text-xs text-ink-muted">{found.reference}</span>
          </div>
          <h2 className="mt-2 text-xl font-semibold">{found.subject}</h2>
          <p className="mt-1 text-sm text-ink-muted">
            {found.serviceType}
            {found.department ? ` · handled by ${found.department}` : ""}
          </p>

          <dl className="mt-4 grid gap-3 text-sm sm:grid-cols-2">
            <div>
              <dt className="text-xs uppercase tracking-wide text-ink-muted">Reported</dt>
              <dd>{formatDate(found.openedAt)}</dd>
            </div>
            {found.address && (
              <div>
                <dt className="text-xs uppercase tracking-wide text-ink-muted">Location</dt>
                <dd>{found.address}</dd>
              </div>
            )}
            {found.expectedBy && (
              <div>
                <dt className="text-xs uppercase tracking-wide text-ink-muted">Expected by</dt>
                <dd>{formatDate(found.expectedBy)}</dd>
              </div>
            )}
            {found.resolvedAt && (
              <div>
                <dt className="text-xs uppercase tracking-wide text-ink-muted">Completed</dt>
                <dd>{formatDate(found.resolvedAt)}</dd>
              </div>
            )}
          </dl>

          {found.resolution && (
            <div className="mt-4 rounded-md p-3 text-sm" style={{ background: "var(--surface-0)" }}>
              <p className="font-medium">What we did</p>
              <p className="mt-1 whitespace-pre-wrap">{found.resolution}</p>
            </div>
          )}

          <h3 className="mt-5 text-sm font-semibold">Progress</h3>
          {(found.updates?.length ?? 0) === 0 ? (
            <p className="mt-2 text-sm text-ink-muted">
              Nothing to report yet. We will be in touch.
            </p>
          ) : (
            <ol className="mt-3 space-y-3">
              {found.updates!.map((u, i) => (
                <li
                  key={i}
                  className="rounded-md p-3 text-sm"
                  style={{ background: "var(--surface-2)" }}
                >
                  <div className="mb-1 flex items-center gap-2 text-xs text-ink-muted">
                    <span className="font-medium text-ink">{u.author}</span>
                    <span>{formatDate(u.at)}</span>
                  </div>
                  <p className="whitespace-pre-wrap">{u.body}</p>
                </li>
              ))}
            </ol>
          )}
        </div>
      )}
    </div>
  );
}

function ReplyBox({ reference, onSent }: { reference: string; onSent: () => void }) {
  const [text, setText] = useState("");
  const send = useMutation({
    mutationFn: () => portalApi.comment(reference, text),
    onSuccess: () => { setText(""); onSent(); },
  });

  return (
    <section className="cc-card p-5">
      <h2 className="text-sm font-semibold">Add something</h2>
      <p className="mt-1 text-sm text-ink-muted">
        If the problem has changed, or you have more detail, tell us here.
      </p>
      {send.error ? <div className="mt-3"><ErrorNote error={send.error} /></div> : null}
      <Textarea
        className="mt-3"
        rows={3}
        value={text}
        onChange={(e) => setText(e.target.value)}
        placeholder="Your message to the City…"
      />
      <div className="mt-3 flex justify-end">
        <Button variant="primary" disabled={!text.trim() || send.isPending} onClick={() => send.mutate()}>
          {send.isPending ? "Sending…" : "Send"}
        </Button>
      </div>
    </section>
  );
}

function WithdrawBox({ reference, onDone }: { reference: string; onDone: () => void }) {
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState("");
  const cancel = useMutation({
    mutationFn: () => portalApi.cancel(reference, reason),
    onSuccess: onDone,
  });

  if (!open) {
    return (
      <div className="text-center">
        <button className="text-sm text-ink-muted underline underline-offset-2" onClick={() => setOpen(true)}>
          Withdraw this report
        </button>
      </div>
    );
  }

  return (
    <section className="cc-card p-5">
      <h2 className="text-sm font-semibold">Withdraw this report</h2>
      <p className="mt-1 text-sm text-ink-muted">
        Only do this if it is no longer needed — if it has been fixed already, for example.
      </p>
      {cancel.error ? <div className="mt-3"><ErrorNote error={cancel.error} /></div> : null}
      <Input
        className="mt-3"
        value={reason}
        onChange={(e) => setReason(e.target.value)}
        placeholder="Why are you withdrawing it? (optional)"
      />
      <div className="mt-3 flex justify-end gap-2">
        <Button onClick={() => setOpen(false)}>Keep it open</Button>
        <Button variant="danger" disabled={cancel.isPending} onClick={() => cancel.mutate()}>
          {cancel.isPending ? "Withdrawing…" : "Withdraw"}
        </Button>
      </div>
    </section>
  );
}

/** RatingCard records satisfaction — the answer half of the CSAT survey. */
function RatingCard({ reference, onDone }: { reference: string; onDone: () => void }) {
  const [score, setScore] = useState(0);
  const [comment, setComment] = useState("");
  const rate = useMutation({
    mutationFn: () => portalApi.rate(reference, score, comment),
    onSuccess: onDone,
  });

  return (
    <section className="cc-card p-5">
      <h2 className="text-sm font-semibold">How did we do?</h2>
      <p className="mt-1 text-sm text-ink-muted">
        Your answer helps the City see which services are working.
      </p>
      {rate.error ? <div className="mt-3"><ErrorNote error={rate.error} /></div> : null}

      <div className="mt-3 flex gap-2" role="radiogroup" aria-label="Rating out of five">
        {[1, 2, 3, 4, 5].map((n) => (
          <button
            key={n}
            role="radio"
            aria-checked={score === n}
            aria-label={`${n} out of 5`}
            onClick={() => setScore(n)}
            className="h-11 w-11 rounded-md border text-lg font-medium"
            style={{
              borderColor: score === n ? "var(--accent)" : "var(--border-strong)",
              background: score === n ? "var(--accent)" : "var(--surface-2)",
              color: score === n ? "#fff" : "var(--text-primary)",
            }}
          >
            {n}
          </button>
        ))}
      </div>
      <p className="mt-1 text-xs text-ink-faint">1 is poor, 5 is excellent.</p>

      <Textarea
        className="mt-3"
        rows={2}
        value={comment}
        onChange={(e) => setComment(e.target.value)}
        placeholder="Anything you would like to add? (optional)"
      />
      <div className="mt-3 flex justify-end">
        <Button variant="primary" disabled={score === 0 || rate.isPending} onClick={() => rate.mutate()}>
          {rate.isPending ? "Sending…" : "Submit"}
        </Button>
      </div>
    </section>
  );
}
