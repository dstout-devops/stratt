# `plugins/ansible/estate/` — what this plugin PROPOSES

The ansible plugin's own declarations, shipped and versioned with the binary that executes them
([ADR-0137](../../../docs/adr/0137-a-plugin-is-a-service-not-a-subdirectory.md) D1). Before this, they
were scattered across `estate/actuators/`, `estate/workflows/`, `estate/triggers/` and
`estate/ansible/projects/` — four directories in a tree the plugin does not own. At the scale this
platform is aimed at, that is the end of the road: every plugin author edits core, and no plugin can be
reviewed or tested as a unit.

| dir          | what                                                                              |
| ------------ | --------------------------------------------------------------------------------- |
| `actuators/` | the ansible Actuator, one per content **project** (ADR-0134 D1)                   |
| `workflows/` | the Workflows those Actuators execute                                             |
| `triggers/`  | the schedules that launch them                                                    |
| `content/`   | the tool content itself — plays, roles, group_vars — one subdirectory per project |

## This is a proposal, not an installation

**Locality is not authority** (ADR-0137 D3). Everything here is a _proposal with defaults_; the adopting
estate decides what it admits and runs, by naming this directory in its `plugins.yaml`. That boundary is
not stylistic: an Actuator declaration carries `facetNamespaces`, a **write ceiling**, so a plugin that
installed itself would be a vendor granting itself authority. Helm's chart-defaults /
operator-overrides shape, and the review is real because the admission is a diff.

The write ceiling is also why these files are **platform-reviewed**, not tenant-editable — see the
honest-limit note in [`actuators/ansible-platform-baseline.yaml`](actuators/ansible-platform-baseline.yaml):
authorization on Actuator _selection_ does not exist yet, so this boundary is enforced by repo review
and must not be described as isolation.

## `contentDir` resolves against THIS root

An Actuator's `contentDir` (e.g. `content/platform-baseline`) is relative to the estate that **shipped**
it — this one — not to the estate that admitted it. That is what lets the content travel with the plugin
(ADR-0134 D3, ADR-0137 D1), and it is guarded by
`TestPluginContentResolvesAgainstItsOwnEstate`.

Note the directory is `content/`, not the old `ansible/projects/`: inside a directory already called
`ansible`, naming a subdirectory `ansible` again buys nothing. The demo estates keep the tool-namespaced
form because their roots are shared with other plugins.

## Developing this plugin does not touch any of it

ADR-0137 D2's acid test holds here: the plugin builds, tests and conformance-checks entirely inside
`plugins/ansible/`. [`../conformance_test.go`](../conformance_test.go) drives the real
`cmd/stratt-ansible` through `sdk/mockstratt` with no estate at all — no cluster, no Postgres, no
Temporal, and no ansible installed. This directory is about **deploying** the plugin, which is a
different act with a different reviewer.
