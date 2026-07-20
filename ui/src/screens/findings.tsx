import { Link, useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { ExternalLink } from "lucide-react";
import { findingsQuery, findingQuery, findingEvidenceQuery } from "@/lib/data";
import { useHoverPrefetch } from "@/lib/prefetch";
import { StateChip } from "@/components/state-chip";
import { findingState } from "@/lib/states";
import { Skeleton } from "@/components/ui/skeleton";
import { Badge } from "@/components/ui/badge";
import { relTime } from "@/lib/format";
import { ErrorLine } from "@/components/feedback";
import type { Schema } from "@/api/client";

export function FindingsList() {
  const { data, isPending, error } = useQuery(findingsQuery());
  const findings = data ?? [];
  const roll = rollup(findings);
  return (
    <div className="p-6">
      <div className="mb-4 flex items-center gap-4">
        <h1 className="text-lg font-semibold">Findings</h1>
        <div className="flex gap-3 text-xs">
          <RollChip label="critical" n={roll.critical} state="failed" />
          <RollChip label="warning" n={roll.warning} state="attention" />
          <RollChip label="open" n={roll.open} state="degraded" />
          <RollChip label="resolved" n={roll.resolved} state="ok" />
        </div>
      </div>
      {error && <ErrorLine error={error} />}
      {isPending ? (
        <div className="space-y-2">
          {Array.from({ length: 8 }).map((_, i) => (
            <Skeleton key={i} className="h-9 w-full" />
          ))}
        </div>
      ) : findings.length === 0 ? (
        <div className="rounded-lg border border-dashed border-border p-8 text-center text-sm text-muted-foreground">
          Clean estate — no Findings.
        </div>
      ) : (
        <div className="overflow-hidden rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="bg-card text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-3 py-2 font-medium">Severity</th>
                <th className="px-3 py-2 font-medium">Target</th>
                <th className="px-3 py-2 font-medium">Baseline</th>
                <th className="px-3 py-2 font-medium">Status</th>
                <th className="px-3 py-2 text-right font-medium">Last seen</th>
              </tr>
            </thead>
            <tbody>
              {findings.map((f) => (
                <FindingRow key={f.id} f={f} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function FindingRow({ f }: { f: Schema["Finding"] }) {
  const prefetch = useHoverPrefetch(findingQuery(f.id));
  return (
    <tr className="border-t border-border transition-colors hover:bg-accent/60" {...prefetch}>
      <td className="px-3 py-2">
        <StateChip state={findingState(f.severity)} label={f.severity} />
      </td>
      <td className="px-3 py-2">
        <Link to="/findings/$id" params={{ id: f.id }} className="text-primary hover:underline">
          {f.target}
        </Link>
      </td>
      <td className="px-3 py-2 text-subtle-foreground">{f.baseline}</td>
      <td className="px-3 py-2 text-subtle-foreground">{f.status}</td>
      <td className="px-3 py-2 text-right tabular text-muted-foreground">
        {relTime(f.lastObserved)}
      </td>
    </tr>
  );
}

export function FindingDetail() {
  const { id } = useParams({ from: "/findings/$id" });
  const { data: f, isPending, error } = useQuery(findingQuery(id));
  const evidence = useQuery({ ...findingEvidenceQuery(id), enabled: !!f });
  if (error)
    return (
      <div className="p-6">
        <ErrorLine error={error} />
      </div>
    );
  if (isPending)
    return (
      <div className="p-6">
        <Skeleton className="h-48 w-full" />
      </div>
    );
  return (
    <div className="p-6">
      <div className="flex items-center gap-3">
        <StateChip state={findingState(f.severity)} label={f.severity} />
        <span className="text-sm font-medium">{f.target}</span>
        <Badge variant="outline">{f.status}</Badge>
      </div>

      {/* Descent (§1.8): up to the Baseline that raised it, down to the check Run behind it. */}
      <div className="mt-3 flex flex-wrap items-center gap-2 text-xs">
        <Badge variant="outline">↑ baseline:{f.baseline}</Badge>
        {f.framework && <Badge variant="outline">{f.framework}</Badge>}
        {f.entityId && (
          <Link to="/entities/$id" params={{ id: f.entityId }}>
            <Badge variant="accent">entity ↗</Badge>
          </Link>
        )}
        {f.runId && (
          <Link to="/runs/$id" params={{ id: f.runId }} className="ml-auto">
            <Badge variant="accent" className="gap-1">
              ↓ check Run <ExternalLink className="size-3" />
            </Badge>
          </Link>
        )}
      </div>

      <div className="mt-5 grid gap-4 md:grid-cols-2">
        <Field label="Drifted observations">
          <span className="tabular">{f.consecutiveDrifted}</span> consecutive (§4.3 damping)
        </Field>
        <Field label="First / last observed">
          {relTime(f.firstObserved)} → {relTime(f.lastObserved)}
        </Field>
      </div>

      <h2 className="mt-6 mb-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        Observed vs expected
      </h2>
      <pre className="max-h-96 overflow-auto rounded-lg border border-border bg-surface-sunken p-3 font-mono text-xs">
        {f.diff !== undefined ? JSON.stringify(f.diff, null, 2) : "— no diff recorded —"}
      </pre>

      {evidence.data && (
        <p className="mt-3 text-xs text-muted-foreground">
          Evidence sealed: <span className="font-mono">{evidence.data.sha256?.slice(0, 16)}…</span>{" "}
          ({evidence.data.sizeBytes} bytes)
        </p>
      )}
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="rounded-lg border border-border p-3">
      <div className="text-xs font-medium text-muted-foreground">{label}</div>
      <div className="mt-1 text-sm">{children}</div>
    </div>
  );
}

function RollChip({
  label,
  n,
  state,
}: {
  label: string;
  n: number;
  state: Parameters<typeof StateChip>[0]["state"];
}) {
  return (
    <span className="flex items-center gap-1.5">
      <StateChip state={state} label={`${n}`} />
      <span className="text-muted-foreground">{label}</span>
    </span>
  );
}

function rollup(findings: Schema["Finding"][]) {
  return {
    critical: findings.filter((f) => f.severity === "critical").length,
    warning: findings.filter((f) => f.severity === "warning").length,
    open: findings.filter((f) => f.status === "open" || f.status === "pending").length,
    resolved: findings.filter((f) => f.status === "resolved").length,
  };
}
