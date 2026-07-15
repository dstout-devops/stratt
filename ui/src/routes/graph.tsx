// Graph section: Views (the Phase-1 View surface) and Entity detail with
// per-Facet Provenance — who wrote this, from which Run/Source (§1.8).
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { api } from "../api/client";
import { Card, DataTable, KV } from "../components/ui";

export function ViewsList() {
  const q = useQuery({ queryKey: ["views"], queryFn: api.listViews });
  return (
    <Card title="Views">
      {q.error && <ErrorLine err={q.error} />}
      <DataTable
        head={["name", "version", "declared by", "selector"]}
        rows={(q.data ?? []).map((v) => [
          <Link to="/views/$name" params={{ name: v.name }}>
            {v.name}
          </Link>,
          <span className="tnum">v{v.version}</span>,
          v.declaredBy ?? "api",
          <code className="text-[12px]">{JSON.stringify(v.selector)}</code>,
        ])}
      />
    </Card>
  );
}

export function ViewDetail({ name }: { name: string }) {
  const view = useQuery({ queryKey: ["view", name], queryFn: () => api.getView(name) });
  const members = useQuery({ queryKey: ["view-members", name], queryFn: () => api.resolveView(name) });
  return (
    <div className="flex flex-col gap-[var(--space-4)]">
      <Card title={`View ${name}`}>
        {view.error && <ErrorLine err={view.error} />}
        {view.data && (
          <KV
            items={[
              ["version", <span className="tnum">v{view.data.version}</span>],
              ["declared by", view.data.declaredBy ?? "api"],
              ["selector", <code className="text-[12px]">{JSON.stringify(view.data.selector)}</code>],
              ["members", <span className="tnum">{members.data?.entities.length ?? "…"}</span>],
            ]}
          />
        )}
      </Card>
      <Card title="Members (live Entity set)">
        {members.error && <ErrorLine err={members.error} />}
        <DataTable
          head={["entity", "kind", "labels"]}
          rows={(members.data?.entities ?? []).map((e) => [
            <Link to="/entities/$id" params={{ id: e.id }} className="mono text-[12px]">
              {e.labels?.["vcenter.name"] ?? e.id}
            </Link>,
            e.kind,
            <code className="text-[12px]">{JSON.stringify(e.labels ?? {})}</code>,
          ])}
        />
      </Card>
    </div>
  );
}

export function EntityDetail({ id }: { id: string }) {
  const q = useQuery({ queryKey: ["entity", id], queryFn: () => api.getEntity(id) });
  const e = q.data?.entity;
  return (
    <div className="flex flex-col gap-[var(--space-4)]">
      <Card title={e ? `Entity ${e.labels?.["vcenter.name"] ?? e.id}` : "Entity"}>
        {q.error && <ErrorLine err={q.error} />}
        {e && (
          <KV
            items={[
              ["id", <span className="mono text-[12px]">{e.id}</span>],
              ["kind", e.kind],
              ["identity keys", <code className="text-[12px]">{JSON.stringify(e.identityKeys ?? {})}</code>],
              ["labels", <code className="text-[12px]">{JSON.stringify(e.labels ?? {})}</code>],
            ]}
          />
        )}
      </Card>
      <Card title="Observed by — the Sources that vouch for this Entity (§1.2 liveness, ADR-0042)">
        <DataTable
          head={["source", "kind", "last seen"]}
          rows={(e?.observedBy ?? []).map((o) => [
            <span className="mono text-[12px]">{o.name}</span>,
            <span className="text-[12px]">{o.kind}</span>,
            <span className="tnum text-[12px]">{new Date(o.lastSeen).toLocaleString()}</span>,
          ])}
        />
      </Card>
      <Card title="Facets — value + Provenance (§2.1: exactly one writer answer)">
        <DataTable
          head={["namespace", "value", "written by", "at"]}
          rows={(q.data?.facets ?? []).map((f) => [
            <span className="mono text-[12px]">{f.namespace}</span>,
            <code className="text-[12px]">{JSON.stringify(f.value)}</code>,
            <ProvenanceBadge writerKind={f.provenance.writerKind} writerRef={f.provenance.writerRef} />,
            <span className="tnum text-[12px]">{new Date(f.provenance.at).toLocaleString()}</span>,
          ])}
        />
      </Card>
    </div>
  );
}

// ProvenanceBadge: writer kind + ref; a Run ref links down the ladder.
function ProvenanceBadge({ writerKind, writerRef }: { writerKind: string; writerRef: string }) {
  const isRun = writerKind === "run";
  return (
    <span className="text-[12px]">
      <span
        className="mr-[var(--space-1)] rounded-[var(--radius-sm)] border px-[var(--space-1)]"
        style={{ borderColor: "var(--color-border)", color: "var(--text-secondary)" }}
      >
        {writerKind}
      </span>
      {isRun ? (
        <Link to="/runs/$id" params={{ id: writerRef }} className="mono">
          {writerRef.slice(0, 8)}…
        </Link>
      ) : (
        <span className="mono">{writerRef}</span>
      )}
    </span>
  );
}

export function ErrorLine({ err }: { err: unknown }) {
  return (
    <p className="text-[13px]" style={{ color: "var(--state-failed)" }} data-testid="error">
      {err instanceof Error ? err.message : String(err)}
    </p>
  );
}
