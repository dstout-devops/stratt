// LiveLogViewer: virtualized SSE tail of a Run's full task-event stream —
// never truncated, follow-tail by default (ADR-0003 L1/L2). Sunken canvas,
// mono, tabular gutter, severity-mapped rows (design-tokens §5.2).
import { useEffect, useRef, useState } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";

export type RunEvent = {
  runId: string;
  slice?: number;
  seq: number;
  at: string;
  kind: string;
  target?: string;
  payload?: Record<string, unknown>;
};

const ROW = 22; // fixed row height for virtualization (design-tokens §5.2)

function severityToken(kind: string): string {
  if (kind.includes("failed") || kind.includes("error") || kind === "unreachable") return "var(--log-error)";
  if (kind.includes("ok") || kind === "stream-end") return "var(--log-success)";
  if (kind.includes("warn") || kind.includes("retry")) return "var(--log-warn)";
  if (kind === "stdout" || kind === "diagnostic") return "var(--log-debug)";
  return "var(--log-info)";
}

function renderPayload(ev: RunEvent): string {
  if (!ev.payload) return "";
  if (typeof ev.payload["line"] === "string") return ev.payload["line"] as string;
  if (typeof ev.payload["stdout"] === "string") return ev.payload["stdout"] as string;
  return JSON.stringify(ev.payload);
}

export function LiveLogViewer({ runId }: { runId: string }) {
  const [events, setEvents] = useState<RunEvent[]>([]);
  const [live, setLive] = useState(true);
  const [streamErr, setStreamErr] = useState("");
  const [follow, setFollow] = useState(true);
  const parentRef = useRef<HTMLDivElement>(null);
  const followRef = useRef(follow);
  followRef.current = follow;

  useEffect(() => {
    // Event kinds are tool-shaped and unbounded (the spine passes actuator
    // event names through, §1.8) — EventSource only delivers *named*
    // listeners, so it can't tail an open kind set. A fetch reader parses
    // the SSE frames directly and takes every event, whatever its name.
    const ctrl = new AbortController();
    const buffer: RunEvent[] = [];
    let flush: number | undefined;
    const push = (data: string) => {
      try {
        buffer.push(JSON.parse(data) as RunEvent);
      } catch {
        return;
      }
      // rAF batching keeps a firehose smooth without dropping events (L1).
      flush ??= requestAnimationFrame(() => {
        setEvents((prev) => [...prev, ...buffer.splice(0)]);
        flush = undefined;
      });
    };
    (async () => {
      try {
        const res = await fetch(`/api/v1/runs/${encodeURIComponent(runId)}/events`, {
          signal: ctrl.signal,
          headers: { Accept: "text/event-stream" },
        });
        if (!res.ok || !res.body) throw new Error(`${res.status}`);
        const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
        let buf = "";
        let ended = false;
        for (;;) {
          const { done, value } = await reader.read();
          if (done) break;
          buf += value;
          for (;;) {
            const sep = buf.indexOf("\n\n");
            if (sep < 0) break;
            const frame = buf.slice(0, sep);
            buf = buf.slice(sep + 2);
            const data = frame
              .split("\n")
              .filter((l) => l.startsWith("data: "))
              .map((l) => l.slice(6))
              .join("\n");
            if (data) push(data);
            if (/^event: stream-end$/m.test(frame)) ended = true;
          }
          if (ended) break;
        }
      } catch (e) {
        // A failed stream must never masquerade as a finished one (§1.8):
        // aborts (unmount) stay quiet, real failures render verbatim.
        if (!ctrl.signal.aborted) {
          setStreamErr(e instanceof Error ? `event stream failed: ${e.message}` : "event stream failed");
        }
      }
      setLive(false);
    })();
    return () => ctrl.abort();
  }, [runId]);

  const virtualizer = useVirtualizer({
    count: events.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => ROW,
    overscan: 20,
  });

  useEffect(() => {
    if (followRef.current && events.length > 0) {
      virtualizer.scrollToIndex(events.length - 1);
    }
  }, [events.length, virtualizer]);

  return (
    <div>
      <div className="mb-[var(--space-2)] flex items-center gap-[var(--space-3)] text-[12px]">
        <span style={{ color: live ? "var(--state-running)" : "var(--text-muted)" }}>
          {live ? "● live" : "○ stream closed"}
        </span>
        {streamErr && (
          <span style={{ color: "var(--state-failed)" }} data-testid="stream-error">
            {streamErr}
          </span>
        )}
        <span className="tnum" style={{ color: "var(--text-muted)" }}>
          {events.length} events
        </span>
        <label className="flex cursor-pointer items-center gap-[var(--space-1)]" style={{ color: "var(--color-accent)" }}>
          <input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} />
          follow tail
        </label>
      </div>
      <div
        ref={parentRef}
        className="h-[480px] overflow-auto rounded-[var(--radius-md)] border"
        style={{ background: "var(--color-surface-sunken)", borderColor: "var(--color-border)" }}
        data-testid="log-viewer"
      >
        <div style={{ height: virtualizer.getTotalSize(), position: "relative" }}>
          {virtualizer.getVirtualItems().map((row) => {
            const ev = events[row.index];
            return (
              <div
                key={row.key}
                className="mono absolute left-0 flex w-full gap-[var(--space-3)] px-[var(--space-3)] text-[12px] whitespace-nowrap"
                style={{ top: row.start, height: ROW, lineHeight: `${ROW}px` }}
              >
                <span className="tnum w-[90px] shrink-0" style={{ color: "var(--text-muted)" }}>
                  {ev.slice ?? 0}/{ev.seq}
                </span>
                <span className="w-[110px] shrink-0" style={{ color: severityToken(ev.kind) }}>
                  {ev.kind}
                </span>
                <span className="w-[140px] shrink-0 overflow-hidden text-ellipsis" style={{ color: "var(--text-secondary)" }}>
                  {ev.target ?? ""}
                </span>
                <span style={{ color: "var(--text-primary)" }}>{renderPayload(ev)}</span>
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}
