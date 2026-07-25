"""Tests for the EE content pin gate (ADR-0117 D3).

These exist because the pin rule is **supply-chain-security logic** (§7.3): it is the only
thing standing between a committed declaration and an unreproducible image. It was first
written as a token DENYLIST, which looked sufficient and was not — a charter review proved
that `devel`, `HEAD`, `dev`, `stable-2.19`, `2.22` and `1.0` all passed as "exactly pinned"
while naming a moving ref or an incomplete version. Every one of those is a case below, so
the same regression cannot land twice.
"""

from __future__ import annotations

import json
import os
from pathlib import Path

import pytest
from content import (
    PinError,
    _tree_digest,
    check_locks,
    lock_path_for,
    verify,
    verify_lock,
)


def _write(tmp_path: Path, body: str) -> Path:
    path = tmp_path / "requirements.yml"
    path.write_text(body)
    return path


def _verify(tmp_path: Path, body: str) -> dict[str, list[dict[str, object]]]:
    return verify([_write(tmp_path, body)])


# ── exact pins are accepted ───────────────────────────────────────────────────────────


@pytest.mark.parametrize("version", ["2.22.3", "1.0.0", "10.20.30", "2.0.0-rc1", "1.2.3+meta"])
def test_exact_versions_accepted(tmp_path: Path, version: str) -> None:
    got = _verify(tmp_path, f'collections:\n  - name: community.crypto\n    version: "{version}"\n')
    assert got["collections"] == [{"name": "community.crypto", "version": version}]


def test_shipped_declaration_is_pinned() -> None:
    """The real file must pass its own gate — a gate nothing shipping satisfies is theatre."""
    shipped = Path(__file__).parent / "content" / "crypto.requirements.yml"
    got = verify([shipped])
    assert got["collections"], "the shipped declaration must declare content"
    for entry in got["collections"] + got["roles"]:
        assert entry["version"], entry


# ── moving refs and partial versions are rejected ─────────────────────────────────────


@pytest.mark.parametrize(
    "version",
    [
        "devel",  # every one of these passed the original denylist
        "HEAD",
        "dev",
        "stable-2.19",
        "2.22",  # incomplete: resolves to whatever 2.22.x is newest
        "1.0",
        "master",
        "main",
        "latest",
        ">=2.0.0",  # ranges
        "*",
        "2.22.3,<3",
    ],
)
def test_unpinned_versions_rejected(tmp_path: Path, version: str) -> None:
    with pytest.raises(PinError):
        _verify(tmp_path, f'collections:\n  - name: community.crypto\n    version: "{version}"\n')


def test_missing_version_rejected(tmp_path: Path) -> None:
    with pytest.raises(PinError, match="declares no version"):
        _verify(tmp_path, "collections:\n  - name: community.crypto\n")


def test_shorthand_string_form_rejected(tmp_path: Path) -> None:
    """`- ns.name` is legal Galaxy shorthand meaning "newest" — unpinned by construction."""
    with pytest.raises(PinError, match="unpinned shorthand"):
        _verify(tmp_path, "collections:\n  - community.crypto\n")


def test_unknown_key_rejected(tmp_path: Path) -> None:
    """Strict, like every Stratt boundary file: a typo must not be silently ignored (§1.8)."""
    with pytest.raises(PinError, match="unknown key"):
        _verify(tmp_path, 'collectionz:\n  - name: c.d\n    version: "1.0.0"\n')


# ── roles: Galaxy's canonical spelling, and the stricter pin rule ─────────────────────


def test_roles_accept_canonical_src_key(tmp_path: Path) -> None:
    """`src:` is Galaxy's canonical role key. Rejecting it would fork a format §1.1 says we don't."""
    got = _verify(tmp_path, 'roles:\n  - src: geerlingguy.certbot\n    version: "5.2.0"\n')
    assert got["roles"] == [{"name": "geerlingguy.certbot", "version": "5.2.0"}]


def test_roles_accept_full_commit_id(tmp_path: Path) -> None:
    """A role installs from a git tarball, so an immutable commit id is a legitimate pin."""
    sha = "0123456789abcdef0123456789abcdef01234567"
    got = _verify(tmp_path, f'roles:\n  - src: some.role\n    version: "{sha}"\n')
    assert got["roles"][0]["version"] == sha


def test_roles_reject_branch_names(tmp_path: Path) -> None:
    """The case that matters most: a BRANCH is a legal role `version:` and a moving target.

    Roles are the weak half of D3 — `ansible-galaxy` writes meta/.galaxy_install_info from
    the REQUEST, so the post-install resolved-set check cannot catch a branch. This rule is
    the only real protection.
    """
    for version in ("devel", "master", "main", "feature/x", "0123456789abcdef"):  # last = short sha
        with pytest.raises(PinError):
            _verify(tmp_path, f'roles:\n  - src: some.role\n    version: "{version}"\n')


def test_role_needs_a_name_or_src(tmp_path: Path) -> None:
    with pytest.raises(PinError, match="must name the content"):
        _verify(tmp_path, 'roles:\n  - version: "1.0.0"\n')


# ── §2.5: secrets are brokered, never baked ───────────────────────────────────────────


@pytest.mark.parametrize(
    "source",
    [
        "https://tok3n@hub.example.com/api/galaxy/",
        "https://user:pass@hub.example.com/api/galaxy/",
    ],
)
def test_credentialed_source_rejected(tmp_path: Path, source: str) -> None:
    """The Dockerfile COPYs these files into an image layer, so a token in a URL would be
    baked into the image and committed to git — flatly forbidden (§2.5)."""
    with pytest.raises(PinError, match="brokered, never baked"):
        _verify(
            tmp_path,
            f'collections:\n  - name: c.d\n    version: "1.0.0"\n    source: {source}\n',
        )


def test_uncredentialed_source_allowed(tmp_path: Path) -> None:
    """A private Automation Hub endpoint is legitimate — only embedded credentials are not."""
    got = _verify(
        tmp_path,
        'collections:\n  - name: c.d\n    version: "1.0.0"\n    source: https://hub.example.com/api/\n',
    )
    assert got["collections"][0]["source"] == "https://hub.example.com/api/"


# ── multi-file and empty-section handling ─────────────────────────────────────────────


def test_empty_and_absent_sections(tmp_path: Path) -> None:
    assert _verify(tmp_path, "collections: []\nroles: []\n") == {"collections": [], "roles": []}
    got = _verify(tmp_path, 'collections:\n  - name: c.d\n    version: "1.0.0"\n')
    assert got["roles"] == []


def test_verify_accepts_no_files() -> None:
    """A base EE declares no content at all; that is legal, not an error."""
    assert verify([]) == {"collections": [], "roles": []}


# ── the content lockfile: per-artifact hashes (ADR-0117 follow-up i) ──────────────────
#
# These cover the half of the supply chain the pin rule above CANNOT reach. A pin bounds
# which VERSION is requested; until the lockfile existed the registry was the only checksum
# authority, so a republished artifact at the same version number resolved clean and changed
# what every Run executed from an unchanged declaration. The digest is also the only real
# protection for ROLES, whose resolved-set check is tautological by construction
# (ansible-galaxy writes meta/.galaxy_install_info from the request, not the artifact).


def _tree(tmp_path: Path, files: dict[str, str]) -> Path:
    root = tmp_path / "artifact"
    for rel, body in files.items():
        path = root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(body)
    root.mkdir(parents=True, exist_ok=True)
    return root


def test_tree_digest_is_stable_across_identical_trees(tmp_path: Path) -> None:
    """The property the whole gate rests on: same content, same digest.

    Measured rather than assumed, because a digest that drifts between two installs of the
    same artifact would make the build fail at random and get the gate deleted. (Verified
    live too: two --no-cache EE builds of community.crypto 2.22.3 produce the same hash.)
    """
    a = _tree(tmp_path / "a", {"plugins/x.py": "body", "README.md": "hi"})
    b = _tree(tmp_path / "b", {"README.md": "hi", "plugins/x.py": "body"})
    assert _tree_digest(a) == _tree_digest(b)


def test_tree_digest_ignores_mtime(tmp_path: Path) -> None:
    root = _tree(tmp_path, {"f": "same"})
    before = _tree_digest(root)
    os.utime(root / "f", (0, 0))
    assert _tree_digest(root) == before


def test_tree_digest_changes_with_content(tmp_path: Path) -> None:
    """The republished-artifact case, reduced to one byte."""
    a = _tree(tmp_path / "a", {"plugins/x.py": "original"})
    b = _tree(tmp_path / "b", {"plugins/x.py": "originaI"})
    assert _tree_digest(a) != _tree_digest(b)


def test_tree_digest_changes_with_layout(tmp_path: Path) -> None:
    """Same bytes, different path — a file moved into an autoloaded location is a real change."""
    a = _tree(tmp_path / "a", {"plugins/x.py": "body"})
    b = _tree(tmp_path / "b", {"plugins/y.py": "body"})
    assert _tree_digest(a) != _tree_digest(b)


def test_tree_digest_covers_the_executable_bit(tmp_path: Path) -> None:
    """Making a shipped script executable changes what the artifact can do; it must hash."""
    root = _tree(tmp_path, {"scripts/run": "#!/bin/sh\n"})
    before = _tree_digest(root)
    (root / "scripts" / "run").chmod(0o755)
    assert _tree_digest(root) != before


def test_tree_digest_hashes_symlinks_by_target(tmp_path: Path) -> None:
    """A symlink is content. Following one would double-count it or escape the tree."""
    root = _tree(tmp_path, {"real": "body"})
    (root / "link").symlink_to("real")
    before = _tree_digest(root)
    (root / "link").unlink()
    (root / "link").symlink_to("elsewhere")
    assert _tree_digest(root) != before


def test_tree_digest_skips_install_generated_files(tmp_path: Path) -> None:
    """meta/.galaxy_install_info restates our own request and varies per install."""
    root = _tree(tmp_path, {"tasks/main.yml": "- debug:"})
    before = _tree_digest(root)
    (root / "meta").mkdir()
    (root / "meta" / ".galaxy_install_info").write_text("install_date: today\nversion: 1.0.0\n")
    assert _tree_digest(root) == before


def test_tree_digest_skips_pycache(tmp_path: Path) -> None:
    """Compiler output is not distributed content and is not byte-reproducible."""
    root = _tree(tmp_path, {"plugins/x.py": "body"})
    before = _tree_digest(root)
    (root / "plugins" / "__pycache__").mkdir()
    (root / "plugins" / "__pycache__" / "x.cpython-314.pyc").write_bytes(b"\x00garbage")
    assert _tree_digest(root) == before


def test_lock_path_is_derived_not_configured() -> None:
    assert lock_path_for(Path("ee/content/crypto.requirements.yml")) == Path(
        "ee/content/crypto.requirements.lock.json"
    )


def _lock(tmp_path: Path, body: object) -> Path:
    path = tmp_path / "x.lock.json"
    path.write_text(json.dumps(body))
    return path


def _manifest(*entries: dict[str, object]) -> dict[str, list[dict[str, object]]]:
    return {"collections": list(entries), "roles": []}


_GOOD = {"name": "c.d", "version": "1.0.0", "sha256": "a" * 64, "declared": True}


def test_verify_lock_accepts_a_match(tmp_path: Path) -> None:
    path = _lock(tmp_path, {"lockfileVersion": 1, "collections": [_GOOD], "roles": []})
    verify_lock(_manifest(_GOOD), path)  # no raise


def test_verify_lock_rejects_a_republished_artifact(tmp_path: Path) -> None:
    """The case the ADR named: same version, different bytes."""
    path = _lock(tmp_path, {"lockfileVersion": 1, "collections": [_GOOD], "roles": []})
    installed = {**_GOOD, "sha256": "b" * 64}
    with pytest.raises(PinError, match="DIFFERENT bytes"):
        verify_lock(_manifest(installed), path)


def test_verify_lock_rejects_a_version_change(tmp_path: Path) -> None:
    path = _lock(tmp_path, {"lockfileVersion": 1, "collections": [_GOOD], "roles": []})
    with pytest.raises(PinError, match="locked at version"):
        verify_lock(_manifest({**_GOOD, "version": "1.0.1"}), path)


def test_verify_lock_rejects_an_unlocked_transitive_dependency(tmp_path: Path) -> None:
    """The direction that is easy to forget: ansible-galaxy resolves deps without asking."""
    path = _lock(tmp_path, {"lockfileVersion": 1, "collections": [_GOOD], "roles": []})
    extra = {"name": "e.f", "version": "2.0.0", "sha256": "c" * 64, "declared": False}
    with pytest.raises(PinError, match="NOT locked"):
        verify_lock(_manifest(_GOOD, extra), path)


def test_verify_lock_rejects_a_locked_artifact_that_vanished(tmp_path: Path) -> None:
    path = _lock(tmp_path, {"lockfileVersion": 1, "collections": [_GOOD], "roles": []})
    with pytest.raises(PinError, match="is not installed"):
        verify_lock({"collections": [], "roles": []}, path)


def test_verify_lock_rejects_a_declared_flag_flip(tmp_path: Path) -> None:
    path = _lock(tmp_path, {"lockfileVersion": 1, "collections": [_GOOD], "roles": []})
    with pytest.raises(PinError, match="transitive dependency"):
        verify_lock(_manifest({**_GOOD, "declared": False}), path)


def test_lockfile_version_is_checked(tmp_path: Path) -> None:
    path = _lock(tmp_path, {"lockfileVersion": 99, "collections": [], "roles": []})
    with pytest.raises(PinError, match="lockfileVersion"):
        verify_lock({"collections": [], "roles": []}, path)


@pytest.mark.parametrize("digest", ["", "abc", "A" * 64, "z" * 64, "a" * 63])
def test_malformed_digest_rejected(tmp_path: Path, digest: str) -> None:
    """A malformed hash is not a weaker check, it is no check — so it must not load."""
    path = _lock(tmp_path, {"lockfileVersion": 1, "collections": [{**_GOOD, "sha256": digest}], "roles": []})
    with pytest.raises(PinError, match="hex digest"):
        verify_lock(_manifest(_GOOD), path)


def test_corrupt_lockfile_names_the_fix(tmp_path: Path) -> None:
    path = tmp_path / "x.lock.json"
    path.write_text("{not json")
    with pytest.raises(PinError, match="ee:content:lock"):
        verify_lock({"collections": [], "roles": []}, path)


# ── check_locks: the OFFLINE gate `task ci` runs (weaker on purpose) ──────────────────


def test_check_locks_requires_a_lockfile(tmp_path: Path) -> None:
    req = tmp_path / "x.requirements.yml"
    req.write_text('collections:\n  - name: c.d\n    version: "1.0.0"\n')
    with pytest.raises(PinError, match="no lockfile"):
        check_locks([req])


def test_check_locks_catches_a_bump_without_a_relock(tmp_path: Path) -> None:
    """The mistake that actually happens: edit the version, forget the lockfile."""
    req = tmp_path / "x.requirements.yml"
    req.write_text('collections:\n  - name: c.d\n    version: "1.0.1"\n')
    _lock(tmp_path, {"lockfileVersion": 1, "collections": [_GOOD], "roles": []}).rename(
        tmp_path / "x.requirements.lock.json"
    )
    with pytest.raises(PinError, match="relock"):
        check_locks([req])


def test_check_locks_catches_a_newly_declared_transitive_dependency(tmp_path: Path) -> None:
    req = tmp_path / "x.requirements.yml"
    req.write_text('collections:\n  - name: c.d\n    version: "1.0.0"\n')
    _lock(
        tmp_path, {"lockfileVersion": 1, "collections": [{**_GOOD, "declared": False}], "roles": []}
    ).rename(tmp_path / "x.requirements.lock.json")
    with pytest.raises(PinError, match="transitive"):
        check_locks([req])


def test_shipped_declaration_is_locked() -> None:
    """The shipped pair must satisfy its own gate — a gate nothing satisfies is theatre.

    Note what this does NOT claim: the recorded hash is verified against real bytes only by
    the EE BUILD (`task ee:content:lock` produced it from a live install, and two --no-cache
    builds agree). Here it is checked for existence, shape and version agreement.
    """
    shipped = Path(__file__).parent / "content" / "crypto.requirements.yml"
    assert check_locks([shipped]) > 0, "the shipped declaration must lock at least one artifact"
