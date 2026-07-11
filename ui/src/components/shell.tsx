// AppShell: primary nav is the mental model (screen-catalog §1) — only the
// sections this slice can honestly serve appear; DescentRail is the
// cross-cutting §1.8 breadcrumb, not a nav entry.
import { Link, useRouterState } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";
import { login, logout, oidcConfigured, principalLabel, setDevPrincipal, devPrincipal } from "../auth/oidc";
import { Button } from "./ui";

const NAV = [
  { to: "/views", label: "Graph" },
  { to: "/runs", label: "Runs" },
  { to: "/workflows", label: "Workflows" },
  { to: "/gates", label: "Gates" },
  { to: "/triggers", label: "Triggers" },
];

function themeNow(): string {
  return document.documentElement.dataset.theme ?? "";
}

export function AppShell({ children }: { children: ReactNode }) {
  const path = useRouterState({ select: (s) => s.location.pathname });
  const [, force] = useState(0);
  const who = principalLabel();
  return (
    <div className="flex h-full">
      <nav
        className="flex w-[200px] shrink-0 flex-col gap-[var(--space-1)] border-r p-[var(--space-4)]"
        style={{ background: "var(--color-surface)", borderColor: "var(--color-border)" }}
      >
        <div className="mb-[var(--space-4)] text-[19px] font-semibold tracking-tight">stratt</div>
        {NAV.map((n) => {
          const active = path.startsWith(n.to);
          return (
            <Link
              key={n.to}
              to={n.to}
              className="rounded-[var(--radius-md)] px-[var(--space-3)] py-[var(--space-2)] text-[13px]"
              style={{
                background: active ? "var(--color-surface-sunken)" : "transparent",
                color: active ? "var(--text-primary)" : "var(--text-secondary)",
                textDecoration: "none",
              }}
            >
              {n.label}
            </Link>
          );
        })}
        <div className="mt-auto flex flex-col gap-[var(--space-2)] text-[12px]" style={{ color: "var(--text-muted)" }}>
          <button
            type="button"
            className="cursor-pointer self-start rounded-[var(--radius-md)] border px-[var(--space-2)] py-[2px]"
            style={{ borderColor: "var(--color-border)", color: "var(--text-secondary)", background: "transparent" }}
            onClick={() => {
              const next = themeNow() === "dark" ? "light" : "dark";
              document.documentElement.dataset.theme = next;
              force((n) => n + 1);
            }}
          >
            theme: {themeNow() || "auto"}
          </button>
          {who ? (
            <>
              <span data-testid="principal" className="break-all" style={{ color: "var(--text-secondary)" }}>
                {who}
              </span>
              <Button kind="quiet" onClick={() => (oidcConfigured ? logout() : (setDevPrincipal(""), location.reload()))}>
                sign out
              </Button>
            </>
          ) : oidcConfigured ? (
            <Button onClick={() => login(path)}>sign in</Button>
          ) : (
            <DevPrincipalField />
          )}
        </div>
      </nav>
      <main className="min-w-0 flex-1 overflow-auto p-[var(--space-5)]">{children}</main>
    </div>
  );
}

// Substrate-less fallback mirroring the server's gated dev-header mode.
function DevPrincipalField() {
  const [v, setV] = useState(devPrincipal());
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        setDevPrincipal(v);
        location.reload();
      }}
      className="flex flex-col gap-[var(--space-1)]"
    >
      <label htmlFor="devp">dev principal</label>
      <input
        id="devp"
        value={v}
        onChange={(e) => setV(e.target.value)}
        className="rounded-[var(--radius-sm)] border px-[var(--space-2)] py-[2px]"
        style={{ background: "var(--color-surface-sunken)", borderColor: "var(--color-border)", color: "var(--text-primary)" }}
      />
    </form>
  );
}

// DescentRail: the §1.8 ladder as a breadcrumb — every rung a link (L4/L10).
export function DescentRail({ rungs }: { rungs: { label: string; to?: string }[] }) {
  return (
    <nav className="mb-[var(--space-4)] flex flex-wrap items-center gap-[var(--space-2)] text-[12px]" aria-label="descent">
      {rungs.map((r, i) => (
        <span key={i} className="flex items-center gap-[var(--space-2)]">
          {i > 0 && <span style={{ color: "var(--text-muted)" }}>→</span>}
          {r.to ? (
            <Link to={r.to} style={{ color: "var(--color-accent)" }}>
              {r.label}
            </Link>
          ) : (
            <span style={{ color: "var(--text-secondary)" }}>{r.label}</span>
          )}
        </span>
      ))}
    </nav>
  );
}
