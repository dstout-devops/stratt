import { Link, useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { runsQuery, runQuery } from "@/lib/data";
import { useHoverPrefetch } from "@/lib/prefetch";
import { StateChip } from "@/components/state-chip";
import { LiveLog } from "@/components/live-log";
import { StartRunDialog } from "@/components/start-run-dialog";
import { TableShell } from "@/components/table";
import { ErrorLine, EmptyState, ListSkeleton } from "@/components/feedback";
import { runState } from "@/lib/states";
import { Skeleton } from "@/components/ui/skeleton";
import { Badge } from "@/components/ui/badge";
import type { Schema } from "@/api/client";
import { relTime } from "@/lib/format";

export function RunsList() {
  const { data, isPending, error } = useQuery(runsQuery());
  const runs = data ?? [];
  return (
    <div className="p-6">
      <div className="mb-4 flex items-center gap-4">
        <h1 className="text-lg font-semibold">Runs</h1>
        <Link
          to="/runs/approvals"
          className="text-sm text-muted-foreground transition-colors hover:text-foreground [&.active]:text-foreground"
        >
          Approvals
        </Link>
        <Link
          to="/workflows"
          className="text-sm text-muted-foreground transition-colors hover:text-foreground [&.active]:text-foreground"
        >
          Workflows
        </Link>
        <div className="flex-1" />
        <StartRunDialog />
      </div>
      {error && <ErrorLine error={error} />}
      {isPending ? (
        <ListSkeleton />
      ) : runs.length === 0 ? (
        <EmptyState label="No Runs yet." />
      ) : (
        <TableShell
          head={["Status", "Run", "Against", "Descent", { label: "Started", align: "right" }]}
        >
          {runs.map((r) => (
            <RunRow key={r.id} run={r} />
          ))}
        </TableShell>
      )}
    </div>
  );
}

function RunRow({ run }: { run: Schema["Run"] }) {
  const prefetch = useHoverPrefetch(runQuery(run.id));
  return (
    <tr className="border-t border-border transition-colors hover:bg-accent/60" {...prefetch}>
      <td className="px-3 py-2">
        <StateChip state={runState(run.status)} label={run.status} />
      </td>
      <td className="px-3 py-2">
        <Link
          to="/runs/$id"
          params={{ id: run.id }}
          className="font-mono text-xs text-primary hover:underline"
        >
          {run.id.slice(0, 12)}
        </Link>
      </td>
      <td className="px-3 py-2 text-subtle-foreground">{run.viewRef ?? "—"}</td>
      <td className="px-3 py-2">
        <div className="flex flex-wrap gap-1">
          {run.triggeredBy && <Badge variant="outline">trigger:{run.triggeredBy}</Badge>}
          {run.baseline && <Badge variant="outline">baseline:{run.baseline}</Badge>}
          {run.workflowRunId && <Badge variant="outline">workflow</Badge>}
        </div>
      </td>
      <td className="px-3 py-2 text-right tabular text-muted-foreground">
        {relTime(run.startedAt)}
      </td>
    </tr>
  );
}

export function RunDetail() {
  const { id } = useParams({ from: "/runs/$id" });
  const { data: run, isPending, error } = useQuery(runQuery(id));
  if (error)
    return (
      <div className="p-6">
        <ErrorLine error={error} />
      </div>
    );
  if (isPending)
    return (
      <div className="p-6">
        <Skeleton className="h-40 w-full" />
      </div>
    );
  return (
    <div className="flex h-full flex-col">
      <div className="border-b border-border p-6">
        <div className="flex items-center gap-3">
          <StateChip state={runState(run.status)} label={run.status} />
          <span className="font-mono text-sm">{run.id}</span>
        </div>
        <div className="mt-3 flex flex-wrap gap-2 text-xs">
          {run.viewRef && <Badge variant="outline">{run.viewRef}</Badge>}
          {run.workflowRunId && (
            <Link to="/workflow-runs/$id" params={{ id: run.workflowRunId }}>
              <Badge variant="accent">↑ workflow-run</Badge>
            </Link>
          )}
          {run.baseline && <Badge variant="outline">↑ baseline:{run.baseline}</Badge>}
          {run.sites && run.sites.length > 0 && (
            <Badge variant="outline">sites: {run.sites.join(", ")}</Badge>
          )}
        </div>
      </div>
      {/* The virtualized, uncapped, follow-tail live task-event stream — the floor of descent. */}
      <LiveLog runId={run.id} />
    </div>
  );
}
