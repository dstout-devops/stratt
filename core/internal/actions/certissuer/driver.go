package certissuer

// driverPy performs one cert operation against the CLM and prints one terminal
// event with typed outputs. It reads the token from the injected CredentialRef
// file and NEVER echoes the token or the issued private key (§2.5, §1.8) — only
// serials/timestamps. dryRun plans the operation without any CLM write.
const driverPy = `import json, os, sys, urllib.request, urllib.error

base = "/runner/project"
cred = "/runner/credentials/cert-issuer"
with open(base + "/step.json") as f:
    p = json.load(f)
with open(cred + "/token") as f:
    token = f.read().strip()

addr = (p.get("addr") or "").rstrip("/")
mount = p.get("mount") or "pki"
op = p.get("operation")
dry = bool(p.get("dryRun"))

def call(method, path, body=None):
    url = addr + "/v1/" + mount + path
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method,
        headers={"X-Vault-Token": token, "Content-Type": "application/json"})
    resp = urllib.request.urlopen(req, timeout=15)
    return json.load(resp)

ev = {"counter": 1, "host": p.get("commonName") or p.get("serial") or "cert", "ok": True, "detail": "", "outputs": {}}
try:
    if dry:
        # Plan only — describe the intended change, touch nothing (§2.2 dry-run).
        ev["event"] = "cert_planned"
        ev["outputs"] = {"operation": op, "dryRun": True,
            "commonName": p.get("commonName"), "serial": p.get("serial")}
    elif op == "issue" or op == "renew":
        out = call("POST", "/issue/" + (p.get("role") or ""),
            {"common_name": p.get("commonName"), "ttl": p.get("ttl") or "720h"})
        d = out["data"]
        if op == "issue":
            ev["event"] = "cert_issued"
            ev["outputs"] = {"serial": d["serial_number"], "notAfter": str(d.get("expiration"))}
        else:
            ev["event"] = "cert_renewed"
            ev["outputs"] = {"newSerial": d["serial_number"], "oldSerial": p.get("serial") or ""}
            if p.get("serial"):
                call("POST", "/revoke", {"serial_number": p["serial"]})
    elif op == "revoke":
        out = call("POST", "/revoke", {"serial_number": p.get("serial")})
        ev["event"] = "cert_revoked"
        ev["outputs"] = {"serial": p.get("serial"), "revocationTime": out["data"].get("revocation_time")}
    else:
        ev.update(ok=False, event="cert_failed", detail="unknown operation")
except urllib.error.HTTPError as e:
    ev.update(ok=False, event="cert_failed", detail="http %d" % e.code)
except Exception as e:
    # NEVER str(e): CLM errors can embed the addr; class only (§2.5, §1.8).
    ev.update(ok=False, event="cert_failed", detail=type(e).__name__)

sys.stdout.write(json.dumps(ev) + "\n")
sys.stdout.flush()
sys.exit(0 if ev["ok"] else 1)
`
