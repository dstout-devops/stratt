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

describe("eventTone", () => {
  it("maps open/tool-shaped kinds onto the log severity palette", () => {
    expect(eventTone("task-failed")).toBe("error");
    expect(eventTone("stderr")).toBe("error");
    expect(eventTone("task-ok")).toBe("success");
    expect(eventTone("changed")).toBe("success");
    expect(eventTone("drift-detected")).toBe("warn");
    expect(eventTone("stdout")).toBe("muted");
    expect(eventTone("task-start")).toBe("info");
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
