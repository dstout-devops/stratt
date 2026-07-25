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
import hashlib
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

LOCKFILE_VERSION = 1

# Files whose bytes are produced by the INSTALL rather than shipped by the artifact, and
# which therefore must not enter the content digest. Kept deliberately tiny — every
# exclusion is content the lockfile stops covering:
#   * meta/.galaxy_install_info — ansible-galaxy writes this per install FROM THE REQUEST
#     (it is the reason the roles half of the resolved-set check is tautological), so it
#     restates our own declaration and varies by install date.
#   * __pycache__ / *.pyc — compiler output, not distributed content, and not
#     byte-reproducible across interpreter versions.
_DIGEST_SKIP_NAMES = frozenset({".galaxy_install_info"})
_DIGEST_SKIP_DIRS = frozenset({"__pycache__"})
_DIGEST_SKIP_SUFFIXES = (".pyc",)


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


def _tree_digest(root: Path) -> str:
    """A deterministic SHA-256 over the CONTENT installed at `root`.

    This is the per-artifact hash ADR-0117 follow-up (i) is about, and it is what makes the
    lockfile mean something the registry cannot overrule. Until it existed, the registry was
    the only checksum authority: `ansible-galaxy` asks Galaxy for `community.crypto==2.22.3`
    and installs whatever bytes come back, so a REPUBLISHED artifact at the same version
    number — a compromised or merely re-cut release — resolved clean and produced a
    different image from the same declaration. The pin rule bounds *which version*; only a
    content hash bounds *which bytes*.

    Hashed as a sorted list of (relative path, executable bit, file digest) so the result
    depends on content and layout and NOTHING else — not mtimes, not inode order, not the
    absolute install prefix, not empty directories. Symlinks are hashed by their target
    string rather than followed: a symlink is content, and following one would either
    double-count or escape the tree.

    HONEST LIMITS, because a hash invites more trust than it earns:
      * This digests the INSTALLED TREE, not the downloaded tarball. It therefore detects a
        republished or tampered artifact, but it does not verify anything BEFORE extraction
        — `ansible-galaxy` has already run. The gate is that no image survives the
        mismatch, not that no bytes were ever written.
      * The first lock is trust-on-first-use. It records what the registry served that day;
        it cannot tell you that day's bytes were the author's intent. Reviewing a lockfile
        DIFF is where the security value lands, which is why the file is in git.
    """
    entries: list[tuple[str, str]] = []
    for path in sorted(root.rglob("*")):
        rel = path.relative_to(root)
        if _DIGEST_SKIP_DIRS.intersection(rel.parts):
            continue
        if path.is_symlink():
            entries.append((rel.as_posix(), "l:" + os.readlink(path)))
            continue
        if not path.is_file():
            continue
        if path.name in _DIGEST_SKIP_NAMES or path.name.endswith(_DIGEST_SKIP_SUFFIXES):
            continue
        mode = "x" if path.stat().st_mode & 0o111 else "-"
        entries.append((rel.as_posix(), f"f{mode}:{hashlib.sha256(path.read_bytes()).hexdigest()}"))
    h = hashlib.sha256()
    for relpath, marker in sorted(entries):
        h.update(relpath.encode())
        h.update(b"\0")
        h.update(marker.encode())
        h.update(b"\0")
    return h.hexdigest()


def _installed_collections(root: Path) -> list[dict[str, Any]]:
    base = root / "ansible_collections"
    found: list[dict[str, Any]] = []
    for manifest in sorted(base.glob("*/*/MANIFEST.json")):
        info = json.loads(manifest.read_text()).get("collection_info") or {}
        found.append(
            {
                "name": f"{info.get('namespace')}.{info.get('name')}",
                "version": info.get("version"),
                "sha256": _tree_digest(manifest.parent),
            }
        )
    return found


def _installed_roles(root: Path) -> list[dict[str, Any]]:
    found: list[dict[str, Any]] = []
    if not root.is_dir():
        return found
    for info in sorted(root.glob("*/meta/.galaxy_install_info")):
        with info.open() as fh:
            meta = yaml.safe_load(fh) or {}
        found.append(
            {
                "name": info.parent.parent.name,
                "version": str(meta.get("version") or ""),
                "sha256": _tree_digest(info.parent.parent),
            }
        )
    return found


# ── the content lockfile (follow-up i) ────────────────────────────────────────────────


def lock_path_for(requirements: Path) -> Path:
    """`ee/content/crypto.requirements.yml` → `ee/content/crypto.requirements.lock.json`.

    Derived rather than configured: a lockfile whose association with its declaration is a
    flag is a lockfile that can be pointed at the wrong file.
    """
    return requirements.with_suffix(".lock.json")


def _load_lock(path: Path) -> dict[str, list[dict[str, Any]]]:
    try:
        doc = json.loads(path.read_text())
    except json.JSONDecodeError as err:
        raise PinError(f"{path}: not valid JSON ({err}); regenerate it with `task ee:content:lock`") from err
    if not isinstance(doc, dict):
        raise PinError(f"{path}: a lockfile must be a JSON object")
    version = doc.get("lockfileVersion")
    if version != LOCKFILE_VERSION:
        raise PinError(
            f"{path}: lockfileVersion {version!r}, this build understands {LOCKFILE_VERSION}. "
            f"Regenerate with `task ee:content:lock`"
        )
    out: dict[str, list[dict[str, Any]]] = {}
    for section in ("collections", "roles"):
        raw = doc.get(section) or []
        if not isinstance(raw, list):
            raise PinError(f"{path}: {section}: must be a list")
        for i, item in enumerate(raw):
            if not isinstance(item, dict) or not item.get("name"):
                raise PinError(f"{path}: {section}[{i}] must be a mapping naming the content")
            digest = str(item.get("sha256") or "")
            if not re.fullmatch(r"[0-9a-f]{64}", digest):
                raise PinError(
                    f"{path}: {section} {item['name']!r} sha256 {digest!r} is not a 64-character "
                    f"hex digest — a malformed hash is not a weaker check, it is no check"
                )
        out[section] = list(raw)
    return out


def _lock_doc(manifest: dict[str, list[dict[str, Any]]]) -> dict[str, Any]:
    return {
        "lockfileVersion": LOCKFILE_VERSION,
        # ASCII deliberately: json.dumps escapes non-ASCII to \\uXXXX, which turns a
        # reviewer-facing note in a reviewer-facing file into line noise.
        "_comment": (
            "Generated by `task ee:content:lock`; do not hand-edit. Each sha256 is a digest of the "
            "content actually installed for that artifact (ADR-0117 follow-up i). The EE build fails "
            "on any mismatch, so a republished artifact at the same version number is caught here "
            "instead of silently changing what a Run executed. Review the DIFF: that is the point."
        ),
        "collections": manifest["collections"],
        "roles": manifest["roles"],
    }


def verify_lock(manifest: dict[str, list[dict[str, Any]]], lock_path: Path) -> None:
    """Assert the installed closure matches the lockfile exactly, or fail the build.

    Both directions are checked, and the second one is the one that is easy to forget:
      * every LOCKED artifact must be installed at the locked version and digest — the
        republished-artifact case;
      * every INSTALLED artifact must be locked — otherwise a new TRANSITIVE dependency
        (which `ansible-galaxy` resolves without asking) enters the image with no recorded
        hash, and the lockfile quietly stops describing the image it claims to pin.
    """
    locked = _load_lock(lock_path)
    for section in ("collections", "roles"):
        want = {e["name"]: e for e in locked[section]}
        got = {e["name"]: e for e in manifest[section]}
        for name, w in sorted(want.items()):
            g = got.get(name)
            if g is None:
                raise PinError(
                    f"{lock_path}: locked {section[:-1]} {name!r} is not installed — the lockfile no "
                    f"longer describes this EE's content"
                )
            if g["version"] != w.get("version"):
                raise PinError(
                    f"{lock_path}: {section[:-1]} {name!r} locked at version {w.get('version')!r} but "
                    f"{g['version']!r} was installed"
                )
            if g["sha256"] != w["sha256"]:
                raise PinError(
                    f"{lock_path}: {section[:-1]} {name!r} version {g['version']} installed with content "
                    f"sha256 {g['sha256']} but the lockfile records {w['sha256']}. The SAME version "
                    f"number now resolves to DIFFERENT bytes — the artifact was republished, the "
                    f"registry is serving something else, or the content was tampered with. This is "
                    f"the case the registry-as-checksum-authority could not detect. Investigate before "
                    f"relocking; `task ee:content:lock` would accept the new bytes without asking"
                )
            if bool(g.get("declared")) != bool(w.get("declared")):
                raise PinError(
                    f"{lock_path}: {section[:-1]} {name!r} is now "
                    f"{'directly declared' if g.get('declared') else 'only a transitive dependency'} "
                    f"but the lockfile records the opposite — the declaration changed; relock it"
                )
        for name in sorted(set(got) - set(want)):
            raise PinError(
                f"{lock_path}: {section[:-1]} {name!r} version {got[name]['version']} is installed but "
                f"NOT locked. A dependency entered this image with no recorded hash (transitive "
                f"resolution, or a declaration added without relocking). Run `task ee:content:lock` and "
                f"review the diff"
            )


def check_locks(paths: list[Path]) -> int:
    """The offline half of the lock gate, for `task ci`: every declared artifact is locked.

    This CANNOT verify a hash — verifying one means installing the artifact, which means the
    network, which CI must not depend on for a pin check. So it is deliberately the weaker
    of the two gates and is described as such: it catches the mistake that actually happens
    (a collection added or bumped without relocking) and says nothing about whether the
    recorded bytes are the right bytes. The BUILD is the strong gate.
    """
    checked = 0
    for path in paths:
        declared = verify([path])
        lock_path = lock_path_for(path)
        if not lock_path.exists():
            raise PinError(
                f"{path}: no lockfile at {lock_path}. Content declarations carry per-artifact hashes "
                f"(ADR-0117 follow-up i); generate it with `task ee:content:lock CONTENT={path.name}`"
            )
        locked = _load_lock(lock_path)
        for section in ("collections", "roles"):
            by_name = {e["name"]: e for e in locked[section]}
            for want in declared[section]:
                got = by_name.get(want["name"])
                if got is None:
                    raise PinError(
                        f"{lock_path}: declares nothing for {section[:-1]} {want['name']!r}, which "
                        f"{path.name} requires — relock (`task ee:content:lock CONTENT={path.name}`)"
                    )
                if str(got.get("version")) != want["version"]:
                    raise PinError(
                        f"{lock_path}: {section[:-1]} {want['name']!r} is locked at "
                        f"{got.get('version')!r} but {path.name} now pins {want['version']!r} — relock"
                    )
                if not got.get("declared"):
                    raise PinError(
                        f"{lock_path}: {section[:-1]} {want['name']!r} is recorded as a transitive "
                        f"dependency but {path.name} declares it directly — relock"
                    )
                checked += 1
    return checked


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


def install(
    paths: list[Path],
    collections_dir: Path,
    roles_dir: Path,
    manifest_path: Path,
    lock_mode: str = "verify",
) -> None:
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
    # The asymmetry is now CLOSED by the lockfile below (follow-up i): a role's content
    # digest comes from its installed bytes, not from anything ansible-galaxy restates, so
    # the roles half finally has an authority independent of the request.
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

    # ── the lockfile gate (follow-up i) ───────────────────────────────────────────────
    # Ordered AFTER the manifest deliberately: the manifest is the §1.8 record of what this
    # image contains, and it is more useful to have written it and then failed than to fail
    # with nothing to look at.
    #
    # One lockfile per requirements file, so it covers exactly the artifacts THAT
    # declaration is responsible for. A base EE (no requirements file) has no lockfile and
    # nothing to check — there is no content whose bytes could change.
    #
    # A lockfile records the whole installed CLOSURE, including transitive dependencies,
    # which is only attributable to one declaration if there IS only one. Two requirements
    # files in a single build would make each lockfile the union of both — a file that reads
    # as "these are crypto's artifacts" while listing someone else's. Refused rather than
    # written wrong; the EE build passes exactly one file (EE_CONTENT), so this is a guard
    # against a future caller, not a limitation anything hits today.
    if len(paths) > 1:
        raise PinError(
            f"lock mode {lock_mode!r} with {len(paths)} requirements files "
            f"({', '.join(p.name for p in paths)}): a lockfile records the installed closure, which "
            f"cannot be attributed to one of several declarations. Build one content set per image"
        )
    for path in paths:
        lock_path = lock_path_for(path)
        if lock_mode == "write":
            lock_path.write_text(json.dumps(_lock_doc(manifest), indent=2, sort_keys=True) + "\n")
            os.chmod(lock_path, 0o644)
            print(
                f"stratt-ee-content: WROTE lockfile {lock_path} (no verification performed)", file=sys.stderr
            )
            continue
        if not lock_path.exists():
            raise PinError(
                f"{path.name} has no lockfile at {lock_path}. Content carries per-artifact SHA-256 "
                f"hashes (ADR-0117 follow-up i) and the build will not produce an image whose bytes "
                f"nothing in git records. Generate it with `task ee:content:lock CONTENT={path.name}`"
            )
        verify_lock(manifest, lock_path)
        n = len(manifest["collections"]) + len(manifest["roles"])
        print(f"stratt-ee-content: {n} artifact(s) match {lock_path.name}", file=sys.stderr)


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)

    v = sub.add_parser(
        "verify",
        help="assert every declared entry is exactly pinned AND locked (no network, no install)",
    )
    v.add_argument("files", nargs="+", type=Path)

    i = sub.add_parser(
        "install", help="verify, install to the EE, assert the resolved set, write the manifest"
    )
    i.add_argument(
        "--lock-mode",
        choices=("verify", "write"),
        default="verify",
        help="verify (default): fail the build if the installed content does not match its "
        "lockfile. write: (re)generate the lockfile from what was installed and verify "
        "NOTHING — only `task ee:content:lock` should ever pass this",
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
            locked = check_locks(args.files)
            print(
                f"stratt-ee-content: {n[0]} collection(s), {n[1]} role(s) exactly pinned; "
                f"{locked} locked (hashes are verified by the BUILD, not here)"
            )
        else:
            install(args.files, args.collections_dir, args.roles_dir, args.manifest, lock_mode=args.lock_mode)
    except PinError as err:
        print(f"stratt-ee-content: {err}", file=sys.stderr)
        return 1
    except subprocess.CalledProcessError as err:
        print(f"stratt-ee-content: {' '.join(err.cmd)} failed with rc={err.returncode}", file=sys.stderr)
        return err.returncode
    return 0


if __name__ == "__main__":
    sys.exit(main())
