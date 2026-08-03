# Configure a network device

**The estate that is not a server.** Everything else Stratt converges has a POSIX shell on the far
end. This target does not: `netops`'s login shell **is** the device CLI, so there is no shell to run
a module in, no python on the device, no writable `/tmp` to stage a script into. Ansible's ordinary
path cannot work here at all — which is precisely what makes it a real test of
`connection.type: network_cli` rather than a decorative one.

**fidelity: `real`.** A real FRR routing daemon with a real `vtysh` CLI, reached over a real SSH
connection, with real cliconf/terminal plugins parsing the terminal. The running-config asserted at
the end is the daemon's own. The concessions: the device is a container in the same kind cluster
rather than a switch on a bench (PLG-1), and FRR stands in for a vendor NOS because cEOS and vJunos
need licences no CI gate can hold.

## Run it (turnkey — one command)

```
task demo:network-device:run
```

From nothing: kind + a minimal Stratt whose desired state IS this demo's estate, the FRR-capable EE
built and loaded, a real device stood up with a throwaway keypair, then the `rtr-configure` Workflow
driven end to end. It asserts four things:

1. the target really is a device — `netops`'s login shell is `/usr/bin/vtysh`, checked, because
   every other claim here is worthless against a target with a shell behind it;
2. the device carries **no observed `mgmt.transport`**, and the Step declares its own reach method —
   nothing provisions a switch, so nothing observes how to reach one (ADR-0156 D5, ADR-0158 D2);
3. the route is in the **device's own running-config**, read back through `vtysh` by the runner
   rather than trusted from the Run's report;
4. a Step whose EE lacks the network content is **refused before it connects**, naming what is
   missing — and the device is verified untouched by it.

Teardown: `task demo:network-device:down`.

## What is actually being demonstrated

- **A connection type is a mechanism, not an enum value.** `connection.type: network_cli` selects
  ansible's netcommon connection plugin; `connection.networkOS: frr.frr.frr` selects the
  cliconf/terminal plugins that parse *this* platform's CLI. Both are required and both are checked
  against the image before anything runs (ADR-0153 D1/D2/D7).
- **The image is the content boundary.** `actuator: ansible-network` selects
  `stratt-ee-frr:dev` — netcommon, `frr.frr`, and the python SSH transport. The
  [`rtr-configure-wrong-ee`](estate/workflows/rtr-configure-wrong-ee.yaml) Workflow is byte-identical
  except for the Actuator, points at the platform EE, and **fails early with the missing content
  named** (ADR-0117 D3a). A boundary that costs nothing to cross is not a boundary.
- **A device is DISCOVERED, not provisioned.** Nothing builds a switch, so no Syncer observes its
  transport. That is exactly the case ADR-0158 D2 makes explicit: an absent `mgmt.transport` means
  the reach method is **unknown**, never "ssh", and the estate states it on the Step instead of the
  shim guessing.
- **A device credential is not a special case.** The key is a brokered `CredentialRef` projected at
  pod spawn, and the shim derives its path from the ref the Step was authorized to use — the same
  §2.5 machinery every other target gets.
- **The assertion reads the device.** A play that reports `changed=true` and a device that has the
  config are different claims. Only the second is about the estate.

## What building it found

**`network_cli` could never have connected, and the image gate passed it.** `ansible.netcommon`
declares neither `ansible-pylibssh` nor `paramiko` as a hard dependency — either will do — so
installing the collection installed **no SSH transport at all**. The collection check passed, and
the run died at connect time with `No module named 'paramiko'`: the exact "names a python module the
estate never wrote" failure ADR-0153 D7 exists to prevent, happening to D7's own connection type.

That became [ADR-0159](../../docs/adr/0159-a-transport-fails-on-three-axes.md): a transport can fail
on three axes — a collection, a control-node binary, a control-node python module — and the gate
checked two.

## By hand

1. **Look at the device.** `kubectl -n stratt exec deploy/rtr-01 -- getent passwd netops` — the
   shell is `/usr/bin/vtysh`. There is nothing else in there to talk to.
2. **Ask it what it is.** `kubectl -n stratt exec deploy/rtr-01 -- vtysh -c 'show version'`.
3. **Look at the Step.** [`rtr-configure.yaml`](estate/workflows/rtr-configure.yaml) — three
   declarations make it a network Step, and nothing above it names a vendor or a transport.
4. **Launch it.** `POST /api/v1/workflows/rtr-configure/runs` with
   `{"inputs":{"routePrefix":"10.99.0.0/24"}}`.
5. **Ask the device again.** `vtysh -c 'show running-config'` — the route is there, and
   `show ip route static` shows the daemon installed it.
6. **Break it on purpose.** Launch `rtr-configure-wrong-ee`. It fails before connecting and says
   which content is missing. Compare that to a python `ImportError` arriving after a live device has
   already been reached.
7. **Make it flap.** POST the same link-flap event nine times:

   ```sh
   for i in $(seq 1 9); do
     curl -sS -X POST localhost:8080/emitters/link-flaps \
       -H 'X-Stratt-Emitter-Token: network-device-demo-not-a-secret' \
       -d "{\"alertname\":\"LinkFlap\",\"device\":\"rtr-01\",\"seq\":$i}"
   done
   ```

   Every one of them matches the Trigger's rule. **One** Run is launched
   ([ADR-0162](../../docs/adr/0162-a-trigger-decides-on-more-than-one-event.md)): the estate asked to
   be told about storms, not flaps, and it said so with a window and a threshold rather than with
   code. `GET /api/v1/workflow-runs` is the honest place to count.
8. **Send the same storm as ONE batched report**, in a payload shape nobody wrote Go for:

   ```sh
   curl -sS -X POST localhost:8080/emitters/nms-batch \
     -H 'X-Stratt-Emitter-Token: network-device-demo-not-a-secret' \
     -d '{"status":"open","report":{"site":"lab-1","linkEvents":[
           {"kind":"link.flap","port":"ge-0/0/1","status":"down"},
           {"kind":"link.flap","port":"ge-0/0/2","status":"down"},
           {"kind":"link.flap","port":"ge-0/0/3","status":"down"},
           {"kind":"link.flap","port":"ge-0/0/4","status":"down"},
           {"kind":"link.flap","port":"ge-0/0/5","status":"down"}]}}'
   ```

   One POST, five events, one Run
   ([ADR-0163](../../docs/adr/0163-one-post-many-events-and-the-shape-is-not-cores.md)). Read
   [`emitters/nms-batch.yaml`](estate/emitters/nms-batch.yaml): it says where the items are and which
   envelope fields to fold in, and that is the whole of what made this shape work. Note
   `status` appears at both levels, so the envelope's is merged as `batchStatus` — a collision Stratt
   refuses to resolve on your behalf rather than silently picking a winner.

## What you just learned

You converged a class of estate that configuration-management tools usually treat as a separate
product: **network devices**, through the same Workflow model, the same authorization, the same
credential brokering and the same descent as every server. The Step names a connection type and a
platform; nothing above it knows either.

And the estate reacted to the device on its own terms: nine flap events, one remediation. The
judgement that "five inside ten minutes is an incident and one is noise" is a declaration a reviewer
can read in Git, not a rule buried in an engine — and the count behind it lives in Postgres, so it
survives a restart and holds across replicas.

You also pointed a source at Stratt whose payload shape nothing in the control plane had ever seen,
and it worked because an estate declared where to look. That is the difference between a platform
that supports your alerting stack and one that ships a list of the stacks it supports.
