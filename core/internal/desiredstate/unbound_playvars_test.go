package desiredstate

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// The unbound-play-variable guard (ADR-0118 slice 6).
//
// This is a TEST, and that boundary is load-bearing. ADR-0118 D6 forbids core from parsing tool
// content to discover parameters — doing so would make the spine learn Ansible's language (§1.4)
// and reopen the no-new-configuration-language non-goal. So nothing here runs in the parser: it
// is repo hygiene over OUR estate, in the same family as TestReferenceEstateParses.
//
// It exists because the two syntaxes are visually identical in YAML and only one was ever
// checked. `{{.launch.x}}` is a Stratt binding, validated at declaration; `{{ access_subject }}`
// is an Ansible variable that Stratt never sees. Two shipped reference Workflows referenced
// three variables defined nowhere in the repository, and they failed at run time on an undefined
// variable with nothing in the build to say so.
//
// IT FOLLOWS `include_role`, and that is not a convenience — it is what stops factoring from being
// an exit from this check. When the three web plays moved their shared ends into
// roles/stratt_web_service, the tasks that left the top-level play included the entire observe and
// fact-back tail, which is precisely where ANS-014's defect had lived. A guard that reads only the
// play it is pointed at would have gone quiet at exactly the moment the risky code moved. See
// scopeOf.
//
// HONEST LIMITS, stated because a guard invites more trust than it earns:
//   - It cannot see variables a play builds dynamically (a name assembled in a task). A templated
//     `include_vars` path is handled by scope rather than by resolution: the `vars/<dir>` literals a
//     play names bind the keys of the files under them, so an apache play does not silently inherit
//     tomcat's variables — but a layout file that FORGETS a key one distro needs still passes here,
//     because every file under the named directory contributes. Only executing that distro catches
//     it, which is what `task dev:content:proof`'s per-family nodes are for.
//   - Its list of play-local constructs (below) is enumerated by hand. A construct it does not
//     know is reported as unbound, so a false positive is possible; that fails loudly and is
//     fixed by adding the construct, which is the safe direction.
//   - It is not a Jinja parser. It matches `{{ name }}`-shaped references and captures only the
//     ROOT identifier, because a filter/index/attribute chain is an expression over a value
//     whose root is the thing that must be bound.
//   - Only the ROOT of each `{{ … }}` expression is checked, so an identifier nested inside a
//     call — `{{ q('subelements', access_members, 'members') }}` — is invisible here. Widening to
//     every identifier would mean parsing Jinja, which is the line D6 draws. `access_members` in
//     that example is in fact bound by a sibling `vars:` block; it was verified by hand, which is
//     precisely the manual step this guard removes for the common case and not the general one.
//   - A reference guarded by `| default(...)` is treated as BOUND. That is Jinja's own idiom for
//     "this may be undefined", so flagging it would be wrong — and it was: the first run of this
//     guard reported five unbound variables in the fileset Workflows, of which three were
//     `| default()`-guarded and only two were real.
//
// ADR-0134 made this guard STRICTLY EASIER without changing what it asserts. Plays are files now,
// so it reads a play out of the Actuator's resolved content rather than extracting a string from a
// declaration — and it still reads `params.play` for the Steps that legitimately keep an inline
// play (D4). The reference-integrity half it used to be asked to add is gone from here entirely:
// ParseDir refuses a Step naming a playbook its Actuator's tree does not hold, so an estate that
// parses has already proven that. TestPlaybookReferenceMustResolve pins that behaviour directly.

// jinjaRef matches a bare Ansible variable reference: `{{ name }}` or `{{ name.field }}` /
// `{{ name | filter }}`, capturing only the ROOT identifier. Stratt's own tokens start with a
// dot (`{{.launch.x}}`) and so never match.
var jinjaRef = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*([^}]*)\}\}`)

// defaulted matches a filter chain that supplies a fallback — Jinja's way of declaring the
// reference optional. `{{ x | default('a') }}` is bound by construction.
var defaulted = regexp.MustCompile(`\|\s*default\s*\(`)

// playLocal are the names a play legitimately defines for itself, so a reference to one is not
// unbound. Enumerated rather than inferred — see the doc comment's limits.
var playLocal = map[string]bool{
	// Ansible's own facts and magic variables.
	"ansible_facts": true, "ansible_host": true, "ansible_hostname": true,
	"ansible_play_hosts": true, "ansible_user": true, "inventory_hostname": true,
	"hostvars": true, "groups": true, "group_names": true, "play_hosts": true,
	"omit": true, "role_path": true, "playbook_dir": true, "lookup": true,
	// `q` is Ansible's shorthand alias for `lookup` — a function, not a variable. Another
	// false positive the first run produced, and the reason the limits above are written down.
	"q": true,
	// Loop and block constructs.
	"item": true, "ansible_loop": true, "ansible_loop_var": true, "ansible_index_var": true,
	// Stratt's own write-back convention (ADR-0084): the play SETS this, never reads it unset.
	"stratt_facets": true,
}

// TestNoUnboundPlayVariables asserts every Ansible variable a shipped play references is
// supplied by its Step — via extraVars (usually mapped from a declared launch input), or
// registered/set by an earlier task in the same play.
func TestNoUnboundPlayVariables(t *testing.T) {
	trees := map[string]string{"reference": estateRoot}
	dirs := demoEstates(t)
	for _, dir := range dirs {
		trees["demo/"+demoLabel(dir)] = dir
	}
	checked := 0
	for label, dir := range trees {
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		decls, err := ParseDir(dir, nil)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		// A play now usually lives in the Actuator's resolved content (ADR-0134), so the
		// guard needs the Actuator a Step names to find the file it runs.
		content := map[string]types.Actuator{}
		for _, a := range decls.Actuators {
			content[a.Name] = a
		}
		for _, wf := range decls.Workflows {
			for _, st := range wf.Steps {
				n, errs := unboundInStep(content, st)
				checked += n
				for _, e := range errs {
					t.Errorf("%s: workflow %q step %q: %s", label, wf.Name, st.Name, e)
				}
			}
		}
		for _, tr := range decls.Triggers {
			n, errs := unboundInParams(content, tr.Actuator, tr.Params)
			checked += n
			for _, e := range errs {
				t.Errorf("%s: trigger %q: %s", label, tr.Name, e)
			}
		}
	}
	// A guard that passes by inspecting nothing is worse than no guard: it reports safety it
	// never established. The reference estate has ansible plays, so this must find some.
	if checked == 0 {
		t.Fatal("found no play variables to check at all — the guard is not reaching the plays it claims to cover")
	}
}

func unboundInStep(content map[string]types.Actuator, st types.Step) (int, []string) {
	return unboundInParams(content, st.Actuator, st.Params)
}

// playOf resolves the Ansible a Step actually runs. Two sources, and the guard has to cover
// both or it silently stops guarding: `playbook` names a file in the Actuator's content root
// (ADR-0134, the normal case), and `play` is still an inline string for the short guard plays
// D4 deliberately kept.
func playOf(content map[string]types.Actuator, actuator string, params map[string]any) string {
	if playbook, _ := params["playbook"].(string); playbook != "" {
		return content[actuator].Content[playbook]
	}
	play, _ := params["play"].(string)
	return play
}

// includedRole matches `include_role:` / `import_role:` followed by its `name:`. Used to pull the
// role's own task files into the scanned text — see scopeOf.
var includedRole = regexp.MustCompile(`(?m)(?:include_role|import_role):\s*\n\s+name:\s*([a-zA-Z_][a-zA-Z0-9_]*)`)

// varsDirRef matches a literal `vars/<dir>` path the play names, which is how a play points the
// shared role at its per-distro layout files.
var varsDirRef = regexp.MustCompile(`\bvars/([a-zA-Z0-9_.-]+)`)

// topLevelKeys pulls the `name:` keys from a flat YAML mapping — a vars or defaults file.
var topLevelKeys = regexp.MustCompile(`(?m)^([a-zA-Z_][a-zA-Z0-9_]*):`)

// scopeOf assembles everything that actually EXECUTES for a Step, and everything that supplies a
// variable to it, out of the Actuator's content root.
//
// It exists because factoring must not be a way out of this guard. When the apache/tomcat/nginx
// plays moved their shared ends into roles/stratt_web_service, the tasks that had been under this
// check — including the whole observe/fact-back tail, which is where ANS-014's defect lived — left
// the file the guard reads. A guard that only ever reads the top-level play rewards moving risky
// logic into a role. So `include_role`/`import_role` is followed, and the role's task files are
// scanned as part of the play that includes them, which is exactly what ansible does.
//
// The same reasoning covers `include_vars`: the per-distro layout files supply real variables, so
// their keys are bound. Rather than resolving a templated include path (`{{ svc_layout_dir }}/{{
// ansible_facts.os_family }}.yml`, which is not statically resolvable), it takes the `vars/<dir>`
// literals the play NAMES and binds the keys of the files under them. That keeps the scope to
// directories this play points at rather than every vars file in the root — an apache play does not
// get tomcat's variables for free.
func scopeOf(a types.Actuator, play string) (text string, supplied map[string]bool) {
	supplied = map[string]bool{}
	text = play

	for _, m := range includedRole.FindAllStringSubmatch(play, -1) {
		prefix := "roles/" + m[1] + "/"
		for path, body := range a.Content {
			if !strings.HasPrefix(path, prefix) {
				continue
			}
			// A role's defaults ARE bindings; its tasks are code to be checked.
			if strings.HasPrefix(path, prefix+"defaults/") || strings.HasPrefix(path, prefix+"vars/") {
				for _, km := range topLevelKeys.FindAllStringSubmatch(body, -1) {
					supplied[km[1]] = true
				}
				continue
			}
			text += "\n" + body
		}
	}

	// group_vars/ and host_vars/ ARE bindings, and this arm exists because of ADR-0161. Ansible loads
	// group_vars/<group>.yml automatically for every group in the inventory — but Stratt rendered NO
	// groups until ADR-0161, so those files could never be loaded and nothing here needed to know
	// about them. Now that a Step can declare `groupBy` and the inventory carries sections, a
	// variable defined there is genuinely supplied, and a guard that did not know it would refuse
	// exactly the plays ADR-0161 exists to make runnable.
	//
	// Deliberately NOT matched against the Step's declared groups: ansible resolves these by NAME at
	// run time from whatever groups exist, and a build-time guard that tried to predict the set would
	// be asserting a fact about the graph it cannot see (§1.2). The question here is only "does some
	// declaration in this project supply this variable", which is what the roles/ arms above ask too.
	for path, body := range a.Content {
		if !strings.HasPrefix(path, "group_vars/") && !strings.HasPrefix(path, "host_vars/") {
			continue
		}
		for _, km := range topLevelKeys.FindAllStringSubmatch(body, -1) {
			supplied[km[1]] = true
		}
	}

	for _, m := range varsDirRef.FindAllStringSubmatch(text, -1) {
		prefix := "vars/" + m[1] + "/"
		for path, body := range a.Content {
			if !strings.HasPrefix(path, prefix) {
				continue
			}
			for _, km := range topLevelKeys.FindAllStringSubmatch(body, -1) {
				supplied[km[1]] = true
			}
		}
	}
	return text, supplied
}

func unboundInParams(content map[string]types.Actuator, actuator string, params map[string]any) (int, []string) {
	play := playOf(content, actuator, params)
	if play == "" {
		return 0, nil
	}
	play, supplied := scopeOf(content[actuator], play)
	if ev, ok := params["extraVars"].(map[string]any); ok {
		for k := range ev {
			supplied[k] = true
		}
	}
	// Names the play defines itself, mid-play: `register: x` and `set_fact:` keys.
	for _, m := range regexp.MustCompile(`(?m)^\s*register:\s*([a-zA-Z_][a-zA-Z0-9_]*)`).FindAllStringSubmatch(play, -1) {
		supplied[m[1]] = true
	}
	for _, m := range regexp.MustCompile(`(?m)^\s*(?:ansible\.builtin\.)?set_fact:\s*$`).FindAllStringIndex(play, -1) {
		// Keys of the set_fact block that follows: one indent level deeper, `name: value`.
		rest := play[m[1]:]
		for _, km := range regexp.MustCompile(`(?m)^\s+([a-zA-Z_][a-zA-Z0-9_]*):`).FindAllStringSubmatch(rest, -1) {
			supplied[km[1]] = true
		}
	}
	// `vars:` blocks anywhere in the play.
	for _, m := range regexp.MustCompile(`(?m)^\s*vars:\s*$`).FindAllStringIndex(play, -1) {
		rest := play[m[1]:]
		for _, km := range regexp.MustCompile(`(?m)^\s+([a-zA-Z_][a-zA-Z0-9_]*):`).FindAllStringSubmatch(rest, -1) {
			supplied[km[1]] = true
		}
	}

	var unbound []string
	seen := map[string]bool{}
	refs := 0
	for _, m := range jinjaRef.FindAllStringSubmatch(play, -1) {
		name, chain := m[1], m[2]
		refs++
		if playLocal[name] || supplied[name] || seen[name] || defaulted.MatchString(chain) {
			continue
		}
		seen[name] = true
		unbound = append(unbound, name)
	}
	sort.Strings(unbound)
	var errs []string
	for _, name := range unbound {
		errs = append(errs, "play references {{ "+name+" }}, which nothing supplies — "+
			"map it in extraVars (from a declared launch input), or set it in the play. "+
			"Stratt never sees Ansible variables, so an unsupplied one fails at run time "+
			"on an undefined variable with nothing in the build to catch it")
	}
	_ = strings.TrimSpace
	return refs, errs
}
