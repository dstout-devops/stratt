// Runs: list + the Run Stream (center-of-gravity screen — live SSE tail).
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { api, type Run } from "../api/client";
import { Card, DataTable, KV, StateChip } from "../components/ui";
import { DescentRail } from "../components/shell";
import { LiveLogViewer } from "../components/logviewer";
import { ErrorLine } from "./graph";

function origin(r: Run) {
  if (r.workflowRunId)
    return (
      <Link to="/workflow-runs/$id" params={{ id: r.workflowRunId }}>
        step {r.stepName}
      </Link>
    );
  if (r.triggeredBy)
    return (
      <Link to="/triggers/$name" params={{ name: r.triggeredBy }}>
        trigger {r.triggeredBy}
      </Link>
    );
  return <span style={{ color: "var(--text-muted)" }}>api</span>;
}

export function RunsList() {
  const q = useQuery({ queryKey: ["runs"], queryFn: () => api.listRuns(100), refetchInterval: 5000 });
  return (
    <Card title="Runs">
      {q.error && <ErrorLine err={q.error} />}
      <DataTable
        head={["run", "status", "view", "origin", "started"]}
        rows={(q.data ?? []).map((r) => [
          <Link to="/runs/$id" params={{ id: r.id }} className="mono text-[12px]">
            {r.id.slice(0, 8)}…
          </Link>,
          <StateChip state={r.status} />,
          r.viewRef?.replace("view://", "") ?? "",
          origin(r),
          <span className="tnum text-[12px]">{new Date(r.startedAt).toLocaleString()}</span>,
        ])}
      />
    </Card>
  );
}

export function RunDetail({ id }: { id: string }) {
  const q = useQuery({
    queryKey: ["run", id],
    queryFn: () => api.getRun(id),
    refetchInterval: (query) => (query.state.data?.finishedAt ? false : 3000),
  });
  const r = q.data;
  return (
    <div className="flex flex-col gap-[var(--space-4)]">
      <DescentRail
        rungs={[
          ...(r?.workflowRunId
            ? [
                { label: "workflow run", to: `/workflow-runs/${r.workflowRunId}` },
                { label: `step ${r.stepName}` },
              ]
            : r?.triggeredBy
              ? [{ label: `trigger ${r.triggeredBy}`, to: `/triggers/${r.triggeredBy}` }]
              : []),
          { label: `run ${id.slice(0, 8)}…` },
          { label: "task events" },
        ]}
      />
      <Card title={`Run ${id.slice(0, 8)}…`}>
        {q.error && <ErrorLine err={q.error} />}
        {r && (
          <KV
            items={[
              ["status", <StateChip state={r.status} />],
              ["view", r.viewRef?.replace("view://", "") ?? ""],
              ["workflow id", <span className="mono text-[12px]">{r.workflowId}</span>],
              ["started", <span className="tnum text-[12px]">{new Date(r.startedAt).toLocaleString()}</span>],
              [
                "finished",
                <span className="tnum text-[12px]">
                  {r.finishedAt ? new Date(r.finishedAt).toLocaleString() : "—"}
                </span>,
              ],
            ]}
          />
        )}
      </Card>
      <Card title="Task events (full stream, live)">
        <LiveLogViewer runId={id} />
      </Card>
    </div>
  );
}
