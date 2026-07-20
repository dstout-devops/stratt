import "@xyflow/react/dist/style.css";
import { useMemo } from "react";
import {
  ReactFlow,
  Background,
  Controls,
  Handle,
  Position,
  type Node,
  type Edge,
  type NodeProps,
} from "@xyflow/react";
import { useDagLayout, type LayoutNode, type LayoutEdge } from "@/lib/use-dag-layout";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";
import { type State, stepState } from "@/lib/states";
import type { Schema } from "@/api/client";

const NODE_W = 200;
const NODE_H = 56;

type Step = Schema["Step"];
type StepNodeData = { label: string; kind: "gate" | "actuation"; detail: string; state?: State };

// A step is exactly one of a Gate (human approval) or an actuation (Actuator against a View) — the
// DAG makes the Workflow's shape and its blocking Gates legible at a glance (§1.8: the Workflow rung
// of the descent, one hop above the Run). When rendered for a WorkflowRun, each node carries its live
// state and is a descent link into that step's Run or Gate.
function stepKind(s: Step): "gate" | "actuation" {
  return s.gate ? "gate" : "actuation";
}
function stepDetail(s: Step): string {
  if (s.gate) return "approval";
  return s.actuator ?? "actuation";
}

const STATE_DOT: Record<State, string> = {
  ok: "bg-state-ok",
  running: "bg-state-running",
  pending: "bg-state-pending",
  attention: "bg-state-attention",
  degraded: "bg-state-degraded",
  failed: "bg-state-failed",
};

function StepNode({ data }: NodeProps<Node<StepNodeData>>) {
  const gate = data.kind === "gate";
  return (
    <div
      className={cn(
        "flex flex-col justify-center rounded-md border px-3 text-xs shadow-sm",
        gate ? "border-plan-change bg-plan-change/10" : "border-border bg-card",
      )}
      style={{ width: NODE_W, height: NODE_H }}
    >
      <Handle type="target" position={Position.Top} className="!bg-muted-foreground" />
      <div className="flex items-center gap-1.5">
        {data.state && (
          <span className={cn("size-1.5 shrink-0 rounded-full", STATE_DOT[data.state])} />
        )}
        <span className="truncate font-medium">{data.label}</span>
      </div>
      <div
        className={cn(
          "truncate text-[10px] uppercase tracking-wide",
          gate ? "text-plan-change" : "text-muted-foreground",
        )}
      >
        {data.detail}
      </div>
      <Handle type="source" position={Position.Bottom} className="!bg-muted-foreground" />
    </div>
  );
}

const nodeTypes = { step: StepNode };

export function WorkflowDAG({
  workflow,
  statusByStep,
  onStepClick,
}: {
  workflow: Schema["Workflow"];
  statusByStep?: Record<string, string | undefined>;
  onStepClick?: (name: string) => void;
}) {
  const steps = useMemo(() => workflow.steps ?? [], [workflow]);

  // Positions-only layout boundary: hand the worker sized nodes + edges, receive {id,x,y}.
  const { layoutNodes, layoutEdges, shapeKey } = useMemo(() => {
    const ln: LayoutNode[] = steps.map((s) => ({ id: s.name, width: NODE_W, height: NODE_H }));
    const le: LayoutEdge[] = [];
    for (const s of steps) {
      for (const dep of s.needs ?? []) {
        le.push({ id: `${dep}->${s.name}`, source: dep, target: s.name });
      }
    }
    const key = `${steps.map((s) => s.name).join(",")}|${le.map((e) => e.id).join(",")}`;
    return { layoutNodes: ln, layoutEdges: le, shapeKey: key };
  }, [steps]);

  const positions = useDagLayout(layoutNodes, layoutEdges, shapeKey);

  const { nodes, edges } = useMemo(() => {
    if (!positions) return { nodes: [], edges: [] };
    const ns: Node<StepNodeData>[] = steps.map((s) => {
      const p = positions.get(s.name) ?? { x: 0, y: 0 };
      const status = statusByStep?.[s.name];
      return {
        id: s.name,
        type: "step",
        position: p,
        data: {
          label: s.name,
          kind: stepKind(s),
          detail: stepDetail(s),
          state: status ? stepState(status) : undefined,
        },
      };
    });
    const es: Edge[] = [];
    for (const s of steps) {
      for (const dep of s.needs ?? []) {
        const conditional = s.when && s.when !== "success";
        es.push({
          id: `${dep}->${s.name}`,
          source: dep,
          target: s.name,
          label: conditional ? s.when : undefined,
          style: conditional ? { stroke: "var(--color-plan-change)" } : undefined,
        });
      }
    }
    return { nodes: ns, edges: es };
  }, [positions, steps, statusByStep]);

  if (!positions) return <Skeleton className="h-[420px] w-full" />;

  return (
    <div className="h-[420px] w-full overflow-hidden rounded-lg border border-border bg-background">
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        fitView
        proOptions={{ hideAttribution: true }}
        nodesDraggable={false}
        nodesConnectable={false}
        elementsSelectable={!!onStepClick}
        onNodeClick={onStepClick ? (_, n) => onStepClick(n.id) : undefined}
      >
        <Background gap={16} className="text-border" />
        <Controls showInteractive={false} />
      </ReactFlow>
    </div>
  );
}
