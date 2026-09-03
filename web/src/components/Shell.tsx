import { useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { NavLink, useNavigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";

import { api } from "@/lib/api";
import { useAuth } from "@/hooks/useAuth";
import { Badge, Button, cx } from "./ui";

const NAV = [
  { to: "/", label: "Dashboard", exact: true, permission: null },
  { to: "/requests", label: "Requests", permission: "request:read" },
  { to: "/contacts", label: "Contacts", permission: "contact:read" },
  { to: "/reports", label: "Reports", permission: "report:read" },
  { to: "/admin", label: "Admin", permission: "config:read" },
] as const;

export function Shell({ children }: { children: ReactNode }) {
  const { me, can, signOut } = useAuth();
  const [menuOpen, setMenuOpen] = useState(false);

  return (
    <div className="flex min-h-screen flex-col">
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:absolute focus:left-2 focus:top-2 focus:z-50 focus:rounded focus:bg-[var(--surface-2)] focus:px-3 focus:py-2"
      >
        Skip to content
      </a>

      <header
        className="sticky top-0 z-30 border-b"
        style={{ background: "var(--surface-1)", borderColor: "var(--border)" }}
      >
        <div className="mx-auto flex max-w-[1600px] items-center gap-4 px-4 py-2.5">
          <NavLink to="/" className="flex shrink-0 items-center gap-2 font-semibold">
            <span
              className="grid h-7 w-7 place-items-center rounded text-xs font-bold text-white"
              style={{ background: "var(--accent)" }}
              aria-hidden
            >
              CC
            </span>
            <span className="hidden sm:inline">CityConnect</span>
          </NavLink>

          <nav className="flex items-center gap-1" aria-label="Main">
            {NAV.filter((item) => !item.permission || can(item.permission)).map((item) => (
              <NavLink
                key={item.to}
                to={item.to}
                end={"exact" in item ? item.exact : false}
                className={({ isActive }) =>
                  cx(
                    "rounded-md px-3 py-1.5 text-sm font-medium transition-colors",
                    isActive ? "text-ink" : "text-ink-muted hover:text-ink",
                  )
                }
                style={({ isActive }) => (isActive ? { background: "var(--surface-0)" } : undefined)}
              >
                {item.label}
              </NavLink>
            ))}
          </nav>

          <div className="ml-auto flex items-center gap-3">
            <GlobalSearch />
            <ThemeToggle />

            <div className="relative">
              <button
                onClick={() => setMenuOpen((v) => !v)}
                className="flex items-center gap-2 rounded-md px-2 py-1.5 text-sm hover:bg-[var(--surface-0)]"
                aria-haspopup="menu"
                aria-expanded={menuOpen}
              >
                <span
                  className="grid h-7 w-7 place-items-center rounded-full text-xs font-semibold"
                  style={{ background: "var(--surface-0)", color: "var(--text-secondary)" }}
                  aria-hidden
                >
                  {initials(me?.user?.name || me?.user?.email || "?")}
                </span>
                <span className="hidden text-left md:block">
                  <span className="block leading-tight">{me?.user?.name || me?.user?.email}</span>
                  <span className="block text-xs leading-tight text-ink-muted">
                    {me?.department?.name ?? roleLabel(me?.user?.role)}
                  </span>
                </span>
              </button>

              {menuOpen && (
                <div
                  role="menu"
                  className="cc-card absolute right-0 mt-1 w-64 p-2 shadow-lg"
                  onMouseLeave={() => setMenuOpen(false)}
                >
                  <div className="border-b px-2 pb-2" style={{ borderColor: "var(--border)" }}>
                    <p className="text-sm font-medium">{me?.user?.name}</p>
                    <p className="text-xs text-ink-muted">{me?.user?.email}</p>
                    <div className="mt-1.5 flex flex-wrap gap-1">
                      <Badge tone="accent">{roleLabel(me?.user?.role)}</Badge>
                      {me?.crossDepartment && <Badge>All departments</Badge>}
                    </div>
                  </div>
                  <p className="px-2 py-2 text-xs text-ink-faint">
                    Signed in through C2 single sign-on. Signing out here also ends your C2 session.
                  </p>
                  <Button className="w-full" onClick={() => void signOut()}>
                    Sign out
                  </Button>
                </div>
              )}
            </div>
          </div>
        </div>
      </header>

      <main id="main" className="mx-auto w-full max-w-[1600px] flex-1 px-4 py-5">
        {children}
      </main>

      <footer
        className="border-t px-4 py-3 text-center text-xs text-ink-faint"
        style={{ borderColor: "var(--border)" }}
      >
        CityConnect · service request management
      </footer>
    </div>
  );
}

function initials(name: string) {
  return name
    .split(/[\s@.]+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((p) => p[0]?.toUpperCase())
    .join("");
}

function roleLabel(role?: string) {
  switch (role) {
    case "admin": return "Administrator";
    case "supervisor": return "Supervisor";
    case "agent": return "Agent";
    case "readonly": return "Read only";
  }
  return "Staff";
}

/**
 * The omnibox. A reference typed in full jumps straight to that request —
 * an agent on a call with a citizen reading out "SR-7K4M-2QX9" wants the
 * record, not a ranked list.
 */
function GlobalSearch() {
  const [term, setTerm] = useState("");
  const [open, setOpen] = useState(false);
  const navigate = useNavigate();
  const boxRef = useRef<HTMLDivElement>(null);

  const { data } = useQuery({
    queryKey: ["search", term],
    queryFn: () => api.search(term),
    enabled: term.trim().length >= 2,
    staleTime: 10_000,
  });

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === "k") {
        e.preventDefault();
        boxRef.current?.querySelector("input")?.focus();
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);

  const results = data?.items ?? [];

  return (
    <div ref={boxRef} className="relative hidden lg:block">
      <input
        value={term}
        onChange={(e) => {
          setTerm(e.target.value);
          setOpen(true);
        }}
        onFocus={() => setOpen(true)}
        onBlur={() => window.setTimeout(() => setOpen(false), 150)}
        placeholder="Search requests and contacts   ⌘K"
        aria-label="Search"
        className="cc-input w-72 py-1.5"
      />

      {open && term.trim().length >= 2 && (
        <div className="cc-card absolute right-0 mt-1 max-h-96 w-96 overflow-y-auto p-1 shadow-lg">
          {results.length === 0 ? (
            <p className="px-3 py-4 text-sm text-ink-muted">No matches.</p>
          ) : (
            results.map((r) => (
              <button
                key={`${r.type}-${r.id}`}
                className="block w-full rounded px-3 py-2 text-left hover:bg-[var(--surface-0)]"
                onMouseDown={() => {
                  navigate(r.type === "request" ? `/requests/${r.id}` : `/contacts/${r.id}`);
                  setTerm("");
                  setOpen(false);
                }}
              >
                <span className="flex items-center gap-2 text-sm">
                  <Badge>{r.type === "request" ? "Request" : "Contact"}</Badge>
                  <span className="truncate font-medium">{r.reference ?? r.title}</span>
                </span>
                <span className="mt-0.5 block truncate text-xs text-ink-muted">
                  {r.reference ? r.title : r.subtitle}
                </span>
              </button>
            ))
          )}
        </div>
      )}
    </div>
  );
}

/**
 * Three-state theme control.
 *
 * Light is the default: the console is a workplace tool used all day beside
 * printed forms and other municipal systems, and it should look the same on
 * every desk rather than depending on how each machine happens to be set up.
 *
 * "System" is still offered and still follows the operating system — it is
 * simply no longer what a new install starts on. An explicit choice stamps
 * data-theme and wins in both directions; "system" stamps nothing.
 *
 * The same default is applied in index.html before first paint, so a machine
 * set to dark does not flash dark on the way in. Both places must agree.
 */
function ThemeToggle() {
  const [theme, setTheme] = useState<"system" | "light" | "dark">(
    () => (localStorage.getItem("cc-theme") as "system" | "light" | "dark") ?? "light",
  );

  useEffect(() => {
    const root = document.documentElement;
    if (theme === "system") {
      root.removeAttribute("data-theme");
    } else {
      root.setAttribute("data-theme", theme);
    }
    localStorage.setItem("cc-theme", theme);
  }, [theme]);

  const next = theme === "system" ? "light" : theme === "light" ? "dark" : "system";
  const icon = theme === "system" ? "◐" : theme === "light" ? "☀" : "☾";

  return (
    <button
      onClick={() => setTheme(next)}
      className="rounded-md px-2 py-1.5 text-sm text-ink-muted hover:bg-[var(--surface-0)] hover:text-ink"
      title={`Theme: ${theme}. Click for ${next}.`}
      aria-label={`Theme: ${theme}. Switch to ${next}.`}
    >
      <span aria-hidden>{icon}</span>
    </button>
  );
}
