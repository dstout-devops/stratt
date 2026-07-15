// Findings: the drift/compliance center-of-gravity screen (charter §3.1,
// ADR-0003 L5): estate roll-up (clean state visible without a query) →
// per-Entity diff → proposed fix. Data: ADR-0019's read-only API.
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { api, type Baseline, type Finding } from "../api/client";
import { Card, DataTable, KV, StateChip } from "../components/ui";
import { DescentRail } from "../components/shell";
import { ErrorLine } from "./graph";

// posture folds a Baseline's live Findings into the L5 roll-up verdict:
// clean (nothing live), damping (pending only — drift observed but not yet
// fired), drifted (open Findings, worst severity shown).
function posture(findings: Finding[]) {
  const live = findings.filter((f) => f.status !== "resolved");
  const open = live.filter((f) => f.status === "open");
  if (open.length > 0) {
    const worst = ["critical", "warning", "info"].find((s) => open.some((f) => f.severity === s));
    return (
      <span className="inline-flex items-center gap-[var(--space-2)]">
        <StateChip state="drifted" />
        <span className="text-[12px]">
          {open.length} open · worst <StateChip state={worst ?? "info"} />
        </span>
      </span>
    );
  }
  if (live.length > 0)
    return (
      <span className="inline-flex items-center gap-[var(--space-2)]">
        <StateChip state="damping" />
        <span className="text-[12px]">{live.length} pending (§4.3 damping)</span>
      </span>
    );
  return <StateChip state="clean" />;
}

// LIMIT is the API's page cap. Hitting it means the fetch is incomplete —
// which must be SAID, never rendered as a clean estate (§1.8, guardian on
// ADR-0020).
const LIMIT = 500;

function CapNotice({ hit, what }: { hit: boolean; what: string }) {
  if (!hit) return null;
  return (
    <p className="mb-[var(--space-2)] text-[12px]" style={{ color: "var(--state-attention)" }}>
      ⚠ capped at {LIMIT} {what} — the picture below is incomplete
    </p>
  );
}

// Roll-up: one row per declared Baseline — the estate drift posture, clean
// states included (L5: a clean state is visible without a manual query).
// Posture reads only LIVE findings (open + pending): resolved history can
// never crowd live drift out of the cap.
export function BaselineRollup() {
  const baselines = useQuery({ queryKey: ["baselines"], queryFn: api.listBaselines, refetchInterval: 10000 });
  const open = useQuery({
    queryKey: ["findings", "open", ""],
    queryFn: () => api.listFindings({ status: "open" }),
    refetchInterval: 10000,
  });
  const pending = useQuery({
    queryKey: ["findings", "pending", ""],
    queryFn: () => api.listFindings({ status: "pending" }),
    refetchInterval: 10000,
  });
  const byBaseline = new Map<string, Finding[]>();
  for (const f of [...(open.data ?? []), ...(pending.data ?? [])]) {
    byBaseline.set(f.baseline, [...(byBaseline.get(f.baseline) ?? []), f]);
  }
  const capped = (open.data?.length ?? 0) >= LIMIT || (pending.data?.length ?? 0) >= LIMIT;
  const loaded = open.isSuccess && pending.isSuccess;
  return (
    <Card title="Estate drift (every Baseline, clean included)">
      {baselines.error && <ErrorLine err={baselines.error} />}
      {open.error && <ErrorLine err={open.error} />}
      {pending.error && <ErrorLine err={pending.error} />}
      <CapNotice hit={capped} what="live findings — postures may under-report" />
      {!loaded && !open.error && !pending.error && (
        <p className="mb-[var(--space-2)] text-[12px]" style={{ color: "var(--text-muted)" }}>
          loading postures…
        </p>
      )}
      <DataTable
        head={["baseline", "posture", "severity", "framework", "cadence", "view"]}
        rows={(loaded ? (baselines.data ?? []) : []).map((b) => [
          <Link to="/baselines/$name" params={{ name: b.name }}>
            {b.name}
          </Link>,
          posture(byBaseline.get(b.name) ?? []),
          <StateChip state={b.severity} />,
          b.framework ?? "—",
          <span className="mono text-[12px]">
            {b.cron}
            {b.paused ? " (paused)" : ""}
          </span>,
          <Link to="/views/$name" params={{ name: b.viewName }}>
            {b.viewName}
          </Link>,
        ])}
      />
    </Card>
  );
}

// FindingsTable: the filterable table, reused by the dashboard and by
// BaselineDetail (baseline preset pins the filter).
export function FindingsTable({ baseline }: { baseline?: string }) {
  const [status, setStatus] = useState("open");
  const q = useQuery({
    queryKey: ["findings", status, baseline ?? ""],
    queryFn: () => api.listFindings({ status: status || undefined, baseline }),
    refetchInterval: 10000,
  });
  return (
    <Card
      title={
        <span className="flex items-center justify-between">
          <span>Findings{baseline ? ` — ${baseline}` : ""}</span>
          <select
            value={status}
            onChange={(e) => setStatus(e.target.value)}
            className="rounded-[var(--radius-md)] border px-[var(--space-2)] py-[2px] text-[13px] font-normal"
            style={{ background: "var(--color-surface)", color: "var(--text-primary)", borderColor: "var(--color-border)" }}
          >
            <option value="open">open</option>
            <option value="pending">pending</option>
            <option value="resolved">resolved</option>
            <option value="">all</option>
          </select>
        </span>
      }
    >
      {q.error && <ErrorLine err={q.error} />}
      <CapNotice hit={(q.data?.length ?? 0) >= LIMIT} what="findings — narrow the filter" />
      <DataTable
        head={["finding", "status", "severity", ...(baseline ? [] : ["baseline"]), "target", "entity", "drifted", "last observed"]}
        rows={(q.data ?? []).map((f) => [
          <Link to="/findings/$id" params={{ id: f.id }} className="mono text-[12px]">
            {f.id.slice(0, 8)}…
          </Link>,
          <StateChip state={f.status} />,
          <StateChip state={f.severity} />,
          ...(baseline
            ? []
            : [
                <Link to="/baselines/$name" params={{ name: f.baseline }}>
                  {f.baseline}
                </Link>,
              ]),
          <span className="mono text-[12px]">{f.target}</span>,
          f.entityId ? (
            <Link to="/entities/$id" params={{ id: f.entityId }} className="mono text-[12px]">
              {f.entityId.slice(0, 8)}…
            </Link>
          ) : (
            <span style={{ color: "var(--text-muted)" }}>—</span>
          ),
          <span className="tnum text-[12px]">×{f.consecutiveDrifted}</span>,
          <span className="tnum text-[12px]">{new Date(f.lastObserved).toLocaleString()}</span>,
        ])}
      />
    </Card>
  );
}

export function FindingsList() {
  return (
    <div className="flex flex-col gap-[var(--space-4)]">
      <BaselineRollup />
      <FindingsTable />
    </div>
  );
}

export function FindingDetail({ id }: { id: string }) {
  const q = useQuery({ queryKey: ["finding", id], queryFn: () => api.getFinding(id), refetchInterval: 10000 });
  const f = q.data;
  const bq = useQuery({
    queryKey: ["baseline", f?.baseline ?? ""],
    queryFn: () => api.getBaseline(f!.baseline),
    enabled: !!f,
  });
  const b: Baseline | undefined = bq.data;
  // The sealed Evidence manifest (§2.4, ADR-0029); 404 = not yet sealed.
  const ev = useQuery({
    queryKey: ["evidence", id],
    queryFn: () => api.getFindingEvidence(id),
    enabled: !!f,
    retry: false,
  });
  return (
    <div className="flex flex-col gap-[var(--space-4)]">
      <DescentRail
        rungs={[
          ...(f ? [{ label: `baseline ${f.baseline}`, to: `/baselines/${f.baseline}` }] : []),
          { label: `finding ${id.slice(0, 8)}…` },
          ...(f?.runId ? [{ label: "evidence run", to: `/runs/${f.runId}` }] : []),
        ]}
      />
      <Card title={`Finding ${id.slice(0, 8)}…`}>
        {q.error && <ErrorLine err={q.error} />}
        {f && (
          <KV
            items={[
              ["status", <StateChip state={f.status} />],
              ["severity", <StateChip state={f.severity} />],
              ["framework", f.framework ?? "—"],
              [
                "baseline",
                <Link to="/baselines/$name" params={{ name: f.baseline }}>
                  {f.baseline}
                </Link>,
              ],
              ["target", <span className="mono text-[12px]">{f.target}</span>],
              [
                "entity",
                f.entityId ? (
                  <Link to="/entities/$id" params={{ id: f.entityId }} className="mono text-[12px]">
                    {f.entityId}
                  </Link>
                ) : (
                  "— (non-Entity target)"
                ),
              ],
              [
                "damping",
                <span className="tnum text-[12px]">
                  {f.consecutiveDrifted} consecutive drifted{b ? ` (fires at ${b.dampingObservations ?? 1})` : ""}
                </span>,
              ],
              ["first observed", <span className="tnum text-[12px]">{new Date(f.firstObserved).toLocaleString()}</span>],
              ["last observed", <span className="tnum text-[12px]">{new Date(f.lastObserved).toLocaleString()}</span>],
              ["opened", <span className="tnum text-[12px]">{f.openedAt ? new Date(f.openedAt).toLocaleString() : "—"}</span>],
              ["resolved", <span className="tnum text-[12px]">{f.resolvedAt ? new Date(f.resolvedAt).toLocaleString() : "—"}</span>],
              ["resolved reason", <span className="text-[12px]">{f.resolvedReason ?? "—"}</span>],
              [
                "evidence",
                f.runId ? (
                  <Link to="/runs/$id" params={{ id: f.runId }} className="mono text-[12px]">
                    run {f.runId.slice(0, 8)}… (full task-event stream)
                  </Link>
                ) : (
                  "—"
                ),
              ],
              [
                "sealed bundle",
                ev.data ? (
                  <a
                    href={`/api/v1/evidence/${ev.data.id}/download`}
                    className="mono text-[12px]"
                    title={`object-locked · sha256 ${ev.data.sha256.slice(0, 12)}… · retain until ${new Date(ev.data.retainUntil).toLocaleDateString()}`}
                  >
                    download (sha256 {ev.data.sha256.slice(0, 8)}…)
                  </a>
                ) : (
                  "— (not sealed)"
                ),
              ],
            ]}
          />
        )}
      </Card>
      <Card title="Observed vs expected (redacted, structure-only — ADR-0019)">
        <pre
          className="mono overflow-x-auto text-[12px] leading-relaxed"
          style={{ color: "var(--text-primary)" }}
        >
          {f?.diff !== undefined ? JSON.stringify(f.diff, null, 2) : "no diff detail on this observation"}
        </pre>
      </Card>
      <Card title="Proposed fix (remediation is a ref — launching stays behind the Workflow's Gate)">
        {b?.remediationWorkflow ? (
          <Link to="/workflows/$name" params={{ name: b.remediationWorkflow }}>
            workflow {b.remediationWorkflow}
          </Link>
        ) : (
          <span style={{ color: "var(--text-muted)" }}>no remediation Workflow declared on this Baseline</span>
        )}
      </Card>
    </div>
  );
}
