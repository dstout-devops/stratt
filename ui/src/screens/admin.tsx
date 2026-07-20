import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { auditQuery, usageQuery, contractsQuery } from "@/lib/data";
import { TableShell, Tabs } from "@/components/table";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorLine, EmptyState } from "@/components/feedback";
import { relTime } from "@/lib/format";
import type { Schema } from "@/api/client";

// Admin — the one audit stream (§1.6: UI/CLI/CI/agents all land here), per-identity usage/cost, and
// the Contract Registry (every pinned schema — the schema-driven rendering source, §2.2/§1.5).
const TABS = ["audit", "usage", "contracts"] as const;

export function AdminPage() {
  const [tab, setTab] = useState<(typeof TABS)[number]>("audit");
  return (
    <div className="p-6">
      <h1 className="mb-3 text-lg font-semibold">Admin</h1>
      <Tabs tabs={TABS} value={tab} onChange={setTab} />
      {tab === "audit" && <AuditTab />}
      {tab === "usage" && <UsageTab />}
      {tab === "contracts" && <ContractsTab />}
    </div>
  );
}

function AuditTab() {
  const { data, isPending, error } = useQuery(auditQuery());
  const events = data ?? [];
  if (isPending) return <Skeleton className="h-40 w-full" />;
  if (error) return <ErrorLine error={error} />;
  if (events.length === 0) return <EmptyState label="No audit events." />;
  return (
    <TableShell head={["Seq", "When", "Principal", "Action", "Object"]}>
      {events.map((e: Schema["AuditEvent"]) => (
        <tr key={e.seq} className="border-t border-border">
          <td className="px-3 py-2 tabular text-muted-foreground">{e.seq}</td>
          <td className="px-3 py-2 tabular text-muted-foreground">{relTime(e.at)}</td>
          <td className="px-3 py-2 text-subtle-foreground">
            {e.principalId ?? "anon"}
            {e.principalKind && (
              <span className="ml-1 text-xs text-muted-foreground">({e.principalKind})</span>
            )}
          </td>
          <td className="px-3 py-2 font-mono text-xs">{e.action}</td>
          <td className="px-3 py-2 font-mono text-xs text-subtle-foreground">{e.object ?? "—"}</td>
        </tr>
      ))}
    </TableShell>
  );
}

function UsageTab() {
  const { data, isPending, error } = useQuery(usageQuery());
  const rows = data ?? [];
  if (isPending) return <Skeleton className="h-40 w-full" />;
  if (error) return <ErrorLine error={error} />;
  if (rows.length === 0) return <EmptyState label="No usage recorded." />;
  return (
    <TableShell head={["Principal", "Tool", "Calls", "Errors", "Last call"]}>
      {rows.map((u: Schema["UsageEntry"], i: number) => (
        <tr key={`${u.principal}-${u.tool}-${i}`} className="border-t border-border">
          <td className="px-3 py-2 text-subtle-foreground">{u.principal}</td>
          <td className="px-3 py-2 font-mono text-xs">{u.tool}</td>
          <td className="px-3 py-2 tabular">{u.calls}</td>
          <td
            className={`px-3 py-2 tabular ${u.errors > 0 ? "text-state-failed" : "text-muted-foreground"}`}
          >
            {u.errors}
          </td>
          <td className="px-3 py-2 tabular text-muted-foreground">{relTime(u.lastCall)}</td>
        </tr>
      ))}
    </TableShell>
  );
}

function ContractsTab() {
  const { data, isPending, error } = useQuery(contractsQuery());
  const contracts = data ?? [];
  if (isPending) return <Skeleton className="h-40 w-full" />;
  if (error) return <ErrorLine error={error} />;
  if (contracts.length === 0) return <EmptyState label="No Contracts registered." />;
  return (
    <TableShell head={["Contract", "Version", "Rung", "Hash"]}>
      {contracts.map((c: Schema["Contract"]) => (
        <tr key={`${c.name}@${c.version}`} className="border-t border-border">
          <td className="px-3 py-2 font-mono text-xs">{c.name}</td>
          <td className="px-3 py-2 tabular">v{c.version}</td>
          <td className="px-3 py-2">
            <Badge variant="outline">{c.rung}</Badge>
          </td>
          <td className="px-3 py-2 font-mono text-xs text-muted-foreground">
            {c.hash.slice(0, 16)}…
          </td>
        </tr>
      ))}
    </TableShell>
  );
}
