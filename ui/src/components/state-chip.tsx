import {
  CircleDashed,
  LoaderCircle,
  CircleCheck,
  TriangleAlert,
  CircleAlert,
  CircleX,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { cn } from "@/lib/utils";
import type { State } from "@/lib/states";

// The one state palette for Run/Step/task-event AND Finding severity (design-tokens §5.1): a color
// means the same thing wherever the §1.8 descent lands. ALWAYS dot + icon + label — never color
// alone (accessibility; sub-3:1 status hues are mitigated exactly here). The status→State mapping
// is pure logic in @/lib/states (unit-tested); this file owns only the presentation.
const MAP: Record<State, { icon: LucideIcon; color: string; spin?: boolean }> = {
  pending: { icon: CircleDashed, color: "text-state-pending" },
  running: { icon: LoaderCircle, color: "text-state-running", spin: true },
  ok: { icon: CircleCheck, color: "text-state-ok" },
  attention: { icon: TriangleAlert, color: "text-state-attention" },
  degraded: { icon: CircleAlert, color: "text-state-degraded" },
  failed: { icon: CircleX, color: "text-state-failed" },
};

export function StateChip({
  state,
  label,
  className,
}: {
  state: State;
  label?: string;
  className?: string;
}) {
  const { icon: Icon, color, spin } = MAP[state];
  return (
    <span className={cn("inline-flex items-center gap-1.5 text-xs font-medium", color, className)}>
      <Icon aria-hidden className={cn("size-3.5", spin && "animate-spin")} />
      <span className="capitalize text-foreground">{label ?? state}</span>
    </span>
  );
}
