# Demo — app install with a certificate

**What you'll see:** you approve one gate, and Stratt SSHes into a managed Linux host as an
unprivileged user, escalates, mints an X.509 certificate, installs an app that serves TLS on it, and
reports what it observed back into the graph. Then it launches a second Workflow that reaches **no
host at all** — and you watch that fail instead of quietly reporting success. About eight minutes, one
command.

**Fidelity: real.** A real SSH connection to a real `sshd`, real `sudo` escalation, a real RSA key and
X.509 certificate, and a real TLS handshake read back off the wire. Two honest concessions: the host
is a container in the same kind cluster — which is exactly what makes it reachable from an execution
pod today — and the certificate is self-signed, because a real CA belongs behind a Connector, not
inside a play.

---

## Stratt in one paragraph

Stratt is an estate-automation platform: a typed graph of everything you run, plus a durable
orchestration engine, where every tool (Ansible, Helm, OpenTofu, cloud APIs…) is a **plugin** behind
one **sovereign plugin port**. You declare _what you want_ as Config-as-Code; Stratt turns that into
gated, audited **Runs** that drive the tools — and it never hides how it got there. This demo is the
Ansible half of that, taken seriously.

## What this demo teaches

- **The tool's content is part of the build, not the request.** The play calls
  `community.crypto` modules. That collection is not fetched at run time and not named in the
  Workflow — it is **declared, pinned, and installed when the execution environment is built**, and
  the build fails if a version is not exact. Reproducibility is a property of the image, and the Run
  states which content it actually ran with.
  ([`ee/content/crypto.requirements.yml`](../../ee/content/crypto.requirements.yml))
- **Which environment a Step runs in is declared, not passed.**
  [`estate/actuators/ansible-crypto.yaml`](estate/actuators/ansible-crypto.yaml) is an ansible
  Actuator bound to the certificate-capable image. The Step asks for it **by naming the Actuator** —
  `actuator: ansible-crypto`. Nothing hands an image name to a tool at run time, so what a Step can
  execute stays reviewable in Git.
- **Privilege escalation is a declared value.** The node **disables root login**, so writing
  `/etc/ssl` and `/etc/nginx` is only reachable by escalating. `become:` is typed — `enabled`,
  `method`, `user` — so escalation is reviewable in Git and recorded on the Run, rather than hidden in
  a flag string. Strip it and the demo fails; that is the point of putting it on a node that will not
  let you cheat.
- **Credentials are pointers, and the split is deliberate.** The host list carries the **address**;
  the CredentialRef carries the **key**; the control plane holds neither. Material is dereferenced by
  the kubelet into the execution pod at spawn and never written to the graph, a log, or an artifact.
- **Write-back is bounded twice.** The Run reports the app's observed listen port. It could not write
  anything else even if the play claimed it: the Actuator declares a ceiling (`facetNamespaces`) and
  the Step declares its own scope, and the two are intersected. Notice what is **not** written — the
  certificate's own attributes. Those belong to a cert-issuer Connector's Syncer; a self-signed cert
  minted on a node has no system of record to project from, and Stratt keeps one authority per fact.
- **A Run that changed nothing is a failure.** `ansible-playbook` exits **0** when its `hosts:`
  pattern matches nothing — it prints "skipping: no hosts matched" and calls that success. A
  fleet-wide change that silently reached zero machines must never look like one that worked. The
  [`vacuous-run-guard`](estate/workflows/vacuous-run-guard.yaml) Workflow proves it still does not.
- **The descent (charter §1.8).** Walk Intent → Workflow → **Run** → task event in the UI, CLI,
  `/api/v1`, or MCP — the same descent, four equally-authorized surfaces.

---

## Run it (turnkey — one command)

Prerequisites: Docker, `go`, `kubectl`, `jq`, `ssh-keygen`, and this repo. Then:

```bash
task demo:app-cert:run
```

That will (from nothing): bring up kind + a minimal Stratt whose desired state IS this demo's estate,
**build the certificate-capable execution environment** (installing and verifying the pinned
collection at build time), stand up the managed node with a throwaway keypair, wait for the host to
be projected into the graph, launch the `app-install-with-cert` Workflow, **auto-approve** its gate as
the dev bootstrap-admin, wait for convergence, and then assert three things:

1. the app answers a **TLS handshake** on the certificate the play issued (read off the wire, not off
   the disk — a file proves a task ran, a handshake proves the app was installed on it);
2. the Run **projected `app.config.port` into the graph** under its bounded grant;
3. a play that matches no host **fails, and names why**.

It prints the declared **fidelity** up front so the claim cannot drift from the code.

Reach the app yourself:

```bash
kubectl -n stratt port-forward svc/app-node 8443:443   # → https://localhost:8443 (self-signed)
```

## Clean up

```bash
task demo:app-cert:down   # uninstall stratt + delete the managed node and its throwaway key
task dev:kind:down        # full teardown — delete the whole kind cluster
```

`demo:app-cert:run` is idempotent (helm `upgrade --install` re-converges the floor, and the play is
written to converge rather than to append), so you can re-run it without tearing down.

## Walk it by hand (the narrated path)

The turnkey runner does these for you; do them yourself to _feel_ the descent.

1. **Build the execution environment.** `task dev:ee-crypto:build`. Watch the build install
   `community.crypto` **at the pinned version** and verify the resolved set against the declaration —
   an unpinned or drifted version fails the build rather than producing an image nobody can reproduce.
   The same Dockerfile builds the base EE; content is selected by a build argument, not a forked image
   definition.
2. **Stand up the floor with the demo estate as its desired state.** The estate is delivered as
   **Config-as-Code** — the reconcile controller enforces it — because Actuators, Connectors and
   CredentialRefs are CaC-only (charter §2.2/§2.3: Git review authorizes plugin registration, and the
   imperative `stratt apply` door cannot register them). Read
   [`estate/actuators/ansible-crypto.yaml`](estate/actuators/ansible-crypto.yaml): the image, and the
   bounded write grant, are right there in Git.
3. **Watch the host arrive in the graph.** [`estate/hosts/app-node.yaml`](estate/hosts/app-node.yaml)
   is just data; the `declared` Syncer projects it, carrying the reachability address that the Ansible
   shim renders as the connection host. The core never authors a connection variable — the file stays
   authoritative, and a host removed from it is never silently deleted. Confirm:
   `GET /api/v1/views/app-nodes/entities`.
4. **Launch the Workflow.** In the UI (`cd ui && npm run dev`) open **Workflows →
   app-install-with-cert → Run**, or `POST /api/v1/workflows/app-install-with-cert/runs`. It parks on
   the gate immediately.
5. **Approve the gate** as a `platform-admins` member. The install Step dispatches an execution pod
   running the certificate-capable image.
6. **Watch the descent.** Open the **Run** and descend: Workflow → Run → task events. Two things worth
   looking for: an early event stating the **EE content this Run ran with**, and the per-task results
   from the `community.crypto` modules. If anything warns, the log header counts it — a warned Run is
   findable rather than buried.
7. **See the result.** `kubectl -n stratt exec deploy/app-node -- openssl s_client -connect
127.0.0.1:443 </dev/null | openssl x509 -noout -subject -enddate` — a real certificate, on a real
   TLS listener. And `GET /api/v1/views/app-nodes/entities` now carries the observed `app.config`.
8. **Prove the guard.** Launch `vacuous-run-guard`. It touches nothing and **fails**, and the failure
   says why. Compare that to what a tool exiting 0 would have told you.

## What you just learned

You drove a real configuration-management Run end to end: **declare → gate → connect → escalate →
converge → observe**, with the tool's content pinned into a reproducible image, the Step's environment
declared in Git, credentials that never touch the control plane, write-back bounded by declaration,
and a no-op refused rather than reported green.

If you have run AWX or Ansible Automation Platform, this is the same job you already know how to
write — with the parts that usually live in someone's head (which collections, which image, who may
escalate, what a Run is allowed to write, whether it did anything) moved into declarations a reviewer
can read.

## What's next in the series

The library ([`../README.md`](../README.md)) grows toward the full multi-substrate "enterprise
estate": the **k8s-deploy**, **ec2-only** and **vsphere-only** demos each teach one substrate, and the
**enterprise capstone** composes them — provisioning a host on one substrate and then converging it
with exactly the Ansible path you just watched.
