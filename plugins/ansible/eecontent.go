package ansible

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// ── the EE's own content, read back before a connection type is trusted ─────────────────────────
//
// FOUND BY MEASURING, and it would have shipped otherwise. ADR-0153 put `network_cli` and `netconf`
// in the Contract's enum. Neither is in ansible-core:
//
//	$ ansible-doc -t connection network_cli
//	[WARNING]: Error loading plugin 'ansible.netcommon.network_cli':
//	           No module named 'ansible_collections.ansible.netcommon'
//	[WARNING]: network_cli was not found
//
// So a Contract that ACCEPTS the value on an EE that cannot load the plugin is a declaration which
// passes review, passes the estate load, passes every unit test — and dies at connection time with a
// message naming a collection the estate never mentioned. That is precisely the failure
// ee/content/platform.requirements.yml was written about, one layer up:
//
//	Error loading plugin 'community.general.apk': No module named 'ansible_collections.community'
//	naming a collection the play never mentions.
//
// ADR-0153 D1's whole argument is that an enum must not admit a value the shim has never honored.
// Admitting one the RUNTIME cannot honor is the same defect through the back door, and closing only
// the front one would have been the "declared but never executed" shape this repo keeps paying for.
//
// ── WHY THE MANIFEST AND NOT A PROBE ────────────────────────────────────────────────────────────
// The obvious check is to shell out to `ansible-doc -t connection <type>` and test the exit code.
// MEASURED: it exits 0 for a plugin that does not exist, so the obvious check silently passes. The
// EE already publishes what it contains at /etc/stratt/ee-content.json — written by ee/content.py
// as "the run-visible manifest (§1.8) … what the image ACTUALLY contains, not a restatement of the
// request". Reading the image's own answer is deterministic, needs no subprocess, and is the same
// artifact an operator descending into the Run sees.

// eeContentManifest is the path ee/content.py writes (MANIFEST_PATH). Every EE has one — a
// contentless image writes an empty manifest rather than no manifest, so an ABSENT file means the
// image was not built by our pipeline, which is a different diagnosis and gets one.
const eeContentManifest = "/etc/stratt/ee-content.json"

// connectionCollections maps a connection type to the collection that provides its plugin. Only
// types that need one appear: ssh and local are ansible-core and are absent by design.
var connectionCollections = map[string]string{
	ConnNetworkCLI: "ansible.netcommon",
	ConnNetconf:    "ansible.netcommon",
}

// eeCollections reads the installed collection names out of the EE's content manifest.
func eeCollections(read func(string) ([]byte, error)) (map[string]string, error) {
	raw, err := read(eeContentManifest)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Collections []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"collections"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("EE content manifest %s is unreadable: %w", eeContentManifest, err)
	}
	out := map[string]string{}
	for _, c := range doc.Collections {
		out[c.Name] = c.Version
	}
	return out, nil
}

// requireConnectionCollection refuses a connection type whose plugin this EE cannot load, BEFORE
// the play runs (§1.8: the diagnosis names the collection, the image, and the fix — not a python
// import error naming a namespace the estate never wrote).
//
// It is deliberately NOT a general "is every collection present" check. The question here is narrow
// and answerable: this Step declared a connection type, that type needs exactly one collection, and
// the image says whether it has it. A broader content check would need to know what a play imports,
// which is the play-parsing ADR-0117 D6 keeps out of the runtime.
func requireConnectionCollection(typ string, read func(string) ([]byte, error)) error {
	want, needs := connectionCollections[typ]
	if !needs {
		return nil // ssh/local are ansible-core; there is nothing to check and nothing to fail on
	}
	have, err := eeCollections(read)
	if err != nil {
		// An unreadable manifest is NOT treated as "collection missing", and not as "present"
		// either. Guessing in either direction turns an image problem into a connection problem.
		return fmt.Errorf("connection.type %s needs collection %s, and this image's content "+
			"manifest could not be read to confirm it (%w) — an EE built outside our pipeline "+
			"publishes no manifest, so what it contains is unknown rather than adequate", typ, want, err)
	}
	if _, ok := have[want]; ok {
		return nil
	}
	return fmt.Errorf("connection.type %s requires the %s collection and this EE does not ship it "+
		"(installed: %s). ansible-core carries ssh, local, winrm and psrp — the netcommon connection "+
		"plugins live in a collection, so without it ansible fails at connect time naming a python "+
		"module your estate never mentioned. Build an EE variant that includes %s and select it from "+
		"the Actuator declaration (ADR-0117 D3), rather than adding it to the platform floor, which is "+
		"bounded to what the platform's OWN shipped content needs",
		typ, want, describeCollections(have), want)
}

// describeCollections renders the installed set for the diagnosis. Sorted, and it names the empty
// case explicitly: "installed: none" is an answer, "installed: " reads like a truncated message.
func describeCollections(have map[string]string) string {
	if len(have) == 0 {
		return "none"
	}
	names := make([]string, 0, len(have))
	for n, v := range have {
		names = append(names, n+" "+v)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// osReadFile is the production reader for requireConnectionCollection.
func osReadFile(path string) ([]byte, error) { return os.ReadFile(path) }
