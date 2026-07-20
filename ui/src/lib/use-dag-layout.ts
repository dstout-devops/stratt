import { useEffect, useState } from "react";
import * as Comlink from "comlink";
import type { LayoutApi, LayoutNode, LayoutEdge } from "@/workers/layout.worker";

// A layout worker is lazy-spun per layout run and terminated when done — elkjs is heavy but runs
// rarely (only when the DAG shape changes), so a short-lived worker beats a persistent one and dodges
// all the StrictMode/lifecycle ambiguity of a long-held Worker ref. Returns positions keyed by node id.
export function useDagLayout(nodes: LayoutNode[], edges: LayoutEdge[], key: string) {
  const [positions, setPositions] = useState<Map<string, { x: number; y: number }> | null>(null);

  useEffect(() => {
    if (nodes.length === 0) {
      setPositions(new Map());
      return;
    }
    let cancelled = false;
    const worker = new Worker(new URL("../workers/layout.worker.ts", import.meta.url), {
      type: "module",
    });
    const api = Comlink.wrap<LayoutApi>(worker);
    api
      .layout(nodes, edges)
      .then((placed) => {
        if (cancelled) return;
        setPositions(new Map(placed.map((p) => [p.id, { x: p.x, y: p.y }])));
      })
      .catch(() => {
        if (!cancelled) setPositions(new Map());
      })
      .finally(() => worker.terminate());
    return () => {
      cancelled = true;
      worker.terminate();
    };
    // key encodes the DAG shape (node ids + edges); re-layout only when it actually changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  return positions;
}

export type { LayoutNode, LayoutEdge };
