# ADR 0125 — Notification sinks are drivers behind a seam, not a switch in the daemon

- **Status:** **Proposed** (2026-07-25, steward). Charter review by hand (this session's rules bar the
  subagent); §1.1/§1.4/§1.5/§2.4/§2.5/§1.8 answered inline. **No new dependency is added** — the one new
  driver is `net/smtp` from the standard library.
- **Date:** 2026-07-25
- **Deciders:** steward
- **Charter sections:** §1.1 (type the seams), §1.4 (boring spine, pluggable everything), §1.5 (sovereign
  contracts, pinned + hash-verified), §1.6 (one capability, every surface), §1.8 (never hide diagnosis),
  §2.4 (no implicit precedence), §2.5 (secrets brokered, never baked)

## Context

The last P3 on the AAP parity table, and the second half of ADR-0117 follow-up **(d)**:

> **Notifications** 🟡 — **webhook sink ONLY** — `notify/dispatcher.go` rejects other kinds; no
> Slack/email/SMTP/PagerDuty.

ADR-0027 booked the residual honestly — _"Typed slack / smtp / pagerduty Sink drivers (webhook reaches
them via their incoming-webhook URLs today)"_ — and that framing is what this ADR revises. Reading the
code for what it actually does turns up something better than three missing drivers:

**There was no seam.** `deliver()` opened with `if sink.Kind != types.SinkWebhook { poison }` and
dispatched the literal string `"notify/webhook"`; `ValidateNotifySink` refused any kind outside a closed
Go list; and `strattd` registered the one delivery Action inline on `STRATT_NOTIFY_PLUGIN_ADDR`. Three
places in the spine named a driver. That is the same defect ADR-0117 (k) removed for the `ansible` and
`script` Actuators — a plugin whose _identity_ had left the core while the core kept a constant saying
which one existed — and it is why "add a Slack sink" read as core work when it never was.

Checking the three named targets against what ships, rather than against the parity row, sharpens it
further:

| Target                    | Reachable before this ADR?                                                                                                                                                                |
| ------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Slack** (incoming hook) | **Yes, already.** The url IS the secret, so it rides the CredentialRef; the body is a `bodyTemplate`. The repo's own notify test has declared a Slack-shaped body since ADR-0027.         |
| **Teams / generic hook**  | **Partly.** Same shape, but a target needing a custom header could not have one — see the inert field below.                                                                              |
| **PagerDuty** (Events v2) | **No — and for a §2.5 reason.** The routing key must ride in the request BODY, and the only body mechanism is a `bodyTemplate` declared in Git. Reaching PagerDuty meant baking a secret. |
| **SMTP / email**          | **No.** Not HTTP at all.                                                                                                                                                                  |

So the honest gap is not "three drivers." It is **one missing seam, one transport, and one inert field** —
`headers` has been a declared property of the `notify/webhook` input Contract since ADR-0027 and the
dispatcher never sent it, so a Sink could not set one and nothing ever failed.

### What already ships that this must reconcile with

| Machinery                                                          | Where                               | Bearing                                                             |
| ------------------------------------------------------------------ | ----------------------------------- | ------------------------------------------------------------------- |
| Delivery is already an **Action over the sovereign port**          | ADR-0046/0052; `plugins/notify`     | The seam existed one layer down; only core's naming of it was fixed |
| `RunAction` resolves an Action **by name from the registry**       | `orchestrate/action.go:143`         | Nothing but the name had to become data                             |
| The registry registers Actions from an **Actuator declaration**    | `connectorregistry/registry.go:236` | `actionNames:` is how a new driver arrives with no strattd change   |
| Actuator-as-declaration, boot blocks deleted not kept              | ADR-0103, ADR-0117 (k)              | The precedent this follows exactly, including deleting the fallback |
| SecretBroker: core hands **coordinates**, plugin resolves material | ADR-0052 MF-A/MF-C                  | Why a driver may hold a secret the control plane must never see     |
| Contracts are pinned, hash-verified **data**                       | `contracts/actions/…`               | What types a driver's params once core stops knowing them           |
| SIEM sinks share the `Sink` Kind but belong to the **forwarder**   | ADR-0034; `types.SIEMSinkKinds`     | A carve-out that must survive, and stays a closed set — see D1      |

## Decision

### D1 — A Sink's `kind` names its delivery Action; core holds no list of kinds

`types.NotifyActionFor(kind)` returns `"notify/" + kind`, and that function is **the whole of core's
knowledge about notification drivers**. `kind: webhook` delivers through `notify/webhook`, `kind: smtp`
through `notify/smtp`, and a kind core has never heard of through `notify/<kind>` — resolved from the
plugin registry like any other Action, never from a Go switch (§1.4). Both hardcoded comparisons are
deleted, and the closed-set refusal in `ValidateNotifySink` with them.

**One field, so there is nothing to disagree with.** The kind IS the selector — deliberately not a `kind`
plus an `action:` beside it, which would be two fields that can contradict each other and no stated winner
(§2.4). This is also why the derivation is mechanical rather than a core-side `map[kind]action`: a map is
a list of drivers wearing a different hat.

**Where the failure surfaces, and why that is the right place.** Core cannot check the kind at declaration
time, because the registry is a runtime fact — so a typo'd kind fails at delivery, recorded on the
`notify_delivery` surface naming the Action that could not be resolved. This is the same posture a Step
naming an unknown Action already has (ADR-0117 (k) recorded that Actions are deliberately unchecked at
declaration), and it is a deliberate trade against §1.8's "as early as possible": the alternative —
validating the kind against the pinned Contract set at parse time — was **rejected**, because contracts
are embedded in the core binary, so it would refuse every third-party driver and re-close the seam we just
opened. Blindness is the feature; the cost is that the diagnosis lands one door later.

**The SIEM kinds stay a closed set.** They are not notify drivers: `stratt-forwarder` links
splunk-hec/syslog/otel-logs in-process, so their validity genuinely is a core fact. The carve-out is
unchanged.

### D2 — Driver-specific config is ONE opaque params bag, typed by the driver's own Contract

`SinkConfig` was accumulating a per-driver field union (`method`, plus `endpoint`/`index`/`facility`/
`insecure` for SIEM), and a webhook `headers` field was never added despite the Contract declaring one.
Continuing that shape means core growing a field for every knob of every driver — the opposite of §1.1's
"type the seams, not the world."

So the notify half of `SinkConfig` is **two fields and no more**:

- **`bodyTemplate`** — core renders the Notice into a body, because that is the Notice→text step every
  driver needs and none should reimplement.
- **`params`** — the driver's own non-secret arguments, merged into the delivery Action's input and
  validated against **that Action's pinned input Contract** (§1.5). `{method: PUT}` or `{headers: {…}}` for
  webhook; `{host, from, to, subject}` for smtp. Core never reads a key here.

`method` moves into `params`, and **`headers` becomes reachable for the first time** — the inert mechanism
closes for free, which is a decent sign the shape is right.

**Two keys are core-owned on every delivery** — `body` and `credentialMount` — and a Sink that also sets
one is **refused, not resolved**. A silent winner between "core rendered the body" and "the declaration
supplied one" is exactly the implicit precedence §2.4 forbids, and it would let a Sink quietly replace the
body the §1.8 descent trail claims was delivered.

**§2.5 is why `params` may not be a secret channel, and why that is not a limitation.** A driver reads its
own secrets from the brokered CredentialRef, so PagerDuty's routing key belongs in the credential a
`notify/pagerduty` driver resolves — **not** in a `bodyTemplate` that lives in Git. That is the precise
reason PagerDuty needs a driver rather than a clever template, and it is a better answer than the one
ADR-0027 booked.

### D3 — `notify` becomes a declared Actuator; the boot block is deleted, not kept

`estate/actuators/notify.yaml` (address, `pluginIdentity`, `tier: trusted`, `dryRunnable: false`,
`actionNames: [notify/webhook, notify/smtp]`) replaces the inline `registerPluginAction("notify/webhook")`
in `strattd`, reconciled by the connectorregistry onto every replica with no restart.

Without this, D1's claim is false: adding a kind would still mean editing `main.go`. With it, a new
destination is **a driver in the plugin plus a name in that file**.

The block is **deleted rather than kept as a fallback**, for the reason ADR-0117 (k) gave: two
registration paths for one Action name collide at §2.4 and make "which one is live?" unanswerable from
Git. Three undocumented env knobs go with it (`STRATT_NOTIFY_{PLUGIN_ADDR,PLUGIN_ID,SOURCE_NAME}`), none of
which anything in the repo set. The declared grant is behaviour-identical: `actuatorGrant` derives
`Source{Kind,Name}` from the Actuator name, which for `notify` IS the plugin identity.

### D4 — Ship `notify/smtp`, and ship exactly one driver

Email is the destination that is genuinely unreachable, the one AAP operators ask for first, and — because
it is not HTTP — **the one that proves the seam is a seam**. A second HTTP driver would have proved
nothing; the port had only ever carried one driver of one transport.

It lands in the existing `plugins/notify` binary as a second Action, on `net/smtp` from the standard
library: no new dependency, no new deployment.

**One driver, not three.** Slack already works (see the table above) and needs no code. A
`notify/pagerduty` driver is now a plugin-side addition anyone can make, with no core change and no ADR —
which is the deliverable. Shipping speculative drivers with no consumer is what ADR-0083 D4's sufficiency
gate and ADR-0104's "add the class when its first provider ships" both refuse, and it is how a seam
accumulates the sprawl it was built to prevent.

Three properties of the driver worth recording, because each is a decision rather than an implementation
detail:

- **STARTTLS is required in the default mode, not attempted-then-shrugged-off.** A relay that does not
  offer it fails the delivery rather than silently sending in cleartext. A notification body carries
  estate detail (run ids, View names, Finding severities) even when the credential does not, and a silent
  downgrade is the whole attack. `tls: none` exists for a plain in-cluster relay and is explicit.
- **The verdict is a sanitized failure class.** An SMTP error string embeds the relay host and the
  envelope exactly as an HTTP error embeds the URL, and the delivery verdict is a control-plane surface
  (§2.5) — so `connect failed` crosses, never `dial tcp relay.internal:587: …`.
- **`net/smtp`'s `PlainAuth` refuses to send credentials over an unencrypted connection.** We rely on that
  fail-closed rather than reimplementing it.

## Charter alignment

- **§1.4.** Three `if <driver>` sites leave the spine. Core's driver knowledge is now one string
  concatenation.
- **§1.1 / §1.5.** The seam is typed where it should be — by the driver's own pinned, hash-verified input
  Contract — rather than by a growing struct in core. An undeclared param is refused **by name** at
  delivery.
- **§2.4.** The kind is a single selector, not a kind-plus-action pair. A params key shadowing a
  core-owned field is refused rather than silently resolved.
- **§2.5.** Material still reaches only the driver, via the SecretBroker. `params` is structurally not a
  secret channel, which is what makes "a target whose secret rides in the body needs a driver" the correct
  answer rather than a workaround.
- **§1.6.** A Sink is CaC-declared and its delivery is a first-class descendable Run, unchanged.
- **§1.8.** Every new refusal names its offender: the Action that could not be resolved, the param the
  driver does not declare, the core-owned key a Sink tried to shadow, the relay that would not encrypt.
- **§1.7.** Zero new dependencies; `net/smtp` is stdlib.

## Consequences

- **Positive.** Parity P3 closes on the seam rather than on a driver count: Slack works today, SMTP ships,
  and PagerDuty/Teams/anything else is a plugin addition with no core change and no ADR. `headers` stops
  being inert. `strattd` loses a boot block and three env knobs. Air-gapped and third-party drivers become
  possible, which a closed kind list made impossible by construction.
- **Negative / trade-offs.** A typo'd `kind` now fails at delivery instead of at parse — the deliberate
  cost of not embedding a driver list, mitigated by the failure naming the exact Action. `SinkConfig.method`
  moves to `config.params.method`, a breaking change to a declaration shape (no Sink is declared anywhere
  in the repo, so the in-tree cost is zero). The SMTP driver is unit-tested against an in-process relay,
  not live-verified against a real MTA — stated rather than implied, and the honest residual.
- **No follow-ups are booked.**

## Alternatives considered

- **Add typed `slack`/`smtp`/`pagerduty` cases to the dispatcher's switch.** Rejected: it is the defect,
  scaled by three. Each new destination would be a core release, and the fourth would still be impossible.
- **Validate the Sink's kind against the pinned Contract set at declaration time.** Rejected in D1 despite
  being the §1.8-preferred door: contracts are embedded in the core binary, so this would refuse every
  third-party driver — closing the seam in the name of failing earlier.
- **Give `SinkConfig` a typed field per driver knob.** Rejected in D2: it types the world instead of the
  seam, and puts core in the business of tracking every driver's arguments.
- **Let a `bodyTemplate` interpolate credential material** so PagerDuty works without a driver. Rejected:
  it would put secret material in a Git-declared template — the §2.5 violation the whole delivery design
  exists to avoid.
- **A long-lived delivery pod / batching.** Untouched here; still ADR-0027's open optimization, and
  orthogonal to which drivers exist.
