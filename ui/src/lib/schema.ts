import type { Schema } from "@/api/client";

// The JSON Schema subset the renderer + form engine read (ADR-0003 L7/L8). Contracts are pinned
// JSON Schema documents served by GET /contracts; the UI never hardcodes fields — it renders and
// validates from the schema. `x-*` keys are declarative widget hints (data, not an expression
// language — §7.5); more land in later slices.
export interface JSONSchema {
  type?: string | string[];
  title?: string;
  description?: string;
  format?: string;
  properties?: Record<string, JSONSchema>;
  required?: string[];
  items?: JSONSchema;
  enum?: unknown[];
  default?: unknown;
  minLength?: number;
  maxLength?: number;
  minimum?: number;
  maximum?: number;
  minItems?: number;
  additionalProperties?: boolean | JSONSchema;
  "x-renderer"?: string;
}

/** contractIndex maps a Contract/Facet schema name → its parsed JSON Schema (by name, e.g.
 * "facets/cert.expiry"). The bytes are pinned by hash so this is cache-stable. */
export function contractIndex(
  contracts: Schema["Contract"][] | undefined,
): Map<string, JSONSchema> {
  const m = new Map<string, JSONSchema>();
  for (const c of contracts ?? []) {
    // Contract.schema is the already-parsed JSON Schema document (served as an object, pinned by
    // c.hash) — use it directly, never re-parse.
    if (c.schema && typeof c.schema === "object") m.set(c.name, c.schema as unknown as JSONSchema);
  }
  return m;
}

/** humanizeKey turns snake/camel/dotted keys into a readable label when the schema has no title. */
export function humanizeKey(key: string): string {
  return key
    .replace(/[_.]/g, " ")
    .replace(/([a-z])([A-Z])/g, "$1 $2")
    .replace(/^\w/, (c) => c.toUpperCase());
}
