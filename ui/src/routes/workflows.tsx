// Workflows: declarations (read-only, CaC), executions, and the Gates inbox.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { api, type Gate, type Step } from "../api/client";
import { Button, Card, DataTable, Dialog, KV, StateChip } from "../components/ui";
import { DescentRail } from "../components/shell";
import { ErrorLine } from "./graph";

export function WorkflowsList() {
  const q = useQuery({ queryKey: ["workflows"], queryFn: api.listWorkflows });
  const runs = useQuery({ queryKey: ["workflow-runs"], queryFn: () => api.listWorkflowRuns(50), refetchInterval: 5000 });
  return (
    <div className="flex flex-col gap-[var(--space-4)]">
      <Card title="Workflows (CaC-declared)">
        {q.error && <ErrorLine err={q.error} />}
        <DataTable
          head={["name", "steps", "gates"]}
          rows={(q.data ?? []).map((w) => [
            <Link to="/workflows/$name" params={{ name: w.name }}>
              {w.name}
            </Link>,
            <span className="tnum">{w.steps.length}</span>,
            <span className="tnum">{w.steps.filter((s) => s.gate).length}</span>,
          ])}
        />
      </Card>
      <Card title="Recent WorkflowRuns">
        <DataTable
          head={["execution", "workflow", "status", "principal", "started"]}
          rows={(runs.data ?? []).map((wr) => [
            <Link to="/workflow-runs/$id" params={{ id: wr.id }} className="mono text-[12px]">
              {wr.id.slice(0, 8)}…
            </Link>,
            wr.workflowName,
            <StateChip state={wr.status} />,
            <span className="mono text-[12px]">{wr.principal ?? ""}</span>,
            <span className="tnum text-[12px]">{new Date(wr.startedAt).toLocaleString()}</span>,
          ])}
        />
      </Card>
    </div>
  );
}

function stepKind(s: Step): string {
  return s.gate ? "gate" : (s.actuator ?? "ansible");
}

export function WorkflowDetail({ name }: { name: string }) {
  const q = useQuery({ queryKey: ["workflow", name], queryFn: () => api.getWorkflow(name) });
  const nav = useNavigate();
  const start = useMutation({
    mutationFn: () => api.startWorkflowRun(name),
    onSuccess: (wr) => nav({ to: "/workflow-runs/$id", params: { id: wr.id } }),
  });
  return (
    <div className="flex flex-col gap-[var(--space-4)]">
      <Card
        title={
          <span className="flex items-center justify-between gap-[var(--space-4)]">
            <span>Workflow {name}</span>
            <Button onClick={() => start.mutate()} disabled={start.isPending}>
              start run
            </Button>
          </span>
        }
      >
        {q.error && <ErrorLine err={q.error} />}
        {start.error && <ErrorLine err={start.error} />}
        <DataTable
          head={["step", "kind", "needs", "when", "view", "credentials", "approvers"]}
          rows={(q.data?.steps ?? []).map((s) => [
            s.name,
            stepKind(s),
            (s.needs ?? []).join(", "),
            s.when ?? "success",
            s.viewName ?? "",
            (s.credentialRefs ?? []).join(", "),
            s.gate
              ? [
                  ...(s.gate.approvers.principals ?? []).map((p) => `principal ${p}`),
                  ...(s.gate.approvers.teams ?? []).map((t) => `team ${t}`),
                ].join(", ")
              : "",
          ])}
        />
      </Card>
    </div>
  );
}

export function WorkflowRunDetail({ id }: { id: string }) {
  const q = useQuery({
    queryKey: ["workflow-run", id],
    queryFn: () => api.getWorkflowRun(id),
    refetchInterval: (query) => (query.state.data?.workflowRun.finishedAt ? false : 2000),
  });
  const wr = q.data?.workflowRun;
  return (
    <div className="flex flex-col gap-[var(--space-4)]">
      <DescentRail
        rungs={[
          ...(wr ? [{ label: `workflow ${wr.workflowName}`, to: `/workflows/${wr.workflowName}` }] : []),
          { label: `workflow run ${id.slice(0, 8)}…` },
        ]}
      />
      <Card title={`WorkflowRun ${id.slice(0, 8)}…`}>
        {q.error && <ErrorLine err={q.error} />}
        {wr && (
          <KV
            items={[
              ["workflow", <Link to="/workflows/$name" params={{ name: wr.workflowName }}>{wr.workflowName}</Link>],
              ["status", <StateChip state={wr.status} />],
              ["principal", <span className="mono text-[12px]">{wr.principal ?? "anonymous"}</span>],
              ["started", <span className="tnum text-[12px]">{new Date(wr.startedAt).toLocaleString()}</span>],
              [
                "finished",
                <span className="tnum text-[12px]">
                  {wr.finishedAt ? new Date(wr.finishedAt).toLocaleString() : "—"}
                </span>,
              ],
            ]}
          />
        )}
      </Card>
      <Card title="Steps — descend into any rung (§1.8)">
        <DataTable
          head={["step", "outcome", "descend"]}
          rows={(q.data?.steps ?? []).map((s) => [
            s.name,
            s.status ? <StateChip state={s.status} /> : <StateChip state={s.runId || s.gateId ? "running" : "pending"} />,
            s.runId ? (
              <Link to="/runs/$id" params={{ id: s.runId }}>
                run {s.runId.slice(0, 8)}… →
              </Link>
            ) : s.gateId ? (
              <Link to="/gates">gate approval →</Link>
            ) : (
              <span style={{ color: "var(--text-muted)" }}>—</span>
            ),
          ])}
        />
      </Card>
    </div>
  );
}

export function GatesInbox() {
  const [status, setStatus] = useState("pending");
  const q = useQuery({
    queryKey: ["gates", status],
    queryFn: () => api.listGates(status || undefined),
    refetchInterval: 5000,
  });
  const [deciding, setDeciding] = useState<Gate | null>(null);
  return (
    <Card
      title={
        <span className="flex items-center justify-between">
          <span>Gates</span>
          <select
            value={status}
            onChange={(e) => setStatus(e.target.value)}
            className="rounded-[var(--radius-sm)] border px-[var(--space-2)] py-[2px] text-[12px]"
            style={{ background: "var(--color-surface-sunken)", borderColor: "var(--color-border)", color: "var(--text-primary)" }}
          >
            {["pending", "approved", "denied", "expired", ""].map((s) => (
              <option key={s} value={s}>
                {s || "all"}
              </option>
            ))}
          </select>
        </span>
      }
    >
      {q.error && <ErrorLine err={q.error} />}
      <DataTable
        head={["gate", "step", "workflow run", "approvers", "status", "decided by", ""]}
        rows={(q.data ?? []).map((g) => [
          <span className="mono text-[12px]">{g.id.slice(0, 8)}…</span>,
          g.step,
          <Link to="/workflow-runs/$id" params={{ id: g.workflowRunId }} className="mono text-[12px]">
            {g.workflowRunId.slice(0, 8)}…
          </Link>,
          [
            ...(g.approvers.principals ?? []).map((p) => `principal ${p}`),
            ...(g.approvers.teams ?? []).map((t) => `team ${t}`),
          ].join(", "),
          <StateChip state={g.status} />,
          <span className="mono text-[12px]">{g.decidedBy ?? ""}</span>,
          g.status === "pending" ? <Button kind="quiet" onClick={() => setDeciding(g)}>decide</Button> : null,
        ])}
      />
      {deciding && <DecisionDialog gate={deciding} onClose={() => setDeciding(null)} />}
    </Card>
  );
}

function DecisionDialog({ gate, onClose }: { gate: Gate; onClose: () => void }) {
  const [note, setNote] = useState("");
  const qc = useQueryClient();
  const decide = useMutation({
    mutationFn: (approve: boolean) => api.decideGate(gate.id, approve, note),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["gates"] });
      onClose();
    },
  });
  return (
    <Dialog open onClose={onClose} title={`Decide gate: ${gate.step}`}>
      <div className="flex flex-col gap-[var(--space-3)]">
        <p className="m-0 text-[13px]" style={{ color: "var(--text-secondary)" }}>
          workflow run <span className="mono">{gate.workflowRunId.slice(0, 8)}…</span> — the decision is
          recorded with your Principal and note (audit, §1.6).
        </p>
        <textarea
          value={note}
          onChange={(e) => setNote(e.target.value)}
          placeholder="note (optional)"
          rows={3}
          className="rounded-[var(--radius-sm)] border p-[var(--space-2)] text-[13px]"
          style={{ background: "var(--color-surface-sunken)", borderColor: "var(--color-border)", color: "var(--text-primary)" }}
        />
        {decide.error && <ErrorLine err={decide.error} />}
        <div className="flex justify-end gap-[var(--space-2)]">
          <Button kind="quiet" onClick={onClose}>
            cancel
          </Button>
          <Button kind="danger" onClick={() => decide.mutate(false)} disabled={decide.isPending}>
            deny
          </Button>
          <Button onClick={() => decide.mutate(true)} disabled={decide.isPending}>
            approve
          </Button>
        </div>
      </div>
    </Dialog>
  );
}
