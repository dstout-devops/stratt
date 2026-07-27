# vspheresim — a vCenter API whose VMs have real guests

A vCenter SOAP API (govmomi's simulator) where each VM is backed by a **real container**,
so a client can create a VM through ordinary vSphere calls and then actually **reach** the
guest.

```sh
task dev:vspheresim:up        # serves :8989
task dev:vspheresim:proof     # create a VM, observe it become reachable, then execute on it
```

## Why it exists

The stock simulator stops at the API. `create-vm`, power, snapshot and migrate all execute
and the inventory is real — but **no guest OS boots**. That is fine for testing a read path
and useless for testing a write path that ends in configuration: a provisioned VM never
reports an address, so nothing can be converged onto it, and "provision then configure"
cannot be exercised on vSphere at all.

govmomi already knows how to back a VM with a container (a VM whose `Config.ExtraConfig`
carries `RUN.container` gets one, and the simulator syncs the container's real IP, hostname
and power state onto `vm.Guest`). What it leaves to its embedder is **deciding that a VM
should have one**. That decision is all this binary adds.

## The design rule

**The simulator decides, never the client.**

If a caller had to ask for a container-backed VM, then declarations that work against
vspheresim would differ from the ones that work against real vCenter, and the simulator
would be testing the wrong thing. A real hypervisor is not told to give a VM a guest OS.
So a client creates an ordinary VM with ordinary vSphere fields and the guest appears —
exactly as it would on hardware.

Everything simulator-specific (which image, which network, which DNS domain) is
configuration **of the simulator**, set by whoever runs it.

## How the guest arrives

A watcher, not an intercept. The container is created inside the simulator's
`applyExtraConfig`, so the backing has to arrive as a `ReconfigVM` — which cannot be done
from inside the registry's own create path without re-entering locks that path holds.
Watching sidesteps that, and the simulator's own code makes it safe: reconfiguring a VM
that is **already powered on** retroactively starts its container.

It is also more honest. A real VM is not reachable the instant its create call returns — it
boots. So the VM exists first with no guest, and the guest arrives a moment later. Anything
consuming a reachability coordinate has to tolerate "built, not yet reachable", and against
this simulator it genuinely does.

## Flags

| Flag               | Env                          | Default                      | Notes                                                                                                                                                                                             |
| ------------------ | ---------------------------- | ---------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `-listen`          | `VSPHERESIM_LISTEN`          | `0.0.0.0:8989`               | Plaintext. A dev simulator; a TLS endpoint here would only teach trust in a meaningless cert.                                                                                                     |
| `-guest-image`     | `VSPHERESIM_GUEST_IMAGE`     | _(empty — backing disabled)_ | **Must run something long-lived.** A bare `alpine` starts, finds nothing to do, exits 0, and the VM then has a backing and no guest. [`guestimage/`](guestimage/) is the one built for this.      |
| `-guest-args`      | `VSPHERESIM_GUEST_ARGS`      | `sleep infinity`             | The guest's command, appended after the image.                                                                                                                                                    |
| `-guest-domain`    | `VSPHERESIM_GUEST_DOMAIN`    | _(empty)_                    | Gives guests `<vm>.<domain>` as hostname, so the guest reports a **dotted FQDN**.                                                                                                                 |
| `-guest-network`   | `VSPHERESIM_GUEST_NETWORK`   | _(empty)_                    | Docker network to attach guests to — how a guest is made reachable from wherever the client runs.                                                                                                 |
| `-guest-mount-dmi` | `VSPHERESIM_GUEST_MOUNT_DMI` | `false`                      | Mounts a synthetic SMBIOS table at `/sys/class/dmi/id`. Off by default: `/sys` is read-only in most container runtimes, and the failure is a container that is created and then refuses to start. |
| `-guest-interval`  | `VSPHERESIM_GUEST_INTERVAL`  | `2s`                         | How often to look for VMs needing a guest.                                                                                                                                                        |

### `-guest-domain` is more load-bearing than it looks

Docker defaults a container's hostname to its own id, which is **not dotted**. A client that
prefers a name and falls back to an address would therefore always take the fallback here,
and the name branch would go permanently untested while looking fine. Setting a domain is
what makes the name path reachable at all.

## The guest image ([`guestimage/`](guestimage/))

Reachable is not converge-able. A container backing gives a VM an address and a
hostname, but a stock base image answers no port — so the coordinate named a host
that could never be logged into, and "provision then configure" still stopped one
step short.

`guestimage/` is the guest OS: **sshd** for the reach path and **python3** for what a
configuration tool needs on the far side of it. It takes the authorized public key as
a build argument and knows nothing else about who is running it.

Two deliberate choices in it:

- **Host keys are generated at first boot, not baked at build.** Baking them would
  give every VM in an estate the same host identity, so host-key verification could
  never tell two machines apart and anything pinning one would be quietly
  meaningless.
- **Arguments are reported and ignored, never exec'd in place of sshd.** The usual
  entrypoint idiom (`[ $# -gt 0 ] && exec "$@"`) is actively harmful here, because
  `-guest-args` defaults to `sleep infinity` — sensible for a bare base image, and a
  silent disabling of sshd for this one. Pass `-guest-args ""` with it; use
  `docker run --entrypoint` to run something else.

## Reaching a guest: a name resolves where a resolver exists

The published coordinate is a **name**, and a name is usable exactly where something
resolves it. In an estate that is DNS. Here it is docker's embedded DNS — which
serves **only user-defined networks**, and only to containers attached to them.

Two consequences, both real rather than incidental:

- `-guest-network` must name a **user-defined** network. On the default bridge the
  guests are reachable by address and the published coordinate resolves nowhere.
- Anything reaching a guest by its coordinate must be **on that network**. The proof
  runs `ssh` from a peer container for exactly this reason; reaching in by address
  from outside would pass while demonstrating the opposite of what is claimed.

`task dev:vspheresim:proof` therefore ends by executing on the guest — `hostname -f`,
`python3 -V`, `id -un` — because a coordinate nothing has ever used is not evidence
that it works.

## Troubleshooting

**A VM has a backing but no address.** Almost always an image that exits immediately. The
simulator says so once per VM:

```
level=WARN msg="guest has a backing but reports no address — the container probably exited;
a guest image must run a long-lived process (check: docker ps -a --filter name=vcsim-)"
```

**Containers are created but never start**, with `error mounting ... "/sys/class/dmi/id":
read-only file system` — that is `-guest-mount-dmi`, which needs a writable `/sys`.

## Extraction

This binary imports **govmomi and the standard library, and nothing from Stratt**. It knows
no Entity, no Facet, no plugin port. That is deliberate on two counts: a simulator is only
trustworthy as a stand-in if it is a property of the _substrate_ rather than of the thing
under test, and the independence makes lifting it into a standalone simulators project a
`git mv` plus a `go.mod`.

An import of anything Stratt-shaped is the signal that a behaviour has leaked from the
system under test into its own test double.

[`guestimage/`](guestimage/) travels with it and holds the same line: the authorized
key arrives as a build argument, and who supplies it is the harness's business.
