package content

import (
	"bufio"
	"bytes"
	"errors"
	"io/fs"
	"sort"
	"strings"
)

// ── ansible.cfg: the file that changes the meaning of everything else (ANS-005) ──────────────────
//
// The audit's framing is exact: `ansible.cfg` "sets roles paths, connection defaults, strategy,
// callbacks — it changes the meaning of everything else in the root". A projection that reads the
// tree without reading the config is reporting on a layout the tool may not be using.
//
// That is not hypothetical here. `roles_path` moves where roles live, and this Syncer's role reader
// looks in `roles/`. A root whose config says `roles_path = galaxy_roles` projected ZERO roles and
// said nothing — a silent wrong answer that ANS-005 exposes and that this file therefore also
// FIXES rather than merely records (see rolesSearchPaths).
//
// ── VALUES FOR THE SETTINGS THAT CHANGE MEANING; NAMES FOR THE REST ─────────────────────────────
// The projected values are a bounded ALLOWLIST — paths, booleans, enums, integers, all structural.
// Everything else in the file contributes its KEY NAME only, which is the same rule this Connector
// applies to vars scopes, notification configuration and schedule extra_data.
//
// It is not merely consistency. `ansible.cfg` CAN hold a credential: a `[galaxy_server.*]` section
// takes a `token`, and that is a real Galaxy API token in a real repo. An allowlist keeps it out by
// construction — a key nobody added to the list contributes its name and nothing else — whereas a
// denylist would have to anticipate it (§2.5).

// configSettings is the allowlist: settings whose VALUE is structural and changes how the rest of
// the root is interpreted. Keyed by the ini key; the section is not part of the key because
// ansible resolves these by name and a repo may place them in either `[defaults]` or a variant.
var configSettings = map[string]string{
	// Layout — these move where content lives, so they change what this projection means.
	"roles_path":          "rolesPath",
	"collections_path":    "collectionsPath",
	"collections_paths":   "collectionsPath", // the deprecated plural spelling, same fact
	"library":             "libraryPath",
	"module_utils":        "moduleUtilsPath",
	"filter_plugins":      "filterPluginsPath",
	"callback_plugins":    "callbackPluginsPath",
	"action_plugins":      "actionPluginsPath",
	"lookup_plugins":      "lookupPluginsPath",
	"inventory":           "inventoryPath",
	"vault_password_file": "vaultPasswordFile", // a PATH, never material
	// Connection + escalation defaults — what a run does when a play says nothing.
	"remote_user":       "remoteUser",
	"host_key_checking": "hostKeyChecking",
	"timeout":           "timeout",
	"become":            "become",
	"become_method":     "becomeMethod",
	"become_user":       "becomeUser",
	"transport":         "transport",
	// Execution shape.
	"forks":              "forks",
	"strategy":           "strategy",
	"gathering":          "gathering",
	"fact_caching":       "factCaching",
	"interpreter_python": "interpreterPython",
	// Output/observability — a custom stdout callback changes what a Run's log looks like,
	// which matters when the log is the evidence (§1.8).
	"stdout_callback":    "stdoutCallback",
	"callbacks_enabled":  "callbacksEnabled",
	"callback_whitelist": "callbacksEnabled", // the pre-2.11 spelling, same fact
}

// AnsibleConfig is the observed ansible.cfg. Named for the file rather than as `Config` because
// this package already has a client Config — two different things, and one of them is a
// projection of somebody else's file.
type AnsibleConfig struct {
	Path string
	// Settings are the allowlisted settings, keyed by their projected name.
	Settings map[string]string
	// OtherKeys are the names of every other key in the file, `section.key`, sorted. Names
	// only: a `[galaxy_server.*]` section holds a real API token (§2.5).
	OtherKeys []string
}

// configPaths are the locations ansible itself searches, in its own precedence order. Only the
// two that live INSIDE a content root are readable here: ANSIBLE_CONFIG and ~/.ansible.cfg and
// /etc/ansible/ansible.cfg belong to the machine running ansible, not to the repo, and claiming
// to have read them would be asserting a fact about someone else's box.
var configPaths = []string{"ansible.cfg", ".ansible.cfg"}

// readConfig parses the root's ansible.cfg, if it has one.
func (c *Client) readConfig() (*AnsibleConfig, error) {
	for _, p := range configPaths {
		b, err := fs.ReadFile(c.fsys, p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		cfg := parseINI(b)
		cfg.Path = p
		return cfg, nil
	}
	return nil, nil
}

// parseINI reads ansible's config format. Hand-rolled rather than via a library because the
// surface needed is tiny (sections, key = value, `#`/`;` comments) and adding a dependency for it
// would not survive the §1.4 boring-spine question.
func parseINI(b []byte) *AnsibleConfig {
	cfg := &AnsibleConfig{Settings: map[string]string{}}
	section := ""
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		// An inline comment after a value is ansible-legal in the `;` form.
		if i := strings.Index(value, " ;"); i >= 0 {
			value = strings.TrimSpace(value[:i])
		}
		if projected, allowed := configSettings[key]; allowed {
			cfg.Settings[projected] = value
			continue
		}
		// Not on the allowlist: the NAME only. A `[galaxy_server.x] token = …` lands here
		// and its value never leaves this function (§2.5).
		cfg.OtherKeys = append(cfg.OtherKeys, section+"."+key)
	}
	sort.Strings(cfg.OtherKeys)
	return cfg
}

// rolesSearchPaths is where this projection should look for roles: always `roles/`, plus any
// RELATIVE, in-root entry of the config's roles_path.
//
// This is the ANS-005 fix rather than the ANS-005 observation. A root configured with
// `roles_path = galaxy_roles` previously projected zero roles and reported no problem.
//
// ABSOLUTE and escaping entries are deliberately skipped: they name a location outside the
// content root, which this Syncer cannot read and must not pretend to have read. They still
// appear in the projected `rolesPath` value, so an operator can see that content lives somewhere
// this projection does not cover — visible rather than silent (§1.8).
func rolesSearchPaths(cfg *AnsibleConfig) []string {
	out := []string{"roles"}
	if cfg == nil {
		return out
	}
	seen := map[string]bool{"roles": true}
	for _, p := range strings.Split(cfg.Settings["rolesPath"], ":") {
		p = strings.TrimSpace(strings.TrimSuffix(p, "/"))
		if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") || strings.Contains(p, "..") {
			continue
		}
		if p = strings.TrimPrefix(p, "./"); p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}
