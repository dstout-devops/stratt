import type { RunEvent } from "@/lib/run-events";

// Pure, unit-testable log projection (design-tokens §5.2). Kept out of the component so the
// severity mapping and message extraction are pinned by tests, not eyeballed in a virtualized list.
export type Tone = "info" | "warn" | "error" | "success" | "muted";

/**
 * eventTone maps one event onto the log severity palette.
 *
 * A stated `level` decides severity: it comes from the plugin port's typed TaskEvent.Level, so it is
 * what the tool actually meant, whereas the kind fallback is substring guesswork over an open
 * vocabulary. The fallback stays, and stays second — `level` is absent for every event produced
 * before ADR-0117 (g) and for spine markers that never set one, and absence must not read as "info"
 * (§1.8). It is why the fallback alone was not enough: it mis-tones a WARN whose kind happens to
 * contain "ok" (`runner_on_ok`) as success, which is exactly the invisibility (g) exists to end.
 */
export function eventTone(ev: Pick<RunEvent, "kind" | "level">): Tone {
  switch (ev.level) {
    case "error":
      return "error";
    case "warn":
      return "warn";
    case "debug":
      return "muted";
  }
  const guessed = toneFromKind(ev.kind);
  // A stated `info` must not be rendered as a failure — but it must not flatten
  // `success` either. The port's level has no "success" member: ok-vs-changed is a KIND
  // distinction the level cannot express, and ansible reports both at INFO, so letting
  // `info` win outright would drain the green out of every play. An explicit info
  // therefore keeps the kind's refinement downward and only caps escalation.
  if (ev.level === "info" && (guessed === "error" || guessed === "warn")) return "info";
  return guessed;
}

function toneFromKind(kind: string): Tone {
  const k = kind.toLowerCase();
  if (k.includes("fail") || k.includes("error") || k === "stderr") return "error";
  if (k.includes("ok") || k.includes("success") || k.includes("changed")) return "success";
  if (k.includes("warn") || k.includes("drift") || k.includes("retry") || k.includes("skip"))
    return "warn";
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
