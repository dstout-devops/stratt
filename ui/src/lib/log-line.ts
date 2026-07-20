import type { RunEvent } from "@/lib/run-events";

// Pure, unit-testable log projection (design-tokens §5.2). Kept out of the component so the
// severity mapping and message extraction are pinned by tests, not eyeballed in a virtualized list.
export type Tone = "info" | "warn" | "error" | "success" | "muted";

/** eventTone maps an open/tool-shaped event kind onto the log severity palette. */
export function eventTone(kind: string): Tone {
  const k = kind.toLowerCase();
  if (k.includes("fail") || k.includes("error") || k === "stderr") return "error";
  if (k.includes("ok") || k.includes("success") || k.includes("changed")) return "success";
  if (k.includes("warn") || k.includes("drift") || k.includes("retry") || k.includes("skip")) return "warn";
  if (k === "stdout" || k === "debug") return "muted";
  return "info";
}

/** eventLine pulls a human line from the tool-shaped payload, falling back to the kind. */
export function eventLine(ev: RunEvent): string {
  const p = ev.payload ?? {};
  for (const key of ["message", "line", "stdout", "msg", "detail", "text"]) {
    const v = p[key];
    if (typeof v === "string" && v.length) return v;
  }
  const task = p["task"] ?? p["name"];
  if (typeof task === "string" && task.length) return task;
  const keys = Object.keys(p);
  return keys.length ? JSON.stringify(p) : ev.kind;
}
