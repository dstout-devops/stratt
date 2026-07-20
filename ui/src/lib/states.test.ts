import { describe, it, expect } from "vitest";
import { runState, findingState } from "@/lib/states";

// The state vocabulary is load-bearing: a color/icon means the same thing across every §1.8 descent
// screen, so the status→State mapping is pinned here (design-tokens §5.1).
describe("runState", () => {
  it("maps Run lifecycle onto the shared palette", () => {
    expect(runState("running")).toBe("running");
    expect(runState("succeeded")).toBe("ok");
    expect(runState("failed")).toBe("failed");
    expect(runState("partial")).toBe("degraded");
    expect(runState("canceled")).toBe("pending");
    expect(runState("weird-unknown")).toBe("pending");
  });
});

describe("findingState", () => {
  it("maps Finding severity onto the shared palette", () => {
    expect(findingState("critical")).toBe("failed");
    expect(findingState("serious")).toBe("degraded");
    expect(findingState("warning")).toBe("attention");
    expect(findingState("info")).toBe("ok");
  });
});
