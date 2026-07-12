package mcp

// driverPy is the hand-rolled MCP client (JSON-RPC 2.0; stdio = stdlib
// only, http = httpx from the EE image). dependency-scout on the Python MCP
// SDK: REJECT for this bounded driver — it hard-ships an ASGI server stack
// a client never uses; the three verbs here (initialize, tools/list,
// tools/call) are proportionate to hand-roll. Canonical schema hash =
// sha256 over json.dumps(sort_keys=True, separators=(",",":")) — the exact
// form Go pins (sorted keys, compact).
const driverPy = `import hashlib, json, os, subprocess, sys

# Overridable only for the CI conformance fixture; pods always use the
# read-only ConfigMap mount.
base = os.environ.get("STRATT_DRIVER_BASE", "/runner/project")
with open(base + "/step.json") as f:
    step = json.load(f)

counter = 0
def emit(**kw):
    global counter
    counter += 1
    kw["counter"] = counter
    sys.stdout.write(json.dumps(kw) + "\n")
    sys.stdout.flush()

def canonical_hash(schema):
    # Canonical form shared with the Go side: sorted keys, compact, raw
    # UTF-8 (ensure_ascii=False; Go uses SetEscapeHTML(false)) — parity is
    # unit-tested over adversarial schemas (guardian on ADR-0022).
    doc = json.dumps(schema, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
    return hashlib.sha256(doc.encode("utf-8")).hexdigest()

def fail(rc):
    emit(event="mcp_finished", rc=rc, mode=step["mode"], server=step["server"])
    sys.exit(1)

rpc_id = 0

class StdioTransport:
    def __init__(self):
        emit(event="phase", phase="spawn stdio server", server=step["server"])
        # The server source is the Git-reviewed declaration, mounted
        # read-only — never a command from run-time input (ADR-0022).
        # stderr goes to a file so a crashing server's traceback is
        # surfaced, never discarded (§1.8; guardian on ADR-0022).
        self.err_path = "/runner/artifacts/server-stderr.log"
        try:
            self.err_file = open(self.err_path, "w")
        except OSError:
            self.err_path = None
            self.err_file = subprocess.DEVNULL
        self.proc = subprocess.Popen(
            ["python3", base + "/server.py"],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE,
            stderr=self.err_file, text=True)

    def send(self, msg):
        self.proc.stdin.write(json.dumps(msg) + "\n")
        self.proc.stdin.flush()

    def recv_until(self, want_id):
        while True:
            line = self.proc.stdout.readline()
            if not line:
                emit(event="raw", line="stdio server closed the pipe", server=step["server"])
                if self.err_path:
                    try:
                        self.err_file.flush()
                        with open(self.err_path) as f:
                            tail = f.read()[-2000:]
                        for eline in tail.splitlines():
                            if eline.strip():
                                emit(event="raw", line="server stderr: " + eline, server=step["server"])
                    except OSError:
                        pass
                fail(1)
            try:
                msg = json.loads(line)
            except ValueError:
                continue
            if msg.get("id") == want_id and ("result" in msg or "error" in msg):
                return msg
            # Server-initiated requests/notifications are out of scope for
            # this bounded client; ignored.

class HTTPTransport:
    def __init__(self):
        import httpx  # pinned in the EE image
        self.httpx = httpx
        self.endpoint = step["endpoint"]
        self.session_id = None
        self.headers = {"Accept": "application/json, text/event-stream",
                        "Content-Type": "application/json"}
        token_file = step.get("tokenFile")
        if token_file:
            try:
                with open(token_file) as f:
                    self.headers["Authorization"] = "Bearer " + f.read().strip()
            except OSError as e:
                emit(event="raw", line="token file unreadable: %s" % e, server=step["server"])
                fail(1)

    def send_recv(self, msg):
        headers = dict(self.headers)
        if self.session_id:
            headers["Mcp-Session-Id"] = self.session_id
        r = self.httpx.post(self.endpoint, json=msg, headers=headers, timeout=30.0)
        if r.status_code >= 400:
            emit(event="raw", line="http %d: %s" % (r.status_code, r.text[:400]), server=step["server"])
            fail(1)
        sid = r.headers.get("Mcp-Session-Id")
        if sid:
            self.session_id = sid
        ctype = r.headers.get("Content-Type", "")
        if ctype.startswith("text/event-stream"):
            for line in r.text.splitlines():
                if line.startswith("data: "):
                    doc = json.loads(line[6:])
                    if doc.get("id") == msg.get("id"):
                        return doc
            emit(event="raw", line="no matching SSE response", server=step["server"])
            fail(1)
        return r.json()

transport = StdioTransport() if step["transport"] == "stdio" else HTTPTransport()

def rpc(method, prms):
    global rpc_id
    rpc_id += 1
    msg = {"jsonrpc": "2.0", "id": rpc_id, "method": method, "params": prms}
    if step["transport"] == "stdio":
        transport.send(msg)
        resp = transport.recv_until(rpc_id)
    else:
        resp = transport.send_recv(msg)
    if "error" in resp:
        emit(event="raw", line="%s error: %s" % (method, json.dumps(resp["error"])[:400]), server=step["server"])
        fail(1)
    return resp["result"]

def notify(method):
    msg = {"jsonrpc": "2.0", "method": method}
    if step["transport"] == "stdio":
        transport.send(msg)
    else:
        headers = dict(transport.headers)
        if transport.session_id:
            headers["Mcp-Session-Id"] = transport.session_id
        transport.httpx.post(transport.endpoint, json=msg, headers=headers, timeout=30.0)

emit(event="phase", phase="initialize", server=step["server"])
rpc("initialize", {"protocolVersion": "2025-06-18",
                   "capabilities": {},
                   "clientInfo": {"name": "stratt-mcp-driver", "version": "1"}})
notify("notifications/initialized")

tools = rpc("tools/list", {}).get("tools", [])
if step["mode"] == "register":
    # Schemas ride ONLY register-mode events: pinning is a deliberate act,
    # never a side effect of calling a sibling tool (guardian on ADR-0022).
    emit(event="mcp_tools", server=step["server"], rev=step["rev"], tools=[
        {"name": t["name"], "hash": canonical_hash(t.get("inputSchema", {})),
         "inputSchema": t.get("inputSchema", {})}
        for t in tools])
    emit(event="mcp_finished", rc=0, mode="register", server=step["server"])
    sys.exit(0)

emit(event="mcp_tools", server=step["server"], rev=step["rev"],
     names=[t["name"] for t in tools])

tool = next((t for t in tools if t["name"] == step["tool"]), None)
if tool is None:
    emit(event="raw", line="server no longer declares tool %s" % step["tool"], server=step["server"])
    fail(1)
actual = canonical_hash(tool.get("inputSchema", {}))
if actual != step["pinnedHash"]:
    # Drift is blocking (charter §1.5): refuse BEFORE tools/call, with both
    # hashes on the record. Accepting the change = bump rev + re-register.
    emit(event="schema_drift", server=step["server"],
         expected=step["pinnedHash"], actual=actual)
    fail(1)

result = rpc("tools/call", {"name": step["tool"], "arguments": step["arguments"]})
is_error = bool(result.get("isError"))
emit(event="tool_result", server=step["server"], isError=is_error,
     content=result.get("content", []))
emit(event="mcp_finished", rc=1 if is_error else 0, mode="call", server=step["server"])
sys.exit(1 if is_error else 0)
`
