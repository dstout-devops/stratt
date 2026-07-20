import { Link, useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { workflowsQuery, workflowQuery } from "@/lib/data";
import { useHoverPrefetch } from "@/lib/prefetch";
import { WorkflowDAG } from "@/components/workflow-dag";
import { TableShell } from "@/components/table";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorLine, EmptyState } from "@/components/feedback";
import type { Schema } from "@/api/client";

// Workflows are the durable DAG definitions behind WorkflowRuns (the Execution column). Read-only
// here — the definition lives in Git desired-state (§1.2); this is the projection + the descent entry.
export function WorkflowsList() {
  const { data, isPending, error } = useQuery(workflowsQuery());
  const workflows = data ?? [];
  return (
    <div className="p-6">
      <div className="mb-4 flex items-center gap-4">
        <h1 className="text-lg font-semibold">Workflows</h1>
        <Link
          to="/runs"
          className="text-sm text-muted-foreground transition-colors hover:text-foreground"
        >
          Runs
        </Link>
      </div>
      {error && <ErrorLine error={error} />}
      {isPending ? (
        <Skeleton className="h-40 w-full" />
      ) : workflows.length === 0 ? (
        <EmptyState label="No Workflows declared." />
      ) : (
        <TableShell head={["Workflow", "Steps", "Gates"]}>
          {workflows.map((w: Schema["Workflow"]) => (
            <WorkflowRow key={w.name} workflow={w} />
          ))}
        </TableShell>
      )}
    </div>
  );
}

function WorkflowRow({ workflow }: { workflow: Schema["Workflow"] }) {
  const prefetch = useHoverPrefetch(workflowQuery(workflow.name));
  const steps = workflow.steps ?? [];
  const gates = steps.filter((s) => s.gate).length;
  return (
    <tr className="border-t border-border transition-colors hover:bg-accent/60" {...prefetch}>
      <td className="px-3 py-2 font-medium">
        <Link
          to="/workflows/$name"
          params={{ name: workflow.name }}
          className="text-primary hover:underline"
        >
          {workflow.name}
        </Link>
      </td>
      <td className="px-3 py-2 tabular text-muted-foreground">{steps.length}</td>
      <td className="px-3 py-2">
        {gates > 0 ? (
          <Badge variant="outline">{gates}</Badge>
        ) : (
          <span className="text-muted-foreground">—</span>
        )}
      </td>
    </tr>
  );
}

export function WorkflowDetail() {
  const { name } = useParams({ from: "/workflows/$name" });
  const { data: workflow, isPending, error } = useQuery(workflowQuery(name));
  return (
    <div className="p-6">
      <div className="mb-4 flex items-center gap-2">
        <Link to="/workflows" className="text-sm text-muted-foreground hover:text-foreground">
          Workflows
        </Link>
        <span className="text-muted-foreground">/</span>
        <h1 className="font-mono text-sm font-semibold">{name}</h1>
      </div>
      {error && <ErrorLine error={error} />}
      {isPending ? (
        <Skeleton className="h-[420px] w-full" />
      ) : workflow ? (
        <WorkflowDAG workflow={workflow} />
      ) : null}
    </div>
  );
}
