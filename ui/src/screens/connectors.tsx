import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { sourcesQuery, emittersQuery } from "@/lib/data";
import { TableShell, Tabs } from "@/components/table";
import { Badge } from "@/components/ui/badge";
import { StateChip } from "@/components/state-chip";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorLine, EmptyState } from "@/components/feedback";
import type { Schema } from "@/api/client";

// Connectors — the tools projecting into / acting on the estate. Sources (systems of record) and
// Emitters (event ingress). Read-only: the external systems stay authoritative (§1.2).
const TABS = ["sources", "emitters"] as const;

export function ConnectorsPage() {
  const [tab, setTab] = useState<(typeof TABS)[number]>("sources");
  return (
    <div className="p-6">
      <h1 className="mb-3 text-lg font-semibold">Connectors</h1>
      <Tabs tabs={TABS} value={tab} onChange={setTab} />
      {tab === "sources" ? <SourcesTab /> : <EmittersTab />}
    </div>
  );
}

function SourcesTab() {
  const { data, isPending, error } = useQuery(sourcesQuery());
  const sources = data ?? [];
  if (isPending) return <Skeleton className="h-40 w-full" />;
  if (error) return <ErrorLine error={error} />;
  if (sources.length === 0) return <EmptyState label="No Sources registered." />;
  return (
    <TableShell head={["Source", "Kind", "Endpoint", "Cell", "Home"]}>
      {sources.map((s: Schema["Source"]) => (
        <tr key={s.name} className="border-t border-border">
          <td className="px-3 py-2 font-medium">{s.name}</td>
          <td className="px-3 py-2">
            <Badge variant="outline">{s.kind}</Badge>
          </td>
          <td className="px-3 py-2 font-mono text-xs text-subtle-foreground">{s.endpoint}</td>
          <td className="px-3 py-2 text-subtle-foreground">{s.cell ?? "—"}</td>
          <td className="px-3 py-2">
            {s.status && <StateChip state={homeState(s.status)} label={s.status} />}
            {s.rehomingTo && (
              <span className="ml-2 text-xs text-muted-foreground">→ {s.rehomingTo}</span>
            )}
          </td>
        </tr>
      ))}
    </TableShell>
  );
}

function EmittersTab() {
  const { data, isPending, error } = useQuery(emittersQuery());
  const emitters = data ?? [];
  if (isPending) return <Skeleton className="h-40 w-full" />;
  if (error) return <ErrorLine error={error} />;
  if (emitters.length === 0) return <EmptyState label="No Emitters configured." />;
  return (
    <TableShell head={["Emitter", "Kind", "Token"]}>
      {emitters.map((e: Schema["Emitter"]) => (
        <tr key={e.name} className="border-t border-border">
          <td className="px-3 py-2 font-medium">{e.name}</td>
          <td className="px-3 py-2">
            <Badge variant="outline">{e.kind}</Badge>
          </td>
          <td className="px-3 py-2 font-mono text-xs text-muted-foreground">
            {e.tokenHash.slice(0, 16)}…
          </td>
        </tr>
      ))}
    </TableShell>
  );
}

function homeState(status: string): Parameters<typeof StateChip>[0]["state"] {
  if (status.includes("active")) return "ok";
  if (status.includes("standby") || status.includes("sealed")) return "attention";
  if (status.includes("degraded") || status.includes("uncertain")) return "degraded";
  return "pending";
}
