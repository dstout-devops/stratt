import { describe, it, expect } from "vitest";
import { eventTone, eventLine } from "@/lib/log-line";
import type { RunEvent } from "@/lib/run-events";

const ev = (kind: string, payload?: Record<string, unknown>): RunEvent => ({
  runId: "r",
  seq: 1,
  at: "",
  kind,
  payload,
});

const leveled = (kind: string, level: RunEvent["level"]): RunEvent => ({ ...ev(kind), level });

describe("eventTone", () => {
  it("maps open/tool-shaped kinds onto the log severity palette", () => {
    expect(eventTone(ev("task-failed"))).toBe("error");
    expect(eventTone(ev("stderr"))).toBe("error");
    expect(eventTone(ev("task-ok"))).toBe("success");
    expect(eventTone(ev("changed"))).toBe("success");
    expect(eventTone(ev("drift-detected"))).toBe("warn");
    expect(eventTone(ev("stdout"))).toBe("muted");
    expect(eventTone(ev("task-start"))).toBe("info");
  });

  it("prefers the level the producer stated over guessing from the kind", () => {
    // The case that motivated ADR-0117 (g): the shim warns about a play that
    // matched no hosts, or an event it could not decode, on kinds whose text
    // reads benign — `runner_on_ok` even contains "ok". Guessing from the kind
    // showed those as success/info, so a warned Run looked clean.
    expect(eventTone(leveled("runner_on_ok", "warn"))).toBe("warn");
    expect(eventTone(leveled("unparsed-event", "warn"))).toBe("warn");
    expect(eventTone(leveled("ee-content", "error"))).toBe("error");
    expect(eventTone(leveled("verbose", "debug"))).toBe("muted");
  });

  it("falls back to the kind when no level was stated", () => {
    // Absence is not "info": every event produced before the field existed, and
    // spine markers that never set one, still need their best-effort tone.
    expect(eventTone(leveled("task-failed", undefined))).toBe("error");
    expect(eventTone(ev("pod-start-failed"))).toBe("error");
    // An explicit info must still win over a kind that reads as failure —
    // otherwise "level wins" is only true when it agrees with the guess.
    expect(eventTone(leveled("failover-complete", "info"))).toBe("info");
  });
});

describe("eventLine", () => {
  it("prefers a human message field", () => {
    expect(eventLine(ev("stdout", { message: "hello" }))).toBe("hello");
    expect(eventLine(ev("stdout", { line: "raw line" }))).toBe("raw line");
    expect(eventLine(ev("task-start", { task: "Install nginx" }))).toBe("Install nginx");
  });
  it("falls back to serialized payload then kind", () => {
    expect(eventLine(ev("x", { rc: 0 }))).toBe('{"rc":0}');
    expect(eventLine(ev("heartbeat"))).toBe("heartbeat");
  });
});
