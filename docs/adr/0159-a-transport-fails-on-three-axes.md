# ADR 0159 — A transport fails on three axes, and the image gate checks two

- **Status:** **Accepted** (2026-08-03, steward) — **live-proven**: the negative case is a real EE
  built from this repo's own Dockerfile without `EE_PYTHON_EXTRA`, and the shim refuses it before the
  run. See Verification. Charter review by hand — this session's rules bar
  the subagent; §1.5/§1.7/§1.8 answered inline. **No new runtime dependency.**
- **Date:** 2026-08-03
- **Deciders:** steward
- **Charter sections:** §1.5 (schemas pinned and verified, drift blocking), §1.7 (evergreen),
  §1.8 (the abstraction must never hide diagnosis)
- **Extends ADR-0153 D7** (the collection axis) and **ADR-0156 D6** (the control-node binary axis)
  with the third axis both left uncovered. **Keeps ADR-0117 D3's** image-is-the-content-boundary
  rule and **D7's refusal to probe**. Emits its refusal per **ADR-0157**'s finding.

## Context

An ansible connection plugin can fail for three independent reasons, and the image gate grew one
axis at a time as each was paid for:

| Axis | Needed by | Checked since |
| --- | --- | --- |
| A **collection** | `network_cli`, `netconf`, `kubectl`, `vmware_tools`, `aws_ssm` | ADR-0153 D7 |
| A **binary on the control node** | `kubectl`, `session-manager-plugin` | ADR-0156 D6 |
| A **python module on the control node** | **`network_cli`, `netconf`** | **nothing** |

The third was found on 2026-08-03, driving a real FRR device for the first time. With
`ansible.netcommon` installed and the collection check passing, the run died at connect time:

```
[WARNING]: ansible-pylibssh not installed, falling back to paramiko
[ERROR]: paramiko is not installed: No module named 'paramiko'
```

**That is D7's own failure, happening to D7's own connection type.** D7 exists to prevent "a
declaration that passes review and dies at connect time naming a python module the estate never
wrote", and this is precisely that sentence, one axis to the side of where D7 was looking.

**Why it was invisible.** `ansible.netcommon` declares NEITHER `ansible-pylibssh` NOR `paramiko` as
a hard dependency, because either will do. So installing the collection installs no SSH transport at
all, and every check that reasons about collections reports a complete image. `ee/Dockerfile` now
installs pylibssh into the network variant (`EE_PYTHON_EXTRA`), which fixes the INSTANCE. The class
is open: the next transport with a python-side dependency fails the same way, at the same moment,
with the same unhelpful error.

### Why not simply probe the image

D7 already answered this and the answer holds. It MEASURED `ansible-doc`'s exit codes as useless for
the question — `ansible-doc -t connection network_cli` warns and still exits 0 — and chose to read
the image's own content manifest instead. A python probe (`python -c "import pylibsshext"`) would
reintroduce exactly what D7 removed: a subprocess whose failure modes are not the question being
asked, running per Run, in the pod, on the critical path of every converge.

The manifest is the seam. It is already written at build time, already read by the shim, already the
thing an operator can `cat` to see what a Run executed. It should describe the image's python
content for the same reason it describes its collections.

## Decision

### D1 — the EE content manifest records the image's PYTHON DISTRIBUTIONS

`ee/content.py install` already writes `/etc/stratt/ee-content.json` with `collections` and `roles`.
It gains `python`: every installed distribution, name and version, from `importlib.metadata`.

**Everything installed, not just the variant's extras.** The alternative — record only what
`EE_PYTHON_EXTRA` added — was rejected twice over. It would require threading the build arg into
content.py, making the manifest a restatement of a declaration rather than an observation of the
image (the §1.2 distinction, applied to an image instead of a host). And it would MISS the case that
matters most: an image whose python library arrives as a transitive dependency of something else is
a working image, and a manifest that only knows about deliberate additions would refuse it.

The manifest stays an observation of what is present. That is what makes it checkable.

### D2 — a python requirement is ANY-OF, never a single named module

`network_cli` needs `ansible-pylibssh` **or** `paramiko`. A check demanding one specific module
would refuse an image that works, which is worse than the gap it closes: a false refusal teaches
operators that the gate is wrong, and a gate people route around protects nothing.

So the requirement is a SET, satisfied by any member, and the diagnosis names the whole set and says
either will do. This is the same shape ADR-0156 D6's binary axis would need if a transport ever
accepted alternatives; it does not today, and this ADR does not generalise it there speculatively.

### D3 — the refusal happens before the run, and emits a terminal

Like the other two axes: refuse at validation time, before `ansible-runner` is spawned, naming the
connection type, the missing set, and the fix (`EE_PYTHON_EXTRA` / build a variant and select it from
the Actuator declaration, ADR-0117 D3).

It emits a TERMINAL event rather than returning a bare error — ADR-0157 found that a returned error
exits the process and leaves the Run's `error` field null, so the refusal reaches no surface an
operator reads. All four reach refusals in the shim now emit terminals; this is the fifth and follows
them rather than reintroducing the defect.

### D4 — the floor stays bounded; python extras are per-variant, in the Dockerfile

**This reconciles with a rule already shipped rather than inventing one.** `ee/content.py` REFUSES
to carry over an `execution-environment.yml`'s `dependencies.python` / `dependencies.system`
sections, with the reason recorded in the refusal itself: "python/system packages belong in
ee/Dockerfile, where the layer they land in is reviewable". `EE_PYTHON_EXTRA` is a build arg on that
Dockerfile, so a variant's python content lands exactly where that rule says it should — and the
manifest then OBSERVES the result rather than the requirements file DECLARING it. Declaration and
observation stay on opposite sides of the seam, which is the whole reason the manifest is checkable.

An earlier draft of this ADR considered teaching the requirements file a python section. It would
have contradicted that rule head-on and moved python content into a layer whose review story the
existing refusal specifically rejects.


`EE_PYTHON_EXTRA` is a build arg, not a floor addition. The platform EE speaks to no network device,
and ADR-0117 D3's rule is that the floor carries what the platform's OWN content needs. An adopter
adding `cisco.ios` adds its python needs the same way.

### D5 — what this does NOT decide: a python-side lockfile

Collections are hash-locked (`ADR-0117` follow-up i): the pin bounds the version, the lockfile bounds
the bytes. Python extras are pinned by VERSION ONLY, so a republished wheel at the same version
changes the image and nothing says so. That asymmetry is real and is recorded in `ee/Dockerfile`
rather than tolerated silently.

It is not closed here because the honest fix is `uv`'s own hash-locking over a per-variant
requirements file, which changes how variants are declared — a bigger change than the axis this ADR
is about, and one that should not ride along inside it. **Booked, with the gap named.**

## Consequences

- **The manifest grows a section.** Readers that parse it must tolerate unknown top-level keys; the
  shim's reader already decodes only the fields it wants, so it is unaffected.
- **Every EE rebuild records its python content**, which makes "what did this Run execute?" answerable
  for the python half for the first time — an §1.8 gain independent of this check.
- **A pre-existing image without the new manifest section fails the check.** This is deliberate: the
  same rule D7 applies to an unreadable manifest ("an EE built outside our pipeline publishes no
  manifest, so what it contains is unknown rather than adequate"). Unknown is not adequate.
- **This closes the class, not just `network_cli`.** `netconf` has the same need and gets the same
  check from the same table.

## Verification

Not shippable on assertion, and this arc has now paid for that rule five times. This ADR owes:

- a unit test that a connection type whose python set is absent from the manifest is REFUSED, and
  that ANY member of the set satisfies it — falsified by removing the check;
- a **live proof**: an EE carrying the collection and NOT the python module must be refused BEFORE
  the run, where today it reaches ansible and returns `No module named 'paramiko'`. Both images are
  buildable from the same Dockerfile with and without `EE_PYTHON_EXTRA`, so the negative case is
  cheap and real rather than simulated.

### Both owed items, paid (2026-08-03)

**Unit**, in `plugins/ansible/connection_type_test.go`: the collection-present/python-absent image is
refused; EITHER library satisfies it; three PEP 503 spellings of the same distribution all satisfy
it; a pre-ADR-0159 manifest with no python section is refused as "cannot say" rather than assumed
adequate; and `ssh` still consults the manifest for nothing at all. **Falsified** by deleting the
call site — two of them fail, and nothing else in the package does.

**Live**, driving the real `stratt-ansible` binary inside two real images built from this Dockerfile,
differing only by `EE_PYTHON_EXTRA`. The negative image's manifest confirms the shape first —
`netcommon present: True, pylibssh present: False` — and then:

```
$ docker run … stratt-ee-network:nopython /usr/local/bin/stratt-ansible
{"event":{"level":"LEVEL_ERROR","terminal":true,"message":
 "connection.type network_cli cannot open a connection in this EE: it needs one of
  ansible-pylibssh or paramiko on the CONTROL NODE and has neither. The collection
  (ansible.netcommon) is present and is NOT enough — … Build an EE variant with
  `--build-arg EE_PYTHON_EXTRA=…` … Without this the Run does not refuse — it reaches
  ansible and dies with `No module named 'paramiko'` …"}}
```

and the shipped variant proceeds exactly as before, rendering `ansible_connection=network_cli` /
`ansible_network_os=frr.frr.frr` and starting the play. `terminal:true` on the refusal is D3
holding: ADR-0157 found that a returned error leaves the Run's own `error` field null, and this
refusal reaches the surfaces an operator reads.
