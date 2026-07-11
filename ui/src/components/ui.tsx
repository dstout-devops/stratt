// Vendored, token-driven primitives (charter §3: components owned in-repo;
// design-tokens.md: semantic tokens only). Small on purpose.
import { useEffect, useRef, type ReactNode } from "react";

export function Card({ title, children }: { title?: ReactNode; children: ReactNode }) {
  return (
    <section
      className="rounded-[var(--radius-lg)] border p-[var(--space-4)]"
      style={{ background: "var(--color-surface)", borderColor: "var(--color-border)" }}
    >
      {title && (
        <h2 className="mb-[var(--space-3)] text-[16px] font-semibold" style={{ color: "var(--text-primary)" }}>
          {title}
        </h2>
      )}
      {children}
    </section>
  );
}

export function Button({
  children,
  onClick,
  kind = "primary",
  disabled,
}: {
  children: ReactNode;
  onClick?: () => void;
  kind?: "primary" | "quiet" | "danger";
  disabled?: boolean;
}) {
  const bg =
    kind === "primary" ? "var(--color-accent)" : kind === "danger" ? "var(--state-failed)" : "transparent";
  const fg = kind === "quiet" ? "var(--text-primary)" : "var(--text-on-accent)";
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      className="cursor-pointer rounded-[var(--radius-md)] border px-[var(--space-3)] py-[var(--space-1)] text-[13px] font-medium disabled:cursor-default disabled:opacity-50"
      style={{
        background: bg,
        color: fg,
        borderColor: kind === "quiet" ? "var(--color-border)" : "transparent",
      }}
    >
      {children}
    </button>
  );
}

// StateChip: one state palette for Run/Step/task-event lifecycle — always
// dot + icon + label; color never carries meaning alone (design-tokens §5.1).
const STATES: Record<string, { token: string; icon: string }> = {
  pending: { token: "var(--state-pending)", icon: "◌" },
  running: { token: "var(--state-running)", icon: "▶" },
  succeeded: { token: "var(--state-ok)", icon: "✓" },
  approved: { token: "var(--state-ok)", icon: "✓" },
  changed: { token: "var(--state-attention)", icon: "~" },
  skipped: { token: "var(--state-pending)", icon: "⤼" },
  expired: { token: "var(--state-degraded)", icon: "⏱" },
  denied: { token: "var(--state-failed)", icon: "⨯" },
  failed: { token: "var(--state-failed)", icon: "✗" },
  canceled: { token: "var(--state-pending)", icon: "⊘" },
};

export function StateChip({ state }: { state: string }) {
  const s = STATES[state] ?? STATES.pending;
  return (
    <span
      className="inline-flex items-center gap-[var(--space-1)] rounded-[var(--radius-full)] border px-[var(--space-2)] py-[1px] text-[12px]"
      style={{ color: s.token, borderColor: "var(--color-border)" }}
      data-state={state}
    >
      <span aria-hidden style={{ fontSize: "10px" }}>
        ●
      </span>
      <span aria-hidden>{s.icon}</span>
      <span>{state}</span>
    </span>
  );
}

export function DataTable({ head, rows }: { head: ReactNode[]; rows: ReactNode[][] }) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full border-collapse text-[13px]">
        <thead>
          <tr>
            {head.map((h, i) => (
              <th
                key={i}
                className="border-b px-[var(--space-3)] py-[var(--space-2)] text-left font-medium"
                style={{ color: "var(--text-secondary)", borderColor: "var(--rule)" }}
              >
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((r, i) => (
            <tr key={i}>
              {r.map((c, j) => (
                <td
                  key={j}
                  className="border-b px-[var(--space-3)] py-[var(--space-2)] align-top"
                  style={{ borderColor: "var(--color-border)" }}
                >
                  {c}
                </td>
              ))}
            </tr>
          ))}
          {rows.length === 0 && (
            <tr>
              <td className="px-[var(--space-3)] py-[var(--space-4)]" style={{ color: "var(--text-muted)" }} colSpan={head.length}>
                nothing here yet
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}

// Dialog: native <dialog> — boring beats a primitive dependency for one
// modal (escape/backdrop handled by the platform).
export function Dialog({
  open,
  onClose,
  title,
  children,
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    if (open && !d.open) d.showModal();
    if (!open && d.open) d.close();
  }, [open]);
  return (
    <dialog
      ref={ref}
      onClose={onClose}
      className="m-auto w-[420px] rounded-[var(--radius-lg)] border p-[var(--space-5)] backdrop:bg-[var(--color-scrim)]"
      style={{
        background: "var(--color-surface)",
        color: "var(--text-primary)",
        borderColor: "var(--color-border)",
        boxShadow: "var(--shadow-2)",
      }}
    >
      <h2 className="mb-[var(--space-3)] text-[16px] font-semibold">{title}</h2>
      {children}
    </dialog>
  );
}

export function KV({ items }: { items: [string, ReactNode][] }) {
  return (
    <dl className="grid grid-cols-[max-content_1fr] gap-x-[var(--space-4)] gap-y-[var(--space-1)] text-[13px]">
      {items.map(([k, v]) => (
        <div key={k} className="contents">
          <dt style={{ color: "var(--text-secondary)" }}>{k}</dt>
          <dd className="m-0" style={{ color: "var(--text-primary)" }}>
            {v}
          </dd>
        </div>
      ))}
    </dl>
  );
}
