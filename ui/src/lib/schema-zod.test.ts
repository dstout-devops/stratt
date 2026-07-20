import { describe, it, expect } from "vitest";
import { zodForObject, defaultsFor } from "@/lib/schema-zod";
import type { JSONSchema } from "@/lib/schema";

const schema: JSONSchema = {
  type: "object",
  required: ["name", "count"],
  properties: {
    name: { type: "string", minLength: 2 },
    count: { type: "integer", minimum: 1 },
    mode: { type: "string", enum: ["a", "b"] },
    enabled: { type: "boolean" },
    tags: { type: "array", items: { type: "string" } },
  },
};

describe("zodForObject", () => {
  const z = zodForObject(schema);

  it("accepts a valid object, coercing numeric strings", () => {
    const r = z.safeParse({ name: "web", count: "3", mode: "a", enabled: true, tags: ["x"] });
    expect(r.success).toBe(true);
    if (r.success) expect(r.data.count).toBe(3);
  });

  it("rejects missing required fields", () => {
    expect(zodForObject(schema).safeParse({ name: "web" }).success).toBe(false); // count missing
  });

  it("enforces constraints (minLength, minimum, enum)", () => {
    expect(z.safeParse({ name: "w", count: 3 }).success).toBe(false); // name too short
    expect(z.safeParse({ name: "web", count: 0 }).success).toBe(false); // count < 1
    expect(z.safeParse({ name: "web", count: 1, mode: "z" }).success).toBe(false); // bad enum
  });

  it("allows optional fields to be absent", () => {
    expect(z.safeParse({ name: "web", count: 1 }).success).toBe(true);
  });
});

describe("defaultsFor", () => {
  it("seeds controlled empties per type", () => {
    expect(defaultsFor(schema)).toEqual({
      name: "",
      count: "",
      mode: "",
      enabled: false,
      tags: [],
    });
  });
});
