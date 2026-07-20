import * as Comlink from "comlink";
import ELK from "elkjs/lib/elk.bundled.js";

// elkjs runs OFF the main thread (§L2: the canvas never blocks scroll/interaction while a large DAG
// lays out). The boundary is positions-only — we hand ELK sized nodes + edges and get back {id,x,y}.
// Nothing React/xyflow crosses the worker seam; the main thread owns rendering.
const elk = new ELK();

export interface LayoutNode {
  id: string;
  width: number;
  height: number;
}
export interface LayoutEdge {
  id: string;
  source: string;
  target: string;
}
export interface Positioned {
  id: string;
  x: number;
  y: number;
}

export interface LayoutApi {
  layout(nodes: LayoutNode[], edges: LayoutEdge[], direction?: string): Promise<Positioned[]>;
}

const api: LayoutApi = {
  async layout(nodes, edges, direction = "DOWN") {
    const graph = {
      id: "root",
      layoutOptions: {
        "elk.algorithm": "layered",
        "elk.direction": direction,
        "elk.layered.spacing.nodeNodeBetweenLayers": "64",
        "elk.spacing.nodeNode": "36",
        "elk.layered.considerModelOrder.strategy": "NODES_AND_EDGES",
      },
      children: nodes.map((n) => ({ id: n.id, width: n.width, height: n.height })),
      edges: edges.map((e) => ({ id: e.id, sources: [e.source], targets: [e.target] })),
    };
    const res = await elk.layout(graph);
    return (res.children ?? []).map((c) => ({ id: c.id, x: c.x ?? 0, y: c.y ?? 0 }));
  },
};

Comlink.expose(api);
