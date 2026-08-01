package controller

import (
	"net/url"
	"strings"
)

// ── the AWX Project, and the credential that hides in its clone URL (ADR-0154 D3) ───────────────
//
// `scm_url` is the fifth place in this Connector where §2.5 has to be answered, and it has a shape
// of its own. AWX stores repository credentials as a separate object — but a real estate routinely
// contains
//
//	https://svc-account:ghp_REALTOKENHERE@github.com/acme/infra.git
//
// because embedding a PAT in the clone URL works and nobody stopped them.
//
// Dropping the URL entirely would lose the fact an operator most needs — WHICH repository — to guard
// against a minority case. Projecting it verbatim would import live tokens into the graph. So the
// userinfo is REMOVED and a boolean says so.
//
// The boolean carries as much weight as the redaction. Stripping silently would leave a reader
// unable to tell a clean URL from a scrubbed one, and "this repository is cloned with an embedded
// credential" is itself worth surfacing (§1.8).

// redactSCMURL removes any embedded credential from an SCM URL, reporting whether it took anything
// out — or whether the value was withheld entirely because it could not be proven safe.
//
// The unparseable case is deliberately conservative. `scm_url` is a free string that AWX does not
// validate into a URL, and an ssh-style `git@github.com:acme/infra.git` is not a URL at all. A value
// this cannot parse AND that contains an `@` is withheld: it might be `user:token@host`, and a value
// we cannot prove is safe is not one to project (§2.5). A value with no `@` has nowhere to hide a
// credential and passes through.
func redactSCMURL(raw string) (projected string, redacted bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		if strings.Contains(raw, "@") {
			// scp-style `git@host:path` is the common benign form, and `user:token@host` the
			// dangerous one. They are not reliably distinguishable here, so the whole value is
			// withheld and the flag says something was removed.
			return "", true
		}
		return raw, false
	}
	if u.User == nil {
		return u.String(), false
	}
	// A bare username with no password is not a secret — `https://git@github.com/…` is an
	// ordinary URL. Only strip when there is something to strip, so the flag stays meaningful:
	// a reader must be able to trust that `scmUrlRedacted` means a CREDENTIAL was present.
	if _, hasPassword := u.User.Password(); !hasPassword {
		return u.String(), false
	}
	u.User = nil
	return u.String(), true
}
