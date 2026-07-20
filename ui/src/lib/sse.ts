// Vendored, lean SSE-over-fetch (ADR-0090 §4). Dependency-scout flagged @microsoft/fetch-event-source
// as ~5-yr-stale on the crown-jewel path, so we own this ~60 lines instead. Native EventSource is
// unusable here for two reasons: it can't send our Authorization / X-Stratt-Principal header, and it
// only dispatches to NAMED listeners — but the Run event `kind` set is open/tool-shaped, so we must
// take EVERY frame. We parse the text/event-stream framing (id/event/data, blank-line-delimited)
// straight off the fetch ReadableStream.

export interface SSEFrame {
  id?: string;
  event?: string;
  data: string;
}

export interface StreamOpts {
  headers?: Record<string, string>;
  signal?: AbortSignal;
  onFrame: (frame: SSEFrame) => void;
  onOpen?: () => void;
}

/** streamSSE resolves when the server closes the stream; rejects on transport/HTTP error or abort. */
export async function streamSSE(url: string, opts: StreamOpts): Promise<void> {
  const res = await fetch(url, {
    headers: { Accept: "text/event-stream", ...opts.headers },
    signal: opts.signal,
  });
  if (!res.ok || !res.body) throw new Error(`stream ${res.status} ${res.statusText}`);
  opts.onOpen?.();

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let sep: number;
    // Frames are separated by a blank line (\n\n). Tolerate \r\n.
    while ((sep = indexOfDelim(buffer)) !== -1) {
      const raw = buffer.slice(0, sep).replace(/\r/g, "");
      buffer = buffer.slice(sep + 2);
      const frame = parseFrame(raw);
      if (frame) opts.onFrame(frame);
    }
  }
}

function indexOfDelim(s: string): number {
  const a = s.indexOf("\n\n");
  const b = s.indexOf("\r\n\r\n");
  if (a === -1) return b === -1 ? -1 : b;
  if (b === -1) return a;
  return Math.min(a, b);
}

function parseFrame(raw: string): SSEFrame | null {
  let id: string | undefined;
  let event: string | undefined;
  let data = "";
  for (const line of raw.split("\n")) {
    if (line === "" || line.startsWith(":")) continue;
    const colon = line.indexOf(":");
    const field = colon === -1 ? line : line.slice(0, colon);
    const val = colon === -1 ? "" : line.slice(colon + 1).replace(/^ /, "");
    if (field === "id") id = val;
    else if (field === "event") event = val;
    else if (field === "data") data = data ? `${data}\n${val}` : val;
  }
  if (!data && !event) return null;
  return { id, event, data };
}
