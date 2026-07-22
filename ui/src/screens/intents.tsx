import { useState } from "react";
import { Link, useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { Plus, Minus } from "lucide-react";
import {
  intentsQuery,
  assignmentsQuery,
  blueprintsQuery,
  compileQuery,
  contractsQuery,
} from "@/lib/data";
import { SchemaValue } from "@/components/schema-value";
import { contractIndex } from "@/lib/schema";
import { TableShell, Tabs } from "@/components/table";
import { Skeleton } from "@/components/ui/skeleton";
import { Badge } from "@/components/ui/badge";
import { StateChip } from "@/components/state-chip";
import { findingState } from "@/lib/states";
import { relTime } from "@/lib/format";
import { ErrorLine, EmptyState } from "@/components/feedback";
import type { Schema } from "@/api/client";

type Tab = "intents" | "assignments" | "blueprints" | "plan";
const TABS = ["intents", "assignments", "blueprints", "plan"] as const;

export function IntentsPage() {
  const [tab, setTab] = useState<Tab>("intents");
  return (
    <div className="p-6">
      <Tabs tabs={TABS} value={tab} onChange={setTab} />
      {tab === "intents" && <IntentsTab />}
      {tab === "assignments" && <AssignmentsTab />}
      {tab === "blueprints" && <BlueprintsTab />}
      {tab === "plan" && <PlanTab />}
    </div>
  );
}

function IntentsTab() {
  const { data, isPending, error } = useQuery(intentsQuery());
  const intents = data ?? [];
  if (isPending) return <Skeleton className="h-40 w-full" />;
  if (error) return <ErrorLine error={error} />;
  if (intents.length === 0) return <EmptyState label="No Intents declared." />;
  return (
    <TableShell head={["Intent", "Kind", "On remove"]}>
      {intents.map((i) => (
        <tr key={i.name} className="border-t border-border hover:bg-accent/60">
          <td className="px-3 py-2">
            <Link
              to="/intents/$name"
              params={{ name: i.name }}
              className="text-primary hover:underline"
            >
              {i.name}
            </Link>
          </td>
          <td className="px-3 py-2">
            <Badge variant="outline">{i.kind}</Badge>
          </td>
          <td className="px-3 py-2 text-subtle-foreground">{i.onRemove ?? "retain"}</td>
        </tr>
      ))}
    </TableShell>
  );
}

function AssignmentsTab() {
  const { data, isPending, error } = useQuery(assignmentsQuery());
  const rows = data ?? [];
  if (isPending) return <Skeleton className="h-40 w-full" />;
  if (error) return <ErrorLine error={error} />;
  if (rows.length === 0) return <EmptyState label="No Assignments." />;
  return (
    <TableShell head={["Assignment", "Intent", "View", "Blueprint", "Max delta"]}>
      {rows.map((a) => (
        <tr key={a.name} className="border-t border-border">
          <td className="px-3 py-2 font-medium">{a.name}</td>
          <td className="px-3 py-2 text-subtle-foreground">{a.intent}</td>
          <td className="px-3 py-2">
            <Link
              to="/views/$name"
              params={{ name: a.view }}
              className="text-primary hover:underline"
            >
              {a.view}
            </Link>
          </td>
          <td className="px-3 py-2 text-subtle-foreground">
            {a.blueprint}@{a.blueprintVersion}
          </td>
          <td className="px-3 py-2 tabular text-muted-foreground">{a.maxDelta ?? "—"}</td>
        </tr>
      ))}
    </TableShell>
  );
}

function BlueprintsTab() {
  const { data, isPending, error } = useQuery(blueprintsQuery());
  const rows = data ?? [];
  if (isPending) return <Skeleton className="h-40 w-full" />;
  if (error) return <ErrorLine error={error} />;
  if (rows.length === 0) return <EmptyState label="No Blueprints." />;
  return (
    <TableShell head={["Blueprint", "Version", "For", "Severity", "Routes"]}>
      {rows.map((b) => (
        <tr key={`${b.name}@${b.version}`} className="border-t border-border">
          <td className="px-3 py-2 font-medium">{b.name}</td>
          <td className="px-3 py-2 tabular">v{b.version}</td>
          <td className="px-3 py-2">
            <Badge variant="outline">{b.for}</Badge>
          </td>
          <td className="px-3 py-2">
            {b.severity && <StateChip state={findingState(b.severity)} label={b.severity} />}
          </td>
          <td className="px-3 py-2 tabular text-muted-foreground">{b.routes.length}</td>
        </tr>
      ))}
    </TableShell>
  );
}

// The L3 membership-delta "plan preview" (ADR-0003 L3): which Entities join/leave each Assignment,
// in the plan/drift palette + sigils (+ − ~) so it reads without color.
function PlanTab() {
  const { data, isPending, error } = useQuery(compileQuery());
  if (isPending) return <Skeleton className="h-40 w-full" />;
  if (error) return <ErrorLine error={error} />;
  const c = data as Schema["CompileStatus"];
  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap gap-3 text-sm text-muted-foreground">
        <span>compiled {c.compiledAt ? relTime(c.compiledAt) : "—"}</span>
        <span>· {c.compiledBaselines ?? 0} Baselines</span>
      </div>
      {c.errors && c.errors.length > 0 && (
        <div className="rounded-md border border-destructive/30 bg-destructive/10 p-3 text-sm text-destructive">
          {c.errors.map((e, i) => (
            <div key={i}>{e}</div>
          ))}
        </div>
      )}
      {(c.deltas ?? []).map((d) => (
        <div key={d.assignment} className="rounded-lg border border-border bg-card p-4">
          <div className="mb-2 flex items-center gap-2">
            <span className="text-sm font-medium">{d.assignment}</span>
            <Badge variant="outline">{d.memberCount} members</Badge>
            {d.paused && <Badge variant="outline">paused</Badge>}
          </div>
          <div className="grid gap-1 font-mono text-xs">
            {d.joins?.map((j) => (
              <div key={`+${j}`} className="flex items-center gap-1.5 text-plan-add">
                <Plus className="size-3" /> {j}
              </div>
            ))}
            {d.leaves?.map((l) => (
              <div key={`-${l}`} className="flex items-center gap-1.5 text-plan-destroy">
                <Minus className="size-3" /> {l}
              </div>
            ))}
            {d.unrouted?.map((u) => (
              <div key={`~${u}`} className="flex items-center gap-1.5 text-plan-change">
                <span className="w-3 text-center font-bold">~</span> {u} (unrouted)
              </div>
            ))}
            {!d.joins?.length && !d.leaves?.length && !d.unrouted?.length && (
              <span className="text-muted-foreground">no membership change</span>
            )}
          </div>
        </div>
      ))}
      {(c.deltas ?? []).length === 0 && <EmptyState label="No Assignment deltas." />}
    </div>
  );
}

export function IntentDetail() {
  const { name } = useParams({ from: "/intents/$name" });
  const { data, isPending, error } = useQuery(intentsQuery());
  const contracts = useQuery(contractsQuery());
  if (isPending)
    return (
      <div className="p-6">
        <Skeleton className="h-40 w-full" />
      </div>
    );
  if (error)
    return (
      <div className="p-6">
        <ErrorLine error={error} />
      </div>
    );
  const intent = (data ?? []).find((i) => i.name === name);
  if (!intent)
    return (
      <div className="p-6">
        <EmptyState label="Intent not found." />
      </div>
    );
  const schema = contractIndex(contracts.data).get(`intents/${intent.kind}`);
  return (
    <div className="p-6">
      <div className="flex items-center gap-3">
        <Badge variant="outline">{intent.kind}</Badge>
        <span className="text-sm font-medium">{intent.name}</span>
        <Badge variant="outline">on remove: {intent.onRemove ?? "retain"}</Badge>
      </div>
      <h2 className="mt-6 mb-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        Spec
      </h2>
      <div className="rounded-lg border border-border bg-card p-4">
        <SchemaValue value={intent.spec} schema={schema} />
      </div>
    </div>
  );
}
