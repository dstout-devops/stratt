import { Badge } from "@/components/ui/badge";
import { humanizeKey, type JSONSchema } from "@/lib/schema";
import { relTime } from "@/lib/format";
import { cn } from "@/lib/utils";

// Read-only schema-driven rendering (ADR-0003 L7). Renders any Facet/Contract value against its
// pinned JSON Schema — object → labeled field grid (schema order + titles when present), scalars
// typed/formatted, arrays as chips, nested objects recursively. Unknown shapes degrade to JSON,
// never a crash. This is the display half of "plugins ship schemas, not React".
export function SchemaValue({ value, schema }: { value: unknown; schema?: JSONSchema }) {
  if (value === null || value === undefined)
    return <span className="text-muted-foreground">—</span>;
  if (typeof value === "boolean")
    return <Badge variant="outline">{value ? "true" : "false"}</Badge>;
  if (typeof value === "number") return <span className="tabular">{value}</span>;
  if (typeof value === "string") {
    const isDate = schema?.format?.includes("date") || /^\d{4}-\d\d-\d\dT/.test(value);
    if (isDate) return <span title={value}>{relTime(value)}</span>;
    return <span className="break-words">{value}</span>;
  }
  if (Array.isArray(value)) {
    if (value.length === 0) return <span className="text-muted-foreground">empty</span>;
    if (value.every((v) => typeof v !== "object")) {
      return (
        <div className="flex flex-wrap gap-1">
          {value.map((v, i) => (
            <Badge key={i} variant="outline">
              {String(v)}
            </Badge>
          ))}
        </div>
      );
    }
    return (
      <div className="flex flex-col gap-2">
        {value.map((v, i) => (
          <div key={i} className="rounded-md border border-border p-2">
            <SchemaValue value={v} schema={schema?.items} />
          </div>
        ))}
      </div>
    );
  }
  if (typeof value === "object")
    return <SchemaObject value={value as Record<string, unknown>} schema={schema} />;
  return <span>{String(value)}</span>;
}

function SchemaObject({ value, schema }: { value: Record<string, unknown>; schema?: JSONSchema }) {
  const props = schema?.properties;
  // Schema order first (with titles), then any extra keys the schema didn't declare.
  const ordered = props ? Object.keys(props).filter((k) => k in value) : [];
  const extra = Object.keys(value).filter((k) => !ordered.includes(k));
  const keys = [...ordered, ...extra];
  return (
    <div className="grid gap-2">
      {keys.map((k) => {
        const ps = props?.[k];
        return (
          <div key={k} className="grid grid-cols-[minmax(8rem,14rem)_1fr] gap-3 text-sm">
            <div className="text-subtle-foreground" title={ps?.description}>
              {ps?.title ?? humanizeKey(k)}
            </div>
            <div className={cn("min-w-0")}>
              <SchemaValue value={value[k]} schema={ps} />
            </div>
          </div>
        );
      })}
    </div>
  );
}
