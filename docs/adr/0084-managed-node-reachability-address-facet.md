# ADR 0084 — Managed-node reachability is a typed address Facet; core resolves the connection seam, the plugin renders the connection

- **Status:** Accepted
- **Date:** 2026-07-19
- **Deciders:** steward (dstout)
- **Reviews:** charter-guardian **PASS-WITH-CHANGES** (folded below: the connection coordinate is a NEW typed field distinct from the `Vars` tool-var bag — Decision 2 + guardrail 1; the Contract demand ships in the same slice as the schema — guardrail 2; the multi-writer claim rule is stated — Decision 1; the schema is closed, no open-ended fields — Decision 1). vocabulary-linter **PASS** (`mgmt.address` Facet-correct and CMDB-safe; Target field `Address`).
- **Charter sections:** §1.1 (type the seams, not the world), §1.2 (projections, never a second truth), §1.4 (boring spine, pluggable everything), §1.5 (sovereign contracts, multiple transports), §1.8 (never hide diagnosis), §2 (frozen vocabulary), §2.5 (credentials never persist), §9 (no ontology creep)
- **Retires:** the Phase-0 `renderTarget` local-connection stub (`core/internal/orchestrate/orchestrate.go` — every target hardcoded `ansible_connection: local`, named by entity ID).
- **Unblocks:** the clean onboarding **convergence** (a real managed node the ansible remediation SSHes into and converges; the converged port projects back and resolves the drift Finding) — the runtime capstone of ADR-0055 / ADR-0083 / G5.

## Context

The live onboarding e2e (declare → compile-with-default → drift Finding → gated ansible remediation) runs, but it
**cannot converge**: `renderTarget` is an explicit Phase-0 stub that gives every target `ansible_connection: local`
and an entity-ID name. That is fine for a simulated estate — the play executes against the ephemeral EE pod — but it
can neither reach a real fleet node nor tell the truth about having reached one. Two "clean" bars are unmet:

- **Stratt-level:** the loop must close — remediate → **converge** → the observed state projects back → the drift
  Finding auto-resolves. Green must mean graph and reality agree.
- **ansible/AWX-level:** the execution must be AWX-credible — a real inventory (`ansible_host`), a real **machine
  credential**, a real connection to a managed node, honest idempotence. The AAP-replacement thesis demands it.

The load-bearing question is **how core addresses a managed node without learning `ansible_host`** — baking a tool var
into the spine would violate §1.4. **In scope:** the typed reachability seam and its charter boundary. **Out of scope:**
the proving fixture (machine `CredentialRef`, the real sshd/nginx node, the `app.config` fact-back) — a follow-up BUILD
task this ADR unblocks; and ssh/winrm actuators beyond ansible (later, each its own Contract).

## Decision

### 1. Reachability is a typed **address Facet** — `mgmt.address` — Contract-demanded (§1.1)
A host's management reachability is a **Facet**, not a label and not core-model state: `mgmt.address` (parallel to the
existing `mgmt.site` locus Facet), schema **`{ address: string, port?: integer }`** — **closed**: no open-ended fields,
every field is pulled by a consumer (§9 — the "..." is exactly where a reachability seam quietly grows into a device
ontology, so there is none). It is attached by whatever Normalizer/Syncer observes or declares it — the declared-estate
plugin (devices-as-code), the vcenter Syncer (the VM's IP), a build's project-back — and is therefore
**landscape-neutral**: a host is reachable because it carries the Facet, regardless of where it came from.

**Multi-writer claim rule (§2.4).** Because more than one source can assert an address on the same Entity, `mgmt.address`
inherits the **same claim machinery as `mgmt.site`** — a declared-authoritative-view / additive assertion under
cross-source liveness (ADR-0041/0042), **never last-writer-wins and never a precedence field**. The claim rule is not
re-invented here; it is the existing per-key Facet ownership.

### 2. Core resolves the Facet into a **new typed Target field** — distinct from the `Vars` tool-var bag (§1.4)
Target resolution reads `mgmt.address` and renders a **typed, generic connection coordinate** — a **new first-class field
`Address` on `actuators.Target` and on the sovereign-port target message** — carried legibly across the port as targets
already cross. This is **normative, not an implementation detail**: the coordinate is NOT the existing
`Target.Vars map[string]string` tool-var bag. Today the spine violates §1.4 here — `renderTarget` stuffs
`ansible_connection: local` into `Vars`, and `pluginhost` tests pin `Vars["ansible_host"]` crossing the port, i.e. **core
emits tool keys**. This ADR **closes that violation**: core populates the typed `Address` field and **stops writing any
`ansible_*` key into `Vars`**; `Vars` reverts to genuinely tool-authored vars only (or is retired for the connection
path). `plugins/ansible/buildInventory` maps the typed `Address` → `ansible_host` — the plugin authors the tool key, the
spine never does. There is no string `ansible_host` anywhere in core. A host missing the demanded Facet is a **loud
unroutable error at resolve time**, never a silent localhost fallback (§1.8 — a silent local run would hide "we never
reached the node").

### 3. The **plugin renders the connection** — transport beneath the sovereign contract (§1.5)
The ansible shim maps the Target's address coordinate to `ansible_host` in the INI inventory it already builds
(`plugins/ansible/buildInventory`). A future ssh/winrm/salt actuator maps the **same** typed coordinate to its own
connection primitive. The connection var is the plugin's rendering of core's typed seam — REST/SSH/WinRM are transports
beneath our contract, none load-bearing for the core.

### 4. Credentials stay **separate** from address (§2.5) — the AWX machine-credential split
The machine credential (SSH private key + login user) is a **`CredentialRef`** referenced by the actuation Step, resolved
to material **only at pod spawn** and mounted into the EE pod (the existing `InjectFile` rail); the shim points
`ansible_ssh_private_key_file` at the mount. Key material never enters the graph, the address Facet, or an artifact. This
mirrors AWX's inventory-host (`ansible_host`) vs. machine-credential (key/user) split exactly — the model operators know.

### 5. The Phase-0 local stub is **retired**; `local` becomes an explicit, declared choice
No target runs `local` by silent default. A genuinely local target (e.g. the control node itself) opts in explicitly via
its declared address Facet (an explicit `local` address), so the inventory always states, legibly, how each host is
reached. "We ran locally" is never a fallback that masks "we could not reach the fleet" (§1.8).

## Guardrails (binding; a violation if broken)

1. **No tool var in core; the coordinate is a typed field, not `Vars`.** The string `ansible_host` / `ansible_connection`
   (or any tool connection key) never appears in the control plane, including in `Target.Vars` crossing the port. Core
   emits the typed `Target.Address`; the plugin renders the var. Retiring the existing `Vars["ansible_*"]` writes (and the
   tests that pin them) is part of this slice, not deferred.
2. **Facet demanded by a Contract — in the SAME slice.** `mgmt.address` ships only while an actuator Contract consumes it,
   and the ansible actuator Contract's `consumes mgmt.address` declaration **lands in the same change as the schema** —
   never an orphan-schema window (§1.1/§9). The full managed-node fixture may follow, but the schema and its Contract
   demand are atomic. Remove the last consumer → remove the schema.
3. **Address ≠ credential (§2.5).** The address Facet holds no secret; the key rides a `CredentialRef`, mounted at spawn,
   never persisted.
4. **No silent local (§1.8).** A target without a resolvable address is a loud unroutable failure, not a localhost run.
5. **Projection discipline (§1.2).** `mgmt.address` is written only by Normalizer / Syncer / Run provenance — never by
   convention, never a writable device table.

## Charter alignment

§1.1 the reachability seam is one typed Facet, demanded by a Contract — not a device ontology. §1.2 the address is a
projection (observed/declared), rebuildable, single-writer. §1.4 the spine owns a generic address; the plugin owns the
connection var. §1.5 SSH/WinRM are transports beneath the contract. §2.5 credentials resolve only at spawn. §1.8 no
silent local run hides a non-reach.

## Consequences

- Retires the Phase-0 stub; a real ansible remediation SSHes into a real node and converges — both "clean" bars reachable.
- Opens ssh/winrm/salt connection actuators later, each mapping the same `mgmt.address` seam (no re-architecture).
- The follow-up fixture (machine `CredentialRef` + real sshd/nginx node + `app.config` fact-back that resolves the drift
  Finding) is the shipping consumer that satisfies the §1.1 sufficiency gate for `mgmt.address`.
- Devices-as-code (`estate/hosts/*.yaml`) gains an address field the declared-estate Syncer projects into `mgmt.address`.

## Alternatives considered

- **Core emits `ansible_host` directly.** Rejected — bakes a tool into the spine (§1.4); every non-ansible actuator would
  inherit an ansible-shaped var.
- **Address as an entity label.** Rejected — labels are untyped and per-key-owned (§1.1 wants the connection seam typed
  and schema-checked; an address deserves a Facet, not a string label).
- **A `Target.Address` field with no Facet backing.** Rejected — no projection source of truth; violates §1.2 (the graph
  is a rebuildable read-model, not a place to stash connection strings by hand).
- **Keep local-connection; hack a reachable fixture.** Rejected — passes the Stratt bar superficially while failing the
  AWX bar, and a silent local run violates §1.8.
