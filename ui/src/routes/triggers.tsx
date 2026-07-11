// Triggers: read-only (CaC, ADR-0010) — declarations + live schedule state.
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { api } from "../api/client";
import { Card, DataTable, KV, StateChip } from "../components/ui";
import { ErrorLine } from "./graph";

export function TriggersList() {
  const q = useQuery({ queryKey: ["triggers"], queryFn: api.listTriggers });
  return (
    <Card title="Triggers (CaC-declared)">
      {q.error && <ErrorLine err={q.error} />}
      <DataTable
        head={["name", "kind", "cron", "view", "principal", "state"]}
        rows={(q.data ?? []).map((t) => [
          <Link to="/triggers/$name" params={{ name: t.name }}>
            {t.name}
          </Link>,
          t.kind,
          <span className="mono text-[12px]">{t.cron}</span>,
          t.viewName,
          <span className="mono text-[12px]">{t.principal ?? ""}</span>,
          <StateChip state={t.paused ? "pending" : "running"} />,
        ])}
      />
    </Card>
  );
}

export function TriggerDetail({ name }: { name: string }) {
  const q = useQuery({ queryKey: ["trigger", name], queryFn: () => api.getTrigger(name), refetchInterval: 10000 });
  const t = q.data?.trigger;
  const s = q.data?.schedule;
  return (
    <div className="flex flex-col gap-[var(--space-4)]">
      <Card title={`Trigger ${name}`}>
        {q.error && <ErrorLine err={q.error} />}
        {t && (
          <KV
            items={[
              ["kind", t.kind],
              ["cron", <span className="mono">{t.cron}</span>],
              ["view", t.viewName],
              ["actuator", t.actuator ?? "ansible"],
              ["principal", <span className="mono text-[12px]">{t.principal ?? "—"}</span>],
              ["credentials", (t.credentialRefs ?? []).join(", ") || "—"],
              ["paused", s?.paused ? "yes" : "no"],
              [
                "next fires",
                <span className="tnum text-[12px]">
                  {(s?.nextFireTimes ?? []).slice(0, 3).map((d) => new Date(d).toLocaleString()).join(" · ") || "—"}
                </span>,
              ],
            ]}
          />
        )}
      </Card>
      <Card title="Recent fired Runs (Trigger → Run, §1.8)">
        <DataTable
          head={["workflow id", "at"]}
          rows={(s?.recentRuns ?? []).map((r) => [
            <span className="mono text-[12px]">{r.workflowId}</span>,
            <span className="tnum text-[12px]">{new Date(r.at).toLocaleString()}</span>,
          ])}
        />
      </Card>
    </div>
  );
}
