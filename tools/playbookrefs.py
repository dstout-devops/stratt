"""Print every playbook the estate REFERENCES, as `<content-root>\t<relative-path>\t<image>[,<image>…]`.

ANS-013's selection half. `task ansible:syntax` feeds these to a real `ansible-playbook
--syntax-check` inside the EE image; this file decides *which* files that is.

── DERIVED, NEVER A LIST ────────────────────────────────────────────────────────────────────────

A hand-kept list of content roots here would be the second-copy defect `e2e:list` exists to refuse:
someone adds an Actuator, does not add it here, and its playbooks are ungated for months while the
gate reports success. So the roots come from the Actuator declarations themselves — every
`actuators/*.yaml` with `pluginIdentity: ansible` and a `contentDir`.

── AND REFERENCED, NEVER GLOBBED ────────────────────────────────────────────────────────────────

Globbing `*.yml` under a content root would feed vars files, `group_vars/` and role task files to a
playbook parser, which fails for reasons that say nothing about the estate. The estate already
states which files it calls playbooks: a Step names an Actuator, and that Actuator declares
`contentInputs` — the param names whose values are content paths. That is exactly the mechanism
`core/internal/desiredstate/content.go` uses to check those files EXIST; this reads the same
declaration to check they are well-formed.

Reading `contentInputs` rather than looking for a param literally called `playbook` is the same
§1.4 line core holds: the estate says which params are content, not this script.
"""

from __future__ import annotations

import sys
from pathlib import Path
from typing import Any

import yaml

REPO = Path(__file__).resolve().parent.parent

# The platform EE, used when an Actuator names no image of its own — the same default the
# dispatcher falls back to (STRATT_EE_IMAGE).
DEFAULT_EE = "stratt-ee:dev"


def _docs(path: Path) -> list[dict[str, Any]]:
    """Every YAML document in a file that is a mapping. Multi-document is legal here."""
    try:
        return [d for d in yaml.safe_load_all(path.read_text()) if isinstance(d, dict)]
    except yaml.YAMLError as err:
        print(f"playbookrefs: {path}: not valid YAML: {err}", file=sys.stderr)
        raise SystemExit(1) from err


def ansible_actuators() -> dict[str, tuple[str, Path, list[str]]]:
    """name -> (resolved content root, contentInputs) for every ansible Actuator in the tree.

    `contentDir` is relative to the estate root that SHIPPED the Actuator (ADR-0137 D1), which is
    the directory holding `actuators/` — not the repo root and not the CWD.
    """
    out: dict[str, tuple[str, Path, list[str]]] = {}
    for f in REPO.glob("**/actuators/*.yaml"):
        if "dev/declarations" in str(f) or "node_modules" in str(f):
            continue  # a staged build artifact, not source
        for doc in _docs(f):
            if doc.get("pluginIdentity") != "ansible":
                continue
            content_dir = doc.get("contentDir")
            name = doc.get("name")
            if not content_dir or not name:
                continue
            root = (f.parent.parent / content_dir).resolve()
            if root.is_dir():
                # The IMAGE matters: `--syntax-check` resolves module names, so a play using
                # frr.frr must be parsed by the EE that carries it. The Actuator already declares
                # which image runs its content (ADR-0117 D3a) — reading it here means the gate
                # checks each playbook against the EE it will actually run in, not a guess.
                image = doc.get("image") or DEFAULT_EE
                out[name] = (str(image), root, list(doc.get("contentInputs") or []))
    return out


def referenced() -> set[tuple[str, Path, str]]:
    """Every (content root, playbook path) a Step or Trigger actually names."""
    actuators = ansible_actuators()
    found: set[tuple[str, Path, str]] = set()
    for sub in ("workflows", "triggers"):
        for f in REPO.glob(f"**/{sub}/*.yaml"):
            if "dev/declarations" in str(f) or "node_modules" in str(f):
                continue
            for doc in _docs(f):
                steps = doc.get("steps") or [doc]  # a Trigger is one step-shaped mapping
                for step in steps:
                    if not isinstance(step, dict):
                        continue
                    entry = actuators.get(step.get("actuator"))
                    if entry is None:
                        continue
                    image, root, inputs = entry
                    params = step.get("params") or {}
                    for key in inputs:
                        value = params.get(key)
                        if isinstance(value, str) and value:
                            found.add((image, root, value))
    return found


def by_playbook() -> dict[tuple[Path, str], list[str]]:
    """Group the references by playbook, collecting EVERY image that runs it.

    A playbook can be referenced by more than one Actuator, and the estate does this ON PURPOSE:
    `demos/network-device` points both `ansible-network` (the FRR EE) and `ansible-plain` (the
    PLATFORM EE) at `configure.yml`, because the second one exists to be REFUSED — it is the
    negative fixture proving ADR-0117 D3a's image gate fires.

    That matters here because `--syntax-check` answers two questions at once: *is this play
    well-formed* and *does this image carry its modules*. Only the first is ANS-013's. The second is
    the image gate's, it is already live-proven, and an estate is allowed to declare a pair that
    fails it deliberately. So a playbook passes when ANY of its images parses it; it fails only when
    every one does not, which is the shape a genuinely malformed play has.
    """
    out: dict[tuple[Path, str], list[str]] = {}
    for image, root, rel in referenced():
        out.setdefault((root, rel), []).append(image)
    return {k: sorted(v) for k, v in out.items()}


def main() -> None:
    refs = referenced()
    if not refs:
        # A gate that silently checks nothing is worse than no gate: it reports success.
        print(
            "playbookrefs: no playbooks referenced by any Step — either the estate changed shape "
            "or this deriver stopped reaching it. Refusing to report success.",
            file=sys.stderr,
        )
        raise SystemExit(1)
    for (root, rel), images in sorted(by_playbook().items(), key=lambda kv: (str(kv[0][0]), kv[0][1])):
        print(f"{root}\t{rel}\t{','.join(images)}")


if __name__ == "__main__":
    main()
