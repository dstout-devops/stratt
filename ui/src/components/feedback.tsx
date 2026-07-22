// Shared feedback primitives, kept OUT of any route module so route chunks don't couple through
// them (clean code-splitting — ADR-0090 §2 route lazy-loading). Error text is surfaced verbatim
// (§1.8 — never swallow the diagnosis). ErrorLine / EmptyState / ListSkeleton are the loading→
// empty→error triad every list screen renders — shared so a list state looks the same everywhere.
import { Skeleton } from "@/components/ui/skeleton";

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

/** ListSkeleton — the shared loading placeholder for a list/table screen: N ghost rows. */
export function ListSkeleton({ rows = 8 }: { rows?: number }) {
  return (
    <div className="space-y-2">
      {Array.from({ length: rows }).map((_, i) => (
        <Skeleton key={i} className="h-9 w-full" />
      ))}
    </div>
  );
}
