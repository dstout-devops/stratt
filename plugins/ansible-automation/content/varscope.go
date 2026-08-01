package content

import (
	"errors"
	"io/fs"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ── group_vars / host_vars: where an Ansible estate's configuration actually lives ──────────────
//
// ANS-003, and the audit put the cost plainly: not observing these means "why did this host get
// this value" is unanswerable from the graph. It is the largest thing an Ansible repo holds that
// the projection could not see.
//
// ── WHAT IS PROJECTED: SCOPE AND KEY NAMES. NEVER VALUES. ───────────────────────────────────────
// A group_vars file routinely contains credentials in the clear — that is the whole reason people
// vault them — so projecting values would carry secret material into the graph (§2.5). But scope
// ALONE does not answer the motivating question either: knowing that `group_vars/web.yml` exists
// says nothing about why a host got `http_port: 8080`. The key NAMES are the answer, and they are
// not secret: they say WHICH variable is bound at WHICH scope, which is the structure an operator
// traverses. This is the third time this line has been drawn in this repo — ADR-0128 D2 (credential
// name and kind only), ADR-0132 D3 (schedule extraDataKeys), AWX-009 (notification configKeys) —
// and it is the same rule each time: names are structure, values are material.
//
// ── PRECEDENCE IS OBSERVED, NEVER COMPUTED ──────────────────────────────────────────────────────
// Ansible's variable precedence is a long ordered list, and two files binding one name is the
// normal case rather than an error. This projects BOTH and marks neither a winner. Computing the
// winner would be reinterpreting Ansible's execution model (the §9 no-new-language line the audit
// says is correctly held) and would make Stratt assert a fact about a run that has not happened
// (§1.2). "Which scopes bind this name" is answerable and true; "which one wins" is ansible's to
// say at run time.
//
// ── VAULTED FILES: PRESENT, AND SAID SO (ANS-008) ───────────────────────────────────────────────
// A vaulted file is observed as PRESENT AND VAULTED with no keys, and is never decrypted (§2.5) —
// the plugin holds no vault password and must not want one. `vaulted: true` with an empty key list
// is a complete, honest answer, and a materially useful one: it distinguishes "this scope binds
// nothing" from "this scope binds things I cannot show you".

// VarScope is one group_vars/host_vars binding site: a file or a directory of files scoped to a
// group or a host.
type VarScope struct {
	Path string
	// Scope is group | host — which of the two directories it was found under.
	Scope string
	// Target is the group or host name the scope binds to: the file's base name without its
	// extension, or the directory name for the directory form.
	Target string
	// Keys are the TOP-LEVEL variable names bound here, sorted. Never the values.
	Keys []string
	// Vaulted reports an ansible-vault encrypted file. Keys is then empty — not because
	// nothing is bound, but because reading it would require decrypting it.
	Vaulted bool
}

// vaultHeader is ansible-vault's magic. A vaulted file is a text file whose FIRST LINE is
// `$ANSIBLE_VAULT;1.1;AES256` (or a later version/cipher), followed by hex.
const vaultHeader = "$ANSIBLE_VAULT"

// varScopes walks group_vars/ and host_vars/ at the content root.
//
// Ansible allows a scope to be EITHER `group_vars/web.yml` OR `group_vars/web/` holding several
// files, and both are common. The directory form projects as ONE scope with the union of its
// files' keys, because that is what ansible does with it — splitting it into one entity per file
// would invent a distinction the estate does not have.
func (c *Client) varScopes() ([]VarScope, error) {
	var out []VarScope
	for dir, scope := range map[string]string{"group_vars": "group", "host_vars": "host"} {
		ents, err := fs.ReadDir(c.fsys, dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range ents {
			if e.IsDir() {
				vs, err := c.varScopeDir(path.Join(dir, e.Name()), scope, e.Name())
				if err != nil {
					return nil, err
				}
				out = append(out, vs)
				continue
			}
			if !hasVarsExt(e.Name()) {
				continue
			}
			keys, vaulted, err := c.varKeys(path.Join(dir, e.Name()))
			if err != nil {
				return nil, err
			}
			out = append(out, VarScope{
				Path: path.Join(dir, e.Name()), Scope: scope,
				Target: strings.TrimSuffix(e.Name(), path.Ext(e.Name())),
				Keys:   keys, Vaulted: vaulted,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// varScopeDir unions the keys of every vars file in a `group_vars/<name>/` directory.
func (c *Client) varScopeDir(dir, scope, target string) (VarScope, error) {
	vs := VarScope{Path: dir, Scope: scope, Target: target}
	ents, err := fs.ReadDir(c.fsys, dir)
	if err != nil {
		return vs, err
	}
	seen := map[string]bool{}
	for _, e := range ents {
		if e.IsDir() || !hasVarsExt(e.Name()) {
			continue
		}
		keys, vaulted, err := c.varKeys(path.Join(dir, e.Name()))
		if err != nil {
			return vs, err
		}
		// ANY vaulted file in the directory makes the scope's key list incomplete, and saying
		// so beats presenting a partial list as if it were the whole binding.
		vs.Vaulted = vs.Vaulted || vaulted
		for _, k := range keys {
			if !seen[k] {
				seen[k] = true
				vs.Keys = append(vs.Keys, k)
			}
		}
	}
	sort.Strings(vs.Keys)
	return vs, nil
}

// varKeys reads the TOP-LEVEL keys of a vars file. Values are decoded and discarded — the decode
// is unavoidable (YAML has no key-only parse) but nothing but the names leaves this function.
//
// A file that does not parse is NOT an error: a vars file can legitimately hold a Jinja template
// that is not valid YAML until rendered, and failing the whole Observe over one would take every
// other artifact down with it (§1.8 — the empty-snapshot guardrail exists for real outages, not
// for a templated file).
func (c *Client) varKeys(p string) (keys []string, vaulted bool, err error) {
	b, err := fs.ReadFile(c.fsys, p)
	if err != nil {
		return nil, false, err
	}
	if strings.HasPrefix(strings.TrimSpace(string(b)), vaultHeader) {
		// Present and vaulted. Never decrypted: this plugin holds no vault password and must
		// not want one (§2.5).
		return nil, true, nil
	}
	var doc map[string]yaml.Node
	if yaml.Unmarshal(b, &doc) != nil {
		return nil, false, nil
	}
	keys = make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	sort.Strings(keys) // stable: a facet that reorders between polls reads as a change
	return keys, false, nil
}

func hasVarsExt(name string) bool {
	switch path.Ext(name) {
	case ".yml", ".yaml", ".json", "":
		// Ansible reads extensionless vars files too, and skipping them would silently
		// under-report a scope rather than fail visibly.
		return !strings.HasPrefix(name, ".")
	default:
		return false
	}
}
