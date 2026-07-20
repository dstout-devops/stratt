import { useQuery } from "@tanstack/react-query";
import { sitesQuery } from "@/lib/data";
import { TableShell } from "@/components/table";
import { Badge } from "@/components/ui/badge";
import { StateChip } from "@/components/state-chip";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorLine, EmptyState } from "@/components/feedback";
import type { Schema } from "@/api/client";

// Fleet — the remote execution loci (Sites: NATS-leaf satellites). Live up/down is ephemeral
// (NATS KV), surfaced on the read.
export function FleetPage() {
  const { data, isPending, error } = useQuery(sitesQuery());
  const sites = data ?? [];
  return (
    <div className="p-6">
      <h1 className="mb-4 text-lg font-semibold">Fleet</h1>
      {error && <ErrorLine error={error} />}
      {isPending ? (
        <Skeleton className="h-40 w-full" />
      ) : sites.length === 0 ? (
        <EmptyState label="No Sites registered — execution runs on the central cluster." />
      ) : (
        <TableShell head={["Site", "Mode", "Namespace", "Status", "Declared"]}>
          {sites.map((s: Schema["Site"]) => (
            <tr key={s.name} className="border-t border-border">
              <td className="px-3 py-2 font-medium">{s.name}</td>
              <td className="px-3 py-2">
                <Badge variant="outline">{s.mode}</Badge>
              </td>
              <td className="px-3 py-2 text-subtle-foreground">{s.namespace ?? "—"}</td>
              <td className="px-3 py-2">
                <StateChip state={s.live ? "ok" : "failed"} label={s.live ? "up" : "down"} />
              </td>
              <td className="px-3 py-2 text-xs text-muted-foreground">{s.declaredBy ?? "—"}</td>
            </tr>
          ))}
        </TableShell>
      )}
    </div>
  );
}
