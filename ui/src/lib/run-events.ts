import { useEffect, useRef, useState } from "react";
import { streamSSE } from "@/lib/sse";
import { authHeader } from "@/lib/session";

// One task event — the floor of the §1.8 descent ladder. Mirrors types/run.go RunEvent. Not a
// generated schema (the endpoint is typed text/event-stream), so it lives here.
export interface RunEvent {
  runId: string;
  slice?: number;
  seq: number;
  at: string;
  kind: string;
  target?: string;
  site?: string;
  payload?: Record<string, unknown>;
}

export type StreamStatus = "connecting" | "streaming" | "ended" | "error";

// useRunEvents opens the replay+follow SSE tail and ingests events with rAF batching — a
// high-volume Run streams to completion without freezing the main thread (ADR-0003 L1/L2, the
// AWX-#15342/#1831 failure this exists to beat). It never caps the buffer (L2: the descent is never
// truncated). The stream is authoritative for events; poll GET /runs/{id} separately for terminal
// status as a backstop.
export function useRunEvents(runId: string) {
  const [events, setEvents] = useState<RunEvent[]>([]);
  const [status, setStatus] = useState<StreamStatus>("connecting");
  const buffer = useRef<RunEvent[]>([]);
  const raf = useRef<number | null>(null);

  useEffect(() => {
    const ctrl = new AbortController();
    let cancelled = false;
    let ended = false;
    setEvents([]);
    buffer.current = [];
    setStatus("connecting");

    const flush = () => {
      raf.current = null;
      if (buffer.current.length === 0) return;
      const batch = buffer.current;
      buffer.current = [];
      setEvents((prev) => prev.concat(batch));
    };
    const schedule = () => {
      if (raf.current === null) raf.current = requestAnimationFrame(flush);
    };

    streamSSE(`/api/v1/runs/${encodeURIComponent(runId)}/events`, {
      headers: authHeader(),
      signal: ctrl.signal,
      onOpen: () => {
        if (!cancelled) setStatus("streaming");
      },
      onFrame: (f) => {
        if (f.event === "stream-end") {
          ended = true;
          return;
        }
        try {
          buffer.current.push(JSON.parse(f.data) as RunEvent);
          schedule();
        } catch {
          /* malformed frame — drop it, never crash the stream */
        }
      },
    })
      .then(() => {
        if (!cancelled) {
          flush();
          setStatus("ended");
        }
      })
      .catch(() => {
        if (!cancelled) setStatus(ended ? "ended" : "error");
      });

    return () => {
      cancelled = true;
      ctrl.abort();
      if (raf.current !== null) cancelAnimationFrame(raf.current);
    };
  }, [runId]);

  return { events, status };
}
