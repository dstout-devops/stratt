// The shared state vocabulary (design-tokens §5.1) — pure, unit-testable mapping of Run/Finding
// status onto the one palette so a color means the same wherever the §1.8 descent lands. Kept out
// of the component so it can be tested directly (gauntlet's extract-pure-logic posture).
export type State = "pending" | "running" | "ok" | "attention" | "degraded" | "failed";

/** runState maps a Run/Step/WorkflowRun lifecycle status onto the shared state palette. */
export function runState(status: string): State {
  switch (status) {
    case "running":
      return "running";
    case "succeeded":
      return "ok";
    case "failed":
      return "failed";
    case "partial":
      return "degraded";
    case "canceled":
    case "pending":
    default:
      return "pending";
  }
}

/** stepState maps a WorkflowRun step's terminal outcome onto the shared state palette. */
export function stepState(status: string): State {
  switch (status) {
    case "succeeded":
      return "ok";
    case "failed":
      return "failed";
    case "running":
      return "running";
    case "skipped":
      return "attention";
    default:
      return "pending";
  }
}

/** findingState maps a Finding severity onto the shared state palette. */
export function findingState(severity: string): State {
  switch (severity) {
    case "critical":
      return "failed";
    case "serious":
      return "degraded";
    case "warning":
      return "attention";
    default:
      return "ok";
  }
}
