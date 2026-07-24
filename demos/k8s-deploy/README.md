# Demo — k8s: deploy an app

**What you'll see:** you declare a small web app in Config-as-Code, approve one gate, and Stratt
deploys it to Kubernetes through its own plugin port — then you watch the whole descent from Intent
down to the running pod. About five minutes, one command, a real workload at the end.

**Fidelity: real.** This is not a mock. The runner stands up a real (kind) cluster and Stratt renders
a real Deployment + Service through the real helm Actuator. What you deploy, you can `curl`.

---

## Stratt in one paragraph

Stratt is an estate-automation platform: a typed graph of everything you run, plus a durable
orchestration engine, where every tool (Helm, Ansible, OpenTofu, cloud APIs…) is a **plugin** behind
one **sovereign plugin port**. You declare _what you want_ as Config-as-Code; Stratt turns that into
gated, audited **Runs** that drive the tools — and it never hides how it got there. This demo is the
smallest honest slice of that: one declared workload, one gate, one real deploy.

## What this demo teaches

- **Config-as-Code → Workflow.** The app and the deploy live as YAML you apply to a running Stratt —
  no click-ops. ([estate/workflows/deploy-hello.yaml](estate/workflows/deploy-hello.yaml))
- **The gate.** Nothing mutates the cluster until a `platform-admins` approver says so. The approval
  is a first-class, audited decision — not a side channel.
- **The plugin port.** The deploy runs as `helm/deploy` — a typed Action contract over the sovereign
  port. Helm is _a plugin_, not baked into the core.
- **The descent (charter §1.8).** After it runs you can walk Intent → Workflow → **Run** → task
  event in the UI, CLI, `/api/v1`, or MCP — the same descent, four equally-authorized surfaces. The
  abstraction never hides the mechanism.

---

## Run it (turnkey — one command)

Prerequisites: Docker, `go`, `kubectl`, `jq`, and this repo. Then:

```bash
task demo:k8s-deploy:run
```

That will (from nothing): bring up a minimal Stratt on kind (`dev:genesis`), apply this demo's
estate, launch the `deploy-hello` Workflow, **auto-approve** its gate as the dev bootstrap-admin,
wait for the Run to converge, and assert the `hello` Deployment is Ready in the `demo` namespace. It
prints the declared **fidelity** up front so the claim can't drift from the code.

See the page it deployed:

```bash
kubectl -n demo port-forward svc/hello 8888:80   # → open http://localhost:8888
```

## Walk it by hand (the narrated path)

The turnkey runner does these for you; do them yourself to _feel_ the descent.

1. **Stand up the floor.** `task dev:genesis` — nothing → a minimal Stratt on kind (spine + strattd +
   a self-retiring `bootstrap-admin`), with the helm Actuator and `helm-deploy` CredentialRef
   registered. (This demo reuses that platform CredentialRef rather than minting new authz.)
2. **Apply the demo estate.** `stratt apply -d demos/k8s-deploy/estate -s http://localhost:8080` —
   this adds the `deploy-hello` Workflow and the `hello` View. Read
   [estate/workflows/deploy-hello.yaml](estate/workflows/deploy-hello.yaml): an `approve` gate Step,
   then a `helm/deploy` Step that renders the `hello-stratt` chart.
3. **Launch the Workflow.** In the UI (`cd ui && npm run dev`) open **Workflows → deploy-hello → Run**,
   or `POST /api/v1/workflows/deploy-hello/runs`. It immediately parks on the gate.
4. **Approve the gate** as a `platform-admins` member. Watch the Run advance to the `deploy` Step.
5. **Watch the descent.** Open the **Run** in the UI and descend: Workflow → Run → the `helm/deploy`
   task event → the helm output. This is the whole point — the mechanism is right there.
6. **See the result.** A real `hello` Deployment + Service in namespace `demo`. Port-forward and open
   the page.

## What you just learned

You drove Stratt's core loop end to end: **declare → gate → run → observe**, with the tool (Helm)
living behind a typed plugin port and the whole descent inspectable. Everything larger — many tools,
many substrates, drift reconciliation — is _more of this shape_, not a different one.

## What's next in the series

This is demo #1. The library ([../README.md](../README.md)) grows toward the full multi-substrate
"enterprise estate": an **ec2-only** demo (real SSH converge), a **vsphere-only** demo (VM provision

- a rich projected graph where Views come alive), and the **enterprise capstone** composing them.
