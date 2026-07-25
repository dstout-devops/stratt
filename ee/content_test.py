"""Tests for the EE content pin gate (ADR-0117 D3).

These exist because the pin rule is **supply-chain-security logic** (§7.3): it is the only
thing standing between a committed declaration and an unreproducible image. It was first
written as a token DENYLIST, which looked sufficient and was not — a charter review proved
that `devel`, `HEAD`, `dev`, `stable-2.19`, `2.22` and `1.0` all passed as "exactly pinned"
while naming a moving ref or an incomplete version. Every one of those is a case below, so
the same regression cannot land twice.
"""

from __future__ import annotations

from pathlib import Path

import pytest
from content import PinError, verify


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
