// Shared feedback primitives, kept OUT of any route module so route chunks don't couple through
// them (clean code-splitting — ADR-0090 §2 route lazy-loading). Error text is surfaced verbatim
// (§1.8 — never swallow the diagnosis).

export function ErrorLine({ error }: { error: unknown }) {
  return (
    <div className="mb-3 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
      {error instanceof Error ? error.message : "Request failed"}
    </div>
  );
}

export function EmptyState({ label }: { label: string }) {
  return (
    <div className="rounded-lg border border-dashed border-border p-8 text-center text-sm text-muted-foreground">
      {label}
    </div>
  );
}
