import { Link, useParams, useNavigate } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { workflowRunQuery, workflowQuery } from "@/lib/data";
import { WorkflowDAG } from "@/components/workflow-dag";
import { TableShell } from "@/components/table";
import { StateChip } from "@/components/state-chip";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorLine } from "@/components/feedback";
import { runState, stepState } from "@/lib/states";

// The WorkflowRun is the descent hop between a Trigger and its Runs (§1.8). We overlay the live
// per-step outcome onto the Workflow's DAG and make every node a descent link into its Run or Gate —
// the "one-click descent" made literal on the canvas.
export function WorkflowRunDetail() {
  const { id } = useParams({ from: "/workflow-runs/$id" });
  const navigate = useNavigate();
  const { data: detail, isPending, error } = useQuery(workflowRunQuery(id));
  const wr = detail?.workflowRun;
  // The definition (steps + needs) gives the DAG its shape; the run detail gives each step its state.
  const { data: workflow } = useQuery({
    ...workflowQuery(wr?.workflowName ?? ""),
    enabled: !!wr?.workflowName,
  });

  if (error)
    return (
      <div className="p-6">
        <ErrorLine error={error} />
      </div>
    );
  if (isPending || !detail || !wr)
    return (
      <div className="p-6">
        <Skeleton className="h-40 w-full" />
      </div>
    );

  const steps = detail.steps ?? [];
  const statusByStep: Record<string, string | undefined> = {};
  const descent: Record<string, { runId?: string; gateId?: string }> = {};
  for (const s of steps) {
    statusByStep[s.name] = s.status;
    descent[s.name] = { runId: s.runId, gateId: s.gateId };
  }

  const onStepClick = (name: string) => {
    const d = descent[name];
    if (d?.runId) navigate({ to: "/runs/$id", params: { id: d.runId } });
    else if (d?.gateId) navigate({ to: "/runs/approvals" });
  };

  return (
    <div className="p-6">
      <div className="mb-4 flex flex-wrap items-center gap-3">
        <StateChip state={runState(wr.status)} label={wr.status} />
        <Link
          to="/workflows/$name"
          params={{ name: wr.workflowName }}
          className="font-mono text-sm text-primary hover:underline"
        >
          {wr.workflowName}
        </Link>
        <span className="font-mono text-xs text-muted-foreground">{wr.id.slice(0, 12)}</span>
        {wr.triggeredBy && <Badge variant="outline">↑ trigger:{wr.triggeredBy}</Badge>}
      </div>

      {workflow ? (
        <WorkflowDAG workflow={workflow} statusByStep={statusByStep} onStepClick={onStepClick} />
      ) : (
        <Skeleton className="h-[420px] w-full" />
      )}

      <div className="mt-6">
        <TableShell head={["Step", "State", "Descent"]}>
          {steps.map((s) => (
            <tr key={s.name} className="border-t border-border">
              <td className="px-3 py-2 font-medium">{s.name}</td>
              <td className="px-3 py-2">
                {s.status ? (
                  <StateChip state={stepState(s.status)} label={s.status} />
                ) : (
                  <span className="text-xs text-muted-foreground">live — descend</span>
                )}
              </td>
              <td className="px-3 py-2">
                {s.runId ? (
                  <Link
                    to="/runs/$id"
                    params={{ id: s.runId }}
                    className="font-mono text-xs text-primary hover:underline"
                  >
                    ↓ run:{s.runId.slice(0, 8)}
                  </Link>
                ) : s.gateId ? (
                  <Link to="/runs/approvals" className="text-xs text-primary hover:underline">
                    ↓ gate
                  </Link>
                ) : (
                  <span className="text-muted-foreground">—</span>
                )}
              </td>
            </tr>
          ))}
        </TableShell>
      </div>
    </div>
  );
}
