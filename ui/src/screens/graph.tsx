import { Link, useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { viewsQuery, viewEntitiesQuery, entityQuery, contractsQuery } from "@/lib/data";
import { useHoverPrefetch } from "@/lib/prefetch";
import { SchemaValue } from "@/components/schema-value";
import { contractIndex } from "@/lib/schema";
import { Skeleton } from "@/components/ui/skeleton";
import { Badge } from "@/components/ui/badge";
import { relTime } from "@/lib/format";
import { ErrorLine } from "@/components/feedback";
import type { Schema } from "@/api/client";

export function ViewsList() {
  const { data, isPending, error } = useQuery(viewsQuery());
  const views = data ?? [];
  return (
    <div className="p-6">
      <h1 className="mb-4 text-lg font-semibold">Views</h1>
      {error && <ErrorLine error={error} />}
      {isPending ? (
        <Skeleton className="h-40 w-full" />
      ) : views.length === 0 ? (
        <Empty label="No Views declared." />
      ) : (
        <div className="grid gap-3 md:grid-cols-2 lg:grid-cols-3">
          {views.map((v) => (
            <Link
              key={v.name}
              to="/views/$name"
              params={{ name: v.name }}
              className="rounded-lg border border-border bg-card p-4 transition-colors hover:border-primary/40"
            >
              <div className="flex items-center justify-between">
                <span className="font-medium">{v.name}</span>
                <Badge variant="outline">v{v.version}</Badge>
              </div>
              <div className="mt-2 flex flex-wrap gap-1 text-xs text-muted-foreground">
                {v.selector.kinds?.map((k) => (
                  <Badge key={k} variant="outline">
                    {k}
                  </Badge>
                ))}
                {v.declaredBy && <span className="ml-auto">{v.declaredBy}</span>}
              </div>
            </Link>
          ))}
        </div>
      )}
    </div>
  );
}

export function ViewDetail() {
  const { name } = useParams({ from: "/views/$name" });
  const { data, isPending, error } = useQuery(viewEntitiesQuery(name));
  const entities = data?.entities ?? [];
  return (
    <div className="p-6">
      <div className="mb-4 flex items-center gap-3">
        <h1 className="text-lg font-semibold">{name}</h1>
        {data && <Badge variant="outline">v{data.view.version}</Badge>}
        <span className="text-sm text-muted-foreground">{entities.length} members</span>
      </div>
      {error && <ErrorLine error={error} />}
      {isPending ? (
        <Skeleton className="h-40 w-full" />
      ) : entities.length === 0 ? (
        <Empty label="No entities match this View." />
      ) : (
        <div className="overflow-hidden rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="bg-card text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-3 py-2 font-medium">Kind</th>
                <th className="px-3 py-2 font-medium">Entity</th>
                <th className="px-3 py-2 font-medium">Labels</th>
                <th className="px-3 py-2 font-medium">Observed by</th>
              </tr>
            </thead>
            <tbody>
              {entities.map((e) => (
                <EntityRow key={e.id} e={e} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function EntityRow({ e }: { e: Schema["Entity"] }) {
  const prefetch = useHoverPrefetch(entityQuery(e.id));
  const name = e.labels["ansible.name"] ?? e.labels["aws.name"] ?? e.labels["vcenter.name"] ?? e.id;
  return (
    <tr className="border-t border-border transition-colors hover:bg-accent/60" {...prefetch}>
      <td className="px-3 py-2">
        <Badge variant="outline">{e.kind}</Badge>
      </td>
      <td className="px-3 py-2">
        <Link to="/entities/$id" params={{ id: e.id }} className="text-primary hover:underline">
          {name}
        </Link>
      </td>
      <td className="px-3 py-2 text-xs text-muted-foreground">
        {Object.entries(e.labels)
          .slice(0, 3)
          .map(([k, v]) => `${k}=${v}`)
          .join("  ")}
      </td>
      <td className="px-3 py-2 text-xs text-subtle-foreground">
        {e.observedBy?.map((o) => o.name).join(", ") ?? "—"}
      </td>
    </tr>
  );
}

export function EntityDetail() {
  const { id } = useParams({ from: "/entities/$id" });
  const { data, isPending, error } = useQuery(entityQuery(id));
  const contracts = useQuery(contractsQuery());
  const index = contractIndex(contracts.data);
  if (error)
    return (
      <div className="p-6">
        <ErrorLine error={error} />
      </div>
    );
  if (isPending)
    return (
      <div className="p-6">
        <Skeleton className="h-48 w-full" />
      </div>
    );
  const { entity, facets } = data;
  return (
    <div className="p-6">
      <div className="flex items-center gap-3">
        <Badge variant="outline">{entity.kind}</Badge>
        <span className="font-mono text-sm">{entity.id}</span>
      </div>
      <div className="mt-2 flex flex-wrap gap-1 text-xs text-muted-foreground">
        {Object.entries(entity.identityKeys).map(([k, v]) => (
          <Badge key={k} variant="outline">
            {k}={v}
          </Badge>
        ))}
      </div>
      {entity.observedBy && entity.observedBy.length > 0 && (
        <div className="mt-2 text-xs text-subtle-foreground">
          Observed by{" "}
          {entity.observedBy.map((o) => `${o.name} (${relTime(o.lastSeen)})`).join(", ")}
        </div>
      )}

      <h2 className="mt-6 mb-3 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        Facets ({facets.length})
      </h2>
      <div className="grid gap-3">
        {facets.map((f) => (
          <div key={f.namespace} className="rounded-lg border border-border bg-card p-4">
            <div className="mb-2 flex items-center gap-2">
              <span className="text-sm font-medium">{f.namespace}</span>
              <ProvenanceBadge p={f.provenance} />
            </div>
            <SchemaValue value={f.value} schema={index.get(`facets/${f.namespace}`)} />
          </div>
        ))}
        {facets.length === 0 && <Empty label="No Facets on this Entity yet." />}
      </div>
    </div>
  );
}

// Provenance — WHY a value is here (§1.2). writerKind:run links down to the Run that wrote it.
function ProvenanceBadge({ p }: { p: Schema["Provenance"] }) {
  const inner = (
    <Badge variant="outline" className="gap-1 text-[11px]" title={`${p.writerRef} @ ${p.at}`}>
      {p.writerKind}:{p.writerRef.slice(0, 20)}
    </Badge>
  );
  if (p.writerKind === "run") {
    return (
      <Link to="/runs/$id" params={{ id: p.writerRef }}>
        {inner}
      </Link>
    );
  }
  return inner;
}

function Empty({ label }: { label: string }) {
  return (
    <div className="rounded-lg border border-dashed border-border p-8 text-center text-sm text-muted-foreground">
      {label}
    </div>
  );
}
