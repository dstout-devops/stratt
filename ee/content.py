#!/usr/bin/env python3
"""Ansible EE content: pin verification, build-time installation, and a run-visible manifest.

ADR-0117 D3. Content (collections AND roles) is declared where it is RESOLVED — in the
EE build — never as a field on the run-time `ansible.input` Contract. A Step selects
content by selecting its Actuator's EE image (D3a), so the image is the content
boundary. Digest-pinning that reference in production is the operator's job (§7.3).

Why build-time (charter forces, not preference):
  * §7.3 supply chain — a run pod fetching content from the network at execution time
    has no provenance, no SBOM, and no reproducibility. Baked + digest-pinned does.
  * PLG-1 / §1.2 — Galaxy or a private Automation Hub is an EXTERNAL, operator-owned
    system. A Run must not fail because someone else's registry is having a bad day.
  * Air-gap — enterprises run disconnected. Build-time makes that a seeding problem
    (solvable) rather than a runtime problem (not).

This script lives in the EE image (charter §3 — Python belongs in execution pods, not
the control plane) and is also the CI pin gate. Both entry points share `verify()`, so they
cannot disagree about what "pinned" means — but they are NOT equivalent: `task ci` checks
every ee/content/*.requirements.yml, while a build sees only the one file its EE_CONTENT
names and additionally runs the post-install resolved-set assertion. CI green therefore does
not guarantee a build will pass; the build is the stronger gate.

The declaration format is the REAL Galaxy `requirements.yml` — the same file
`ansible-galaxy` and `ansible-builder` consume. Stratt invents no content language
(§1.1): ansible-builder EE compatibility is a charter commitment, and a bespoke format
would break it.

Install target is `/usr/share/ansible/{collections,roles}`, which is on ansible's
DEFAULT search path — verified live: ansible-runner adds its own project dirs WITHOUT
replacing the defaults, so build-time content needs no ANSIBLE_ROLES_PATH,
no ANSIBLE_COLLECTIONS_PATH, and no ansible.cfg. Consequently an INLINE play can use a
build-time role, which the shim's single-play.yml layout cannot do for a repo-local one.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import urllib.parse
from pathlib import Path
from typing import Any

import yaml

# An exact pin is asserted POSITIVELY, never by denying a list of bad tokens. A denylist
# looked sufficient and was not: `devel`, `HEAD`, `dev`, `stable-2.19`, `2.22` and `1.0`
# all contain no forbidden character, so every one of them passed as "exactly pinned"
# while naming a moving git ref or an incomplete version. That matters most for ROLES,
# which install from git tarballs where a branch name is a legal `version:`.
_SEMVER = re.compile(r"^\d+\.\d+\.\d+([-+][0-9A-Za-z.\-+]+)?$")  # 1.2.3, 1.2.3-rc1, 1.2.3+meta
_GIT_SHA = re.compile(r"^[0-9a-f]{40}$")  # a full commit id — immutable, unlike a tag

MANIFEST_PATH = "/etc/stratt/ee-content.json"


class PinError(Exception):
    """A declaration that would produce an unreproducible image."""


def _load(path: Path) -> dict[str, Any]:
    with path.open() as fh:
        doc = yaml.safe_load(fh) or {}
    if not isinstance(doc, dict):
        raise PinError(f"{path}: a requirements file must be a mapping with collections:/roles: keys")
    unknown = set(doc) - {"collections", "roles"}
    if unknown:
        # Strict, like every other Stratt boundary file: an unknown key is a typo or an
        # assumption we do not honor, and silently ignoring it is how a declaration ends
        # up trusted by an operator and read by nothing (§1.8).
        raise PinError(f"{path}: unknown key(s) {sorted(unknown)}; only collections: and roles: are honored")
    return doc


def _check_source(section: str, name: str, source: str, path: Path) -> None:
    """Reject a source URL carrying credentials in its userinfo.

    `.claude/rules/infra-supplychain.md` is absolute: no secret material in images,
    layers, or manifests (§2.5 — brokered, never baked). The Dockerfile COPYs these
    declarations into an image layer, so a token embedded in a private Automation Hub URL
    would be baked into the image and committed to git. A Hub credential belongs in a
    build secret, not in a declaration file.
    """
    parsed = urllib.parse.urlsplit(source)
    if parsed.username or parsed.password or "@" in parsed.netloc:
        raise PinError(
            f"{path}: {section} {name!r} source {source!r} embeds credentials in its URL. "
            f"Secrets are brokered, never baked (§2.5) — and this file is COPYed into an "
            f"image layer. Use an unauthenticated URL plus a build secret"
        )


def _entries(doc: dict[str, Any], section: str, path: Path) -> list[dict[str, Any]]:
    raw = doc.get(section) or []
    if not isinstance(raw, list):
        raise PinError(f"{path}: {section}: must be a list")
    out: list[dict[str, Any]] = []
    for i, item in enumerate(raw):
        if isinstance(item, str):
            # `- community.crypto` is legal Galaxy shorthand and means "newest" — an
            # unpinned dependency by construction. Rejected with the fix spelled out.
            raise PinError(
                f"{path}: {section}[{i}] {item!r} is the unpinned shorthand form; "
                f"write it as a mapping with an exact version: "
                f'- {{ name: {item}, version: "x.y.z" }}'
            )
        if not isinstance(item, dict):
            raise PinError(f"{path}: {section}[{i}] must be a mapping")
        # `src:` is Galaxy's CANONICAL key for a role; `name:` is the install-directory
        # override. Accepting only `name:` rejected the documented spelling with a
        # misleading "must be a mapping with a name", which would read as a Stratt dialect
        # of a format this decision promises not to fork (§1.1).
        ref = item.get("src") or item.get("name")
        if not ref:
            raise PinError(
                f"{path}: {section}[{i}] must name the content — `name:` (collections) "
                f"or `src:`/`name:` (roles)"
            )
        version = item.get("version")
        if not version:
            raise PinError(
                f"{path}: {section} {ref!r} declares no version. Content must be pinned "
                f"exactly — an unpinned entry makes the image unreproducible and its digest "
                f"a lie about what content a Run had (ADR-0117 D3)"
            )
        version = str(version)
        # Positively an exact version (or, for a role from git, an immutable commit id).
        # A tag is accepted for roles because Galaxy roles are distributed by tag, but a
        # BRANCH is not: `devel` and `stable-2.19` are the failure this replaced.
        if not (_SEMVER.match(version) or (section == "roles" and _GIT_SHA.match(version))):
            allowed = "an exact x.y.z version" + (
                " or a full 40-character commit id" if section == "roles" else ""
            )
            raise PinError(
                f"{path}: {section} {ref!r} version {version!r} is not an exact pin — "
                f"require {allowed}. Branch names and partial versions (devel, HEAD, "
                f"stable-2.19, 2.22) resolve differently on different days"
            )
        if item.get("source"):
            _check_source(section, ref, str(item["source"]), path)
        entry: dict[str, Any] = {"name": ref, "version": version}
        if item.get("source"):
            entry["source"] = item["source"]
        out.append(entry)
    return out


def verify(paths: list[Path]) -> dict[str, list[dict[str, Any]]]:
    """Parse + assert every declared entry is exactly pinned. No network, no install."""
    merged: dict[str, list[dict[str, Any]]] = {"collections": [], "roles": []}
    for path in paths:
        doc = _load(path)
        for section in ("collections", "roles"):
            merged[section].extend(_entries(doc, section, path))
    return merged


def _installed_collections(root: Path) -> list[dict[str, Any]]:
    base = root / "ansible_collections"
    found: list[dict[str, Any]] = []
    for manifest in sorted(base.glob("*/*/MANIFEST.json")):
        info = json.loads(manifest.read_text()).get("collection_info") or {}
        found.append({"name": f"{info.get('namespace')}.{info.get('name')}", "version": info.get("version")})
    return found


def _installed_roles(root: Path) -> list[dict[str, Any]]:
    found: list[dict[str, Any]] = []
    if not root.is_dir():
        return found
    for info in sorted(root.glob("*/meta/.galaxy_install_info")):
        with info.open() as fh:
            meta = yaml.safe_load(fh) or {}
        found.append({"name": info.parent.parent.name, "version": str(meta.get("version") or "")})
    return found


def _assert_on_search_path(collections_dir: Path, roles_dir: Path) -> None:
    """Fail the build if ansible would not FIND content installed at these paths.

    The whole no-plumbing design rests on `/usr/share/ansible/{collections,roles}` being on
    ansible's default search path — measured, not assumed. But `ansible-core` and
    `ansible-runner` float within their evergreen version bands (§1.7), so a future release
    that started overriding those paths would make build-time content silently invisible,
    surfacing only as a baffling "role not found" at run time. Asserting it at BUILD time
    turns that into a loud, early failure in the place that can fix it (§1.8).
    """
    out = subprocess.run(["ansible-config", "dump"], check=True, capture_output=True, text=True).stdout
    for label, path in (("COLLECTIONS_PATHS", collections_dir), ("DEFAULT_ROLES_PATH", roles_dir)):
        line = next((ln for ln in out.splitlines() if ln.startswith(label)), "")
        if str(path) not in line:
            raise PinError(
                f"ansible would not find content at {path}: it is absent from {label} "
                f"({line or 'not reported'}). The no-ANSIBLE_ROLES_PATH design assumes these "
                f"defaults; an ansible-core/ansible-runner upgrade appears to have changed them, "
                f"so the install path must be revisited (ADR-0117 D3)"
            )


def install(paths: list[Path], collections_dir: Path, roles_dir: Path, manifest_path: Path) -> None:
    declared = verify(paths)  # pins are enforced BEFORE anything touches the network
    collections_dir.mkdir(parents=True, exist_ok=True)
    roles_dir.mkdir(parents=True, exist_ok=True)
    if declared["collections"] or declared["roles"]:
        # Only when content is actually being installed: a base EE installs nothing, so
        # there is no search-path assumption to hold it to.
        _assert_on_search_path(collections_dir, roles_dir)

    for path in paths:
        # Both subcommands run unconditionally: `ansible-galaxy` treats a file with no
        # matching section as "Skipping install, no requirements found" and exits 0
        # (verified), so no conditional logic is needed to handle a one-section file.
        for args in (
            ["ansible-galaxy", "collection", "install", "-r", str(path), "-p", str(collections_dir)],
            ["ansible-galaxy", "role", "install", "-r", str(path), "-p", str(roles_dir)],
        ):
            subprocess.run(args, check=True)

    # Verify-don't-trust: assert the RESOLVED set actually matches the declared pins.
    # `ansible-galaxy` can satisfy a request with a different version (dependency
    # resolution, a redirected name), and an image whose contents differ from its
    # declaration makes the digest an unreliable statement about the Run (§1.8).
    #
    # HONEST LIMIT — the two halves are not equally strong, and pretending otherwise was
    # a defect in the first draft of this file:
    #   * COLLECTIONS: genuinely independent. The version is read from MANIFEST.json,
    #     which ships INSIDE the downloaded artifact, so a mismatch is detectable.
    #   * ROLES: effectively tautological. `ansible-galaxy` writes
    #     meta/.galaxy_install_info from the REQUEST, not from the artifact, so this
    #     compares the declaration against itself and cannot fail. Verified: requesting
    #     `master` yields install_info `version: master`. The real protection for roles is
    #     the pin rule above (exact version or immutable commit id), not this check.
    # Closing the roles half needs per-artifact hashing — ADR-0117 follow-up (i).
    installed = {"collections": _installed_collections(collections_dir), "roles": _installed_roles(roles_dir)}
    by_name = {s: {e["name"]: e["version"] for e in installed[s]} for s in installed}
    for section in ("collections", "roles"):
        for want in declared[section]:
            got = by_name[section].get(want["name"])
            if got is None:
                raise PinError(f"declared {section[:-1]} {want['name']!r} is not installed after install")
            if got != want["version"]:
                raise PinError(
                    f"declared {section[:-1]} {want['name']!r} pinned {want['version']} "
                    f"but {got} was installed — the image would not match its declaration"
                )

    # The run-visible manifest (§1.8). Dependencies pulled in transitively are recorded
    # too, marked declared=false: the manifest is what the image ACTUALLY contains, not
    # a restatement of the request, so an operator descending into a Run can see the
    # content that was really present — including what nobody asked for directly.
    declared_names = {s: {e["name"] for e in declared[s]} for s in declared}
    manifest = {
        section: [
            {**entry, "declared": entry["name"] in declared_names[section]} for entry in installed[section]
        ]
        for section in ("collections", "roles")
    }
    manifest_path.parent.mkdir(parents=True, exist_ok=True)
    manifest_path.write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")
    os.chmod(manifest_path, 0o644)
    print(f"stratt-ee-content: wrote {manifest_path}", file=sys.stderr)
    print(json.dumps(manifest, indent=2, sort_keys=True))


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)

    v = sub.add_parser("verify", help="assert every declared entry is exactly pinned (no network)")
    v.add_argument("files", nargs="+", type=Path)

    i = sub.add_parser(
        "install", help="verify, install to the EE, assert the resolved set, write the manifest"
    )
    i.add_argument(
        "files",
        nargs="*",
        type=Path,
        help="Galaxy requirements files; NONE is legal and yields a base EE "
        "with an explicit empty manifest (stated, not merely absent)",
    )
    i.add_argument("--collections-dir", type=Path, default=Path("/usr/share/ansible/collections"))
    i.add_argument("--roles-dir", type=Path, default=Path("/usr/share/ansible/roles"))
    i.add_argument("--manifest", type=Path, default=Path(MANIFEST_PATH))

    args = ap.parse_args()
    try:
        if args.cmd == "verify":
            merged = verify(args.files)
            n = len(merged["collections"]), len(merged["roles"])
            print(f"stratt-ee-content: {n[0]} collection(s), {n[1]} role(s) exactly pinned")
        else:
            install(args.files, args.collections_dir, args.roles_dir, args.manifest)
    except PinError as err:
        print(f"stratt-ee-content: {err}", file=sys.stderr)
        return 1
    except subprocess.CalledProcessError as err:
        print(f"stratt-ee-content: {' '.join(err.cmd)} failed with rc={err.returncode}", file=sys.stderr)
        return err.returncode
    return 0


if __name__ == "__main__":
    sys.exit(main())
