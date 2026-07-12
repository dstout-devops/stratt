// Baselines: read-only (CaC, ADR-0019) — checkable desired state: View +
// check Step + remediation ref + cadence (charter §2.4).
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { api } from "../api/client";
import { Card, DataTable, KV, StateChip } from "../components/ui";
import { FindingsTable } from "./findings";
import { ErrorLine } from "./graph";

export function BaselinesList() {
  const q = useQuery({ queryKey: ["baselines"], queryFn: api.listBaselines, refetchInterval: 10000 });
  return (
    <Card title="Baselines (CaC-declared)">
      {q.error && <ErrorLine err={q.error} />}
      <DataTable
        head={["name", "actuator", "view", "cadence", "severity", "framework", "damping", "state"]}
        rows={(q.data ?? []).map((b) => [
          <Link to="/baselines/$name" params={{ name: b.name }}>
            {b.name}
          </Link>,
          b.actuator ?? "ansible",
          <Link to="/views/$name" params={{ name: b.viewName }}>
            {b.viewName}
          </Link>,
          <span className="mono text-[12px]">{b.cron}</span>,
          <StateChip state={b.severity} />,
          b.framework ?? "—",
          <span className="tnum text-[12px]">{b.dampingObservations ?? 1}</span>,
          <StateChip state={b.paused ? "pending" : "running"} />,
        ])}
      />
    </Card>
  );
}

export function BaselineDetail({ name }: { name: string }) {
  const q = useQuery({ queryKey: ["baseline", name], queryFn: () => api.getBaseline(name), refetchInterval: 10000 });
  const b = q.data;
  return (
    <div className="flex flex-col gap-[var(--space-4)]">
      <Card title={`Baseline ${name}`}>
        {q.error && <ErrorLine err={q.error} />}
        {b && (
          <KV
            items={[
              [
                "view",
                <Link to="/views/$name" params={{ name: b.viewName }}>
                  {b.viewName}
                </Link>,
              ],
              ["actuator", b.actuator ?? "ansible"],
              ["cadence", <span className="mono">{b.cron}</span>],
              ["paused", b.paused ? "yes" : "no"],
              ["severity", <StateChip state={b.severity} />],
              ["framework", b.framework ?? "—"],
              [
                "damping",
                <span className="tnum text-[12px]">{b.dampingObservations ?? 1} consecutive drifted observations to fire (§4.3)</span>,
              ],
              [
                "remediation",
                b.remediationWorkflow ? (
                  <Link to="/workflows/$name" params={{ name: b.remediationWorkflow }}>
                    workflow {b.remediationWorkflow}
                  </Link>
                ) : (
                  "—"
                ),
              ],
              ["principal", <span className="mono text-[12px]">{b.principal ?? "—"}</span>],
              ["credentials", (b.credentialRefs ?? []).join(", ") || "—"],
            ]}
          />
        )}
      </Card>
      {b?.params && (
        <Card title="Check Step params (read-only by construction — ADR-0019)">
          <pre className="mono overflow-x-auto text-[12px] leading-relaxed" style={{ color: "var(--text-primary)" }}>
            {JSON.stringify(b.params, null, 2)}
          </pre>
        </Card>
      )}
      <FindingsTable baseline={name} />
    </div>
  );
}
