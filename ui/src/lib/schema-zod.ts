import { z } from "zod";
import type { JSONSchema } from "@/lib/schema";

// Compile a JSON Schema into a Zod schema at RUNTIME (ADR-0090 §5). We build Zod inline (gauntlet's
// SchemaForm.buildZodForProperty approach) rather than depend on the unmaintained json-schema-to-zod
// (dependency-scout). Pure + unit-tested: the form's validation is only as good as this. Format
// drives WIDGET choice (in the form), not validation, except where it's a real constraint (email).
export function zodForProperty(s: JSONSchema): z.ZodTypeAny {
  const type = Array.isArray(s.type) ? s.type[0] : s.type;
  if (s.enum && s.enum.length > 0) {
    return z.enum(s.enum.map(String) as [string, ...string[]]);
  }
  switch (type) {
    case "string": {
      let zz = z.string();
      if (s.format === "email") zz = zz.email();
      if (typeof s.minLength === "number") zz = zz.min(s.minLength);
      if (typeof s.maxLength === "number") zz = zz.max(s.maxLength);
      return zz;
    }
    case "number":
    case "integer": {
      // coerce: form inputs arrive as strings; "5" → 5, "" / "abc" → NaN → a validation error.
      let zz = z.coerce.number();
      if (type === "integer") zz = zz.int();
      if (typeof s.minimum === "number") zz = zz.min(s.minimum);
      if (typeof s.maximum === "number") zz = zz.max(s.maximum);
      return zz;
    }
    case "boolean":
      return z.boolean();
    case "array":
      return z.array(s.items ? zodForProperty(s.items) : z.unknown());
    case "object":
      return zodForObject(s);
    default:
      return z.unknown();
  }
}

/** zodForObject builds the object schema, applying required-vs-optional at the object level so a
 * property's own schema stays orthogonal to its required-list membership. */
export function zodForObject(s: JSONSchema): z.ZodType<Record<string, unknown>> {
  const required = new Set(s.required ?? []);
  const shape: Record<string, z.ZodTypeAny> = {};
  for (const [key, ps] of Object.entries(s.properties ?? {})) {
    const field = zodForProperty(ps);
    shape[key] = required.has(key) ? field : field.optional();
  }
  return z.object(shape);
}

/** defaultsFor seeds RHF with controlled empties per type so no field starts uncontrolled. */
export function defaultsFor(s: JSONSchema): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [key, ps] of Object.entries(s.properties ?? {})) {
    if (ps.default !== undefined) {
      out[key] = ps.default;
      continue;
    }
    const type = Array.isArray(ps.type) ? ps.type[0] : ps.type;
    if (ps.enum && ps.enum.length) out[key] = "";
    else if (type === "boolean") out[key] = false;
    else if (type === "array") out[key] = [];
    else if (type === "object") out[key] = {};
    else if (type === "number" || type === "integer") out[key] = "";
    else out[key] = "";
  }
  return out;
}
