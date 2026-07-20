import { useEffect, useRef, useState } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";
import { ArrowDownToLine } from "lucide-react";
import { useRunEvents, type RunEvent, type StreamStatus } from "@/lib/run-events";
import { eventTone, eventLine, type Tone } from "@/lib/log-line";
import { StateChip } from "@/components/state-chip";
import type { State } from "@/lib/states";
import { cn } from "@/lib/utils";

const ROW = 22; // fixed row height → virtualization (design-tokens §5.2)

const TONE_CLASS: Record<Tone, string> = {
  info: "text-subtle-foreground",
  warn: "text-log-warn",
  error: "text-log-error",
  success: "text-log-success",
  muted: "text-muted-foreground",
};

const STATUS_STATE: Record<StreamStatus, State> = {
  connecting: "pending",
  streaming: "running",
  ended: "ok",
  error: "failed",
};

// The center-of-gravity screen (§3.1): a virtualized, uncapped, follow-tail live task-event log.
// This is the AWX-beating surface — no 4000-event cap (L2), no freeze at volume (L1).
export function LiveLog({ runId }: { runId: string }) {
  const { events, status } = useRunEvents(runId);
  const parentRef = useRef<HTMLDivElement>(null);
  const [follow, setFollow] = useState(true);

  const virt = useVirtualizer({
    count: events.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => ROW,
    overscan: 16,
  });

  // follow-tail: while engaged, pin to the newest event as the stream grows.
  useEffect(() => {
    if (follow && events.length > 0) virt.scrollToIndex(events.length - 1, { align: "end" });
  }, [events.length, follow, virt]);

  const onScroll = () => {
    const el = parentRef.current;
    if (!el) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
    if (atBottom !== follow) setFollow(atBottom);
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex items-center gap-3 border-b border-border px-3 py-1.5 text-xs">
        <StateChip state={STATUS_STATE[status]} label={status} />
        <span className="tabular text-muted-foreground">{events.length} events</span>
        <div className="flex-1" />
        {!follow && (
          <button
            onClick={() => setFollow(true)}
            className="flex items-center gap-1 text-primary hover:underline"
          >
            <ArrowDownToLine className="size-3.5" /> Follow tail
          </button>
        )}
      </div>
      <div
        ref={parentRef}
        onScroll={onScroll}
        className="min-h-0 flex-1 overflow-auto bg-surface-sunken font-mono text-xs leading-[22px]"
        aria-live={follow ? "polite" : "off"}
      >
        {events.length === 0 && status !== "connecting" ? (
          <div className="p-4 text-muted-foreground">No task events.</div>
        ) : (
          <div style={{ height: virt.getTotalSize(), position: "relative" }}>
            {virt.getVirtualItems().map((vi) => (
              <LogRow key={vi.key} ev={events[vi.index]} top={vi.start} />
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

function LogRow({ ev, top }: { ev: RunEvent; top: number }) {
  return (
    <div
      className="absolute inset-x-0 flex items-center gap-3 px-3"
      style={{ height: ROW, transform: `translateY(${top}px)` }}
    >
      <span className="tabular w-12 shrink-0 text-right text-muted-foreground">{ev.seq}</span>
      <span className={cn("w-24 shrink-0 truncate font-medium", TONE_CLASS[eventTone(ev.kind)])}>{ev.kind}</span>
      {ev.target && <span className="w-40 shrink-0 truncate text-subtle-foreground">{ev.target}</span>}
      <span className="truncate text-foreground">{eventLine(ev)}</span>
    </div>
  );
}
