# ADR 0148 — One Blueprint per application, and an observation that can come back wrong

- **Status:** **Proposed** (2026-07-29, steward) — implemented and **live-proven across five nodes,
  with each decision separately falsified**. Charter review by hand (this session's rules bar the
  subagent); §1.1/§1.2/§1.5/§1.8/§2.4/§9 answered inline. **No new Named Kind, no new dependency, no
  migration.** One additive Blueprint field (`delivers`), one load-time check, one shared content
  role.
- **Date:** 2026-07-29
- **Deciders:** steward
- **Charter sections:** §1.1 (type the seams — the estate declares desired state, never a distro
  ontology), §1.8 (**the abstraction must never hide diagnosis** — D4 is this section applied to a
  fact-back), §2.4 (exclusive claims are refused, never merged or tie-broken; a capability with two
  providers fails closed), §9 (no ontology creep — a per-distro package map in an Intent is the
  thing this refuses), §1.2 (only a Run's provenance writes what it observed), §1.5 (core stays
  content-blind: it never parses a play to learn what a variable means)
- **Reconciles with:** **ADR-0083** (the Blueprint route is the sole tool-materialization seam — D1
  is that rule followed to its consequence), **ADR-0135 D3** + **ADR-0138 D5** (a route may name a
  capability; dial-less providers are verifiable — D7 is their only live consumer and states its
  limit), **ADR-0118 D1** (co-equal declarations are a compile error, and the env-keyed-values shape
  D3 refuses one axis over), **ADR-0117 D3/D3a/D6** (content is build-time, selected by Actuator;
  core never parses content to discover parameters), **ADR-0134** (tool content lives beside the
  estate), **ADR-0054** (`facetWriteScope`, intersected with the Actuator's ceiling — D5 corrects
  what was claimed about where that ceiling lives), **ADR-0080** (the software dimension's shared
  component shape, now emitted in one place), **ADR-0084** (the `stratt_facets` fact-back
  convention), **ADR-0055/0083** (`app.config` and the onboarding Baseline that demanded it),
  **ADR-0149** (the EE content floor, without which none of this content could install a package on
  Alpine), **ADR-0060** (`graph.facet_owner` — many owners, at most one authoritative)

## Context

`Intent/Application` shipped carrying `package: nginx`, with exactly one content root behind it whose
whole job was to adjust a listen port on a node that already ran nginx. The task was to add Apache,
then Tomcat, **against the same estate shape** — because the second application is the only thing
that proves the intent layer abstracts the tool rather than describing the one tool it was written
for.

It did not survive contact. Every decision below is something the second and third application
forced, and four of them are corrections of a claim the first application had made look true.

The single most useful fact about this work: **the estate LOADED at every stage.** Valid YAML, every
content ref resolving, the playvars guard green, `task ci` green — while apache could not install on
Alpine, could not configure on Debian or RHEL, and Tomcat had never once started. What found all of
it was executing the content ([`task dev:content:proof`](../../Taskfile.yml)), which is now five
nodes: apache on Alpine/Debian/RedHat and tomcat on Debian/RedHat.

## Decision

### D1 — One Blueprint per delivered application, because content selection cannot be data

This is a **derivation, not a preference**, and it is worth writing down because the conclusion looks
like duplication and is not:

1. A templated content reference is **refused at estate load** (`validateContentRefs`) — a
   `playbook:` whose value is `{{.spec.package}}` never parses. Content selection cannot be data.
2. So one Step runs one playbook.
3. So one Workflow converges one application.
4. So one Blueprint route names one Workflow, and one Blueprint delivers one application.
5. And one Actuator per application, because `facetNamespaces` is the write ceiling and it is
   declared per Actuator (see D5).

The alternative — one `web-server` Blueprint that picks its play from the Intent — is not a tidier
version of this. It is the thing step 1 refuses, and step 1 exists because executable content chosen
at run time by estate data has no pinned identity: §7.3's supply-chain claim and ADR-0117 D3's "the
image is the content boundary" both dissolve if a Run can be pointed at arbitrary content by a
string in a Git file.

### D2 — The Intent states the application, the Blueprint declares what it delivers, and disagreement is refused at load

`Blueprint.delivers` names the application a Blueprint installs. `checkBlueprintDelivers` refuses any
Assignment binding an `Intent/Application` whose `spec.package` names a different one.

Both halves are load-bearing:

- **The Intent states it** because an Intent must be readable on its own. `package: tomcat` tells an
  operator what will run without chasing an Assignment to a Blueprint to a Workflow to a content
  root.
- **The Blueprint declares it** because the Blueprint is what actually installs it, and D1 means that
  is a fixed property of the Blueprint rather than a parameter.

Neither is derivable from the other, which is exactly why the **agreement** is what gets checked
rather than one being inferred. And it is checked **at load**: the alternative is an operator
discovering at a gate, on a live floor, that the tomcat Intent they approved is about to converge
nginx — a failure moved to the far side of an approval, which §1.8 treats as the serious kind.

> **Naming.** An earlier revision routed this through a `web-app` content root with a "which tech"
> setting. Abandoned on review: "web app" names a _service_, not the technology under it, and the
> setting was the data-selected content D1 refuses. The vocabulary matters here — `delivers` names an
> outcome, which is ADR-0083's language, not a tool config.

### D3 — Desired state belongs to the estate; the TARGET'S LAYOUT belongs to content

The Intent declares a **port**. Everything else is a fact about the machine:

| Fact                                   | Alpine                | Debian                      | RHEL                                          |
| -------------------------------------- | --------------------- | --------------------------- | --------------------------------------------- |
| apache package                         | `apache2`             | `apache2`                   | `httpd`                                       |
| apache include dir                     | `/etc/apache2/conf.d` | `/etc/apache2/conf-enabled` | `/etc/httpd/conf.d`                           |
| the file holding the stock `Listen 80` | `httpd.conf`          | `ports.conf`                | `conf/httpd.conf`                             |
| the control binary                     | `httpd`               | `apache2ctl`                | `httpd`                                       |
| tomcat package                         | —                     | `tomcat10` (Tomcat 10)      | `tomcat` (Tomcat **9**)                       |
| `CATALINA_BASE` vs `CATALINA_HOME`     | —                     | **different directories**   | one directory                                 |
| the tomcat **launcher**                | —                     | `catalina.sh` (daemonizes)  | `/usr/libexec/tomcat/server` (**foreground**) |

All of it is read from `ansible_facts.os_family` into `vars/<app>/<family>.yml`, and **an unknown
family is refused rather than defaulted**. That last clause is the decision, not a detail: the play
that shipped resolved the _package name_ per family and then fell through to Alpine's paths for
everyone, which is why Debian and RHEL wrote configuration nothing reads and said nothing. Defaulting
to one family's layout makes a silently-wrong path indistinguishable from a correct one until someone
connects to the port.

**Why not a per-distro map in `Intent/Application`.** It makes core learn distro packaging — the
ontology creep §1.1 and §9 forbid — and it is precisely the env-keyed-values shape ADR-0118 D1
refuses, rotated one axis: a value whose meaning depends on a key the estate cannot validate. Ansible
already has facts and a package abstraction; §1.4 says let the tool know what the tool knows.

**The other half of the rule is that desired state must actually REACH the converge.** The port is
resolved once per Assignment — from the Blueprint default or the Intent's override, never both
(ADR-0118 D1) — and the same resolved value feeds the expectation the Baseline checks AND the
`remediationParams` the converge runs with. An expectation and a converge disagreeing about the
desired port would make drift permanent and invisible;
`TestRemediationParamsCarryEachAssignmentsResolvedSpec` pins that they cannot.

**The rule generalises past paths, which is what executing RHEL taught.** The _launcher_ is a layout
fact too. A play hard-coding `catalina.sh` is not a play whose paths happen to be Debian's — it is a
play that only works on Debian, and no amount of path-fixing reaches that.

### D4 — An observation must be able to come back wrong

The most important decision here, and the one that had been most confidently gotten wrong.

All three plays ended by greping the file they had written two tasks earlier, under a comment
claiming they read "the effective listen port from the running config." It was an **echo**: it could
only ever return the desired value. So it returned it for hosts where apache had never started and
for a Tomcat whose JVM had exited immediately — the drift Finding **resolved**, and the estate
believed a dead service was converged. The comment directly above that code read _"a play that wrote
the right file to a dead service would report success (§1.8)."_

Three properties are now required of a converge's fact-back:

1. **It reads the running system.** The port must accept a connection (`wait_for`), what answers must
   speak HTTP (`uri`), and it must identify as the service the Blueprint claims (the `Server` header).
2. **It fails the Run rather than reporting.** A converge that did not converge is a **failure, not a
   fact**. `failed_when: false` is gone from the config and start paths; the one remaining swallow is
   `pkill`'s (it exits 1 when nothing matched, the normal first-run case), and the start is
   **detached** rather than swallowed — with `wait_for` as the gate, which is strictly stronger than a
   launcher's exit code, since a launcher can exit 0 and leave the JVM dead.
3. **An inapplicable check must be DECLARED, not omitted.** Tomcat sends no `Server` header, so
   `svc_server_token` is required and set to `""`; nginx has no per-family layout, so
   `svc_layout_dir` is required and set to `""`. "Does not apply here" and "nobody thought about it"
   must not look the same to the next author.

And the **test** does not trust the play either: `servesHTTP` probes each converged node from a third
container sharing no code, filesystem or variables with the converge. Reading only the play's own
report is still taking the play's word for it.

### D5 — The shared skeleton is a role over one content root, and the write ceiling is the ACTUATOR'S

Three plays across three families showed the shared parts are a **prefix and a suffix, not a
wrapper**: guard the family, load the layout, install — then observe the running service and report.
The middle is never shared (a conf.d drop-in and `httpd -k restart`; an XML Connector and a
catalina.sh; a server block and a reload). A role owning the middle would need a hook, and a hook is
how a shared role becomes harder to read than the duplication it replaced. So
`roles/stratt_web_service` owns the ends and each play keeps its middle.

**The reason is correctness, not brevity.** ANS-014 was one defect in three copies of the observe
tail and had to be fixed three times. The next application added will copy whichever play its author
finds first; with the ends in a role, what it copies is the fixed version by construction. The same
argument makes ADR-0080's component shape identical across applications by construction rather than
by three authors remembering it.

> **CORRECTION.** `ansible-tomcat.yaml` claimed the per-project `facetNamespaces` ceiling was carried
> by the **directory**, so that merging two applications' content roots would merge their write
> ceilings. It is not: `facetNamespaces` is a field on the **Actuator** declaration, intersected with
> the Step's `facetWriteScope` per Actuator. Three Actuators over one content root keep three
> distinct ceilings — which is what permits `content/{apache,tomcat,nginx}` to become
> `content/webapp` and share the role. What the split actually bought was **file-level** least
> authority (an apache pod mounted only apache's code); that is real but weaker, and the residual is
> stated in the Actuators: an apache Run now mounts tomcat's play, and this Actuator would accept
> `playbook: tomcat-configure.yml`. That widens the surface without escalating authority, and it is
> the ordinary AWX shape of many job templates over one project.

**Factoring must not be an exit from a check.** `TestNoUnboundPlayVariables` read only the top-level
play, so moving the observe tail into a role would have moved the riskiest code out of its sight. It
now follows `include_role`/`import_role` into the role's task files, treats a role's `defaults/` as
bindings, and scopes `include_vars` by the `vars/<dir>` literals a play names. Verified before being
relied on: nested content paths survive the ConfigMap round-trip (`cmKey` flattens with `__`; one
VolumeMount per file restores `/runner/project/<real/path>`), so `roles/` and `vars/<app>/` work
in-cluster and not only under the proof's bind mount.

### D6 — Two managed applications on one host is REFUSED, and that is a real product limit

`app.config` is claimed **exclusive**, so two Assignments converging one Entity's `app.config` is a
compile error (§2.4 — a double-claim is refused, never merged or tie-broken). Correct, and it means:
**one host, one Stratt-managed web application.** Each application therefore gets its own View and
its own fixture host.

Stated as a decision rather than left to be discovered, because it is a limit an adopter will hit
(a host running apache on 80 and tomcat on 8080 is ordinary) and because the fix is a real design
question, not a config: `app.config` carries exactly `port` (a CLOSED schema, §9), so distinguishing
two applications on one host needs a per-application key in the claim — which is a claim-model
change and belongs in its own ADR, not smuggled in here.

### D7 — The capability route keeps exactly one live consumer, and its limit is written down

`ansible-nginx` alone declares `provides: [configmgmt]` with `remediates: {Application: …}`; apache
and tomcat name their Workflows directly. The asymmetry is deliberate, and both halves have reasons:

- **Exactly one provider**, because capability resolution is keyed on (class, intentKind,
  environment) and a second provider makes every capability-routed remediation for the kind
  ambiguous and **fail closed** (§2.4). Adding apache as a second `configmgmt` provider would break
  the nginx Blueprint to add an unrelated one.
- **One live consumer**, because the capability-routed path (ADR-0135 D3 / ADR-0138 D5) broke every
  real floor once and was fixed; the only thing that catches a regression in it is an estate that
  actually uses it, gated by `task dev:connector-e2e`. Moving all three applications to named
  Workflows would have left the mechanism correct, tested by fakes, and unexercised.

**The tension, stated.** `configmgmt for Intent/Application` resolving to a provider means the CLASS
answers "how do I converge an application" — and it cannot, because each project converges exactly
one, so the honest answer is "nginx's". That was invisible while nginx was the only application. What
makes it safe now is **D2**: binding a tomcat Intent to the nginx-delivering Blueprint is refused at
load, so the ambiguity cannot reach a Run. The rule the estate follows is ADR-0106 D1's, one level
up: **name the capability when more than one provider could legitimately serve; name the Workflow
when the estate has decided.** Per delivery form, the estate has decided.

## Alternatives considered

- **One `web-server` Blueprint parameterised by application.** Refused by D1's step 1 at load. Not a
  style choice — data-selected executable content has no pinned identity.
- **A per-distro package/path map in `Intent/Application`.** Rejected: ontology creep (§1.1/§9) and
  ADR-0118 D1's env-keyed-values shape rotated one axis. Content already has facts.
- **Infer the delivered application from the Blueprint's Workflow, or from the Intent alone.**
  Rejected both directions: an Intent that cannot be read on its own fails §1.8's descent, and a
  Blueprint whose delivered application is implicit cannot be checked against the Intent at all —
  there would be nothing to disagree.
- **Merge the three plays into one content root as one Actuator.** That _would_ have merged the write
  ceilings (one Actuator, one `facetNamespaces`), which is the version of the merge that is genuinely
  weaker. Three Actuators over one root is not that, per D5.
- **A role that owns the whole converge with a hook for the middle.** Rejected: inverted control for
  three lines of shared prefix, and less readable than what it replaces.
- **Keep `failed_when: false` and assert in the test instead.** Rejected: it makes every Run
  self-reporting success the normal case and leaves the estate's Findings wrong for anyone not
  running the test. The Run is the thing that must fail.

## Charter alignment

- **§1.1 / §9.** The estate types one seam — a port, in a CLOSED `app.config` — and learns nothing
  about distro packaging. `delivers` is a single string naming an outcome, not an application
  ontology.
- **§1.8.** D4 is this section applied to the fact-back, which is where it had been violated while
  quoting itself. D2 moves a failure from a live floor to estate load.
- **§2.4.** The exclusive claim on `app.config` is enforced as a compile error (D6); a second
  `configmgmt` provider fails closed rather than being tie-broken (D7).
- **§1.5 / §1.2.** Core never parses a play; the launch interface carries desired state only, and
  what the graph learns is what a Run observed, with provenance.
- **§1.4.** Distro resolution is left to ansible, which already does it. No new mechanism: one
  additive field, one check, one role.

## Consequences

- **Positive.** Three applications converge from Intents that name no package manager, no path and no
  launcher, across three distro families, all executed. A fourth application is a Blueprint, a
  Workflow, an Actuator, a play middle and a matrix entry — with the risky ends inherited rather than
  re-authored. Binding the wrong Blueprint to an Intent is a load error.
- **Negative / accepted.** One managed web application per host (D6). Per-application Blueprint and
  Actuator counts grow linearly — the cost of D1's derivation, paid deliberately. An apache Run mounts
  the other applications' plays (D5's residual).
- **Neutral.** `delivers` is optional and absent Blueprints are unaffected, so nothing pre-existing
  changes behaviour.

## Verification — the evidence, and what each decision was falsified by

- **`task dev:content:proof`: five nodes green (2026-07-29).** apache installs and serves on
  `alpine:3.22` (`Apache/2.4.68 (Unix)`), `debian:12-slim` (`Apache/2.4.68 (Debian)`) and
  `rockylinux:9` (`403`, a healthy no-index answer); tomcat on `debian:12-slim` (`200`, Tomcat 10) and
  `rockylinux:9` (`404`, Tomcat 9 with no ROOT webapp). **Every node is asserted to start WITHOUT its
  package**, so a green run means the remediation installed it; and every node is probed
  **out-of-band** from a third container after the converge.
- **D3 falsified:** restoring the Alpine hard-code in `vars/apache/RedHat.yml` fails the run at
  `Path /etc/apache2/httpd.conf does not exist` — loudly, where it used to be swallowed.
- **D4 falsified:** dropping `svc_server_token` from the tomcat play fails with the role's own
  diagnostic naming the fix; and the earlier echo-observation is what let three separate defects
  (`pkill` signalling its own shell, `CATALINA_BASE` pointed at `CATALINA_HOME`, a foreground
  launcher) each report success.
- **D5 falsified:** a typo introduced inside `roles/stratt_web_service/tasks/observe.yml` is now
  reported by `TestNoUnboundPlayVariables` against all three Workflows that include it; before the
  guard followed `include_role`, against none.
- **D2 pinned:** `TestBlueprintDeliversMustAgreeWithTheIntentsPackage` — an Intent asking for one
  application, bound to a Blueprint that installs another, is refused at load
  (`checkBlueprintDelivers`).
- **D3 pinned:** `TestRemediationParamsCarryEachAssignmentsResolvedSpec` — the converge receives the
  same per-Assignment value the expectation is checked against.
- **`task ci` green** at every commit in the arc.

## Follow-ups

- **(a) nginx is the weakest leg and is now the only unexecuted one.** It runs on Alpine only, on a
  node that already ships nginx — so its `svc_install: false` path is the only one exercised, and two
  of the three families it declares have never run. It is also the D7 capability consumer, which
  makes it the leg with the most claimed and the least executed. A bare node plus two matrix entries.
- **(b) The CHART delivery form.** ADR-0080 gives `software.package` / `software.container` /
  `software.chart` one component shape and one advisory pass. Only the package form has a
  write-owner here; a Blueprint routing an `Intent/Application` to `helm/deploy` is what shows D1–D4
  are about delivery forms rather than about Ansible.
- **(c) Co-hosting (D6).** Needs a per-application key in the `app.config` claim, which is a
  claim-model change and owes its own ADR.
- **(d) A collection-shaped content root (ANS-007).** The role landed as a project-relative `roles/`
  tree, which works and is not the idiomatic ansible packaging. Recognising a `galaxy.yml` root is
  the real ANS-007, and D5's shared root is the step that makes it worth doing rather than a
  prerequisite for it.
- **(e) The `install?` conditional deserves a second estate, not a second application.** `svc_install`
  is currently false for exactly one play, on the one node that pre-ships its package. The claim it
  encodes — that the same content installs onto a bare node and adjusts a pre-built image — is
  therefore only half executed.
