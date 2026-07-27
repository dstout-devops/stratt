// Package contract loads, pins, and evaluates the platform's Contract and
// Facet-schema documents (charter §1.5, §2.2): JSON Schema as data,
// validated by a standard validator (santhosh-tekuri/jsonschema, scouted
// RECOMMEND — ADR-0015), never language classes. Schema drift against a
// registered pin is blocking.
package contract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"github.com/dstout-devops/stratt/contracts"
	"github.com/dstout-devops/stratt/core/internal/template"
	"github.com/dstout-devops/stratt/types"
)

type compiled struct {
	contract types.Contract
	schema   *jsonschema.Schema
}

var (
	once      sync.Once
	loadErr   error
	byName    map[string]*compiled
	ordered   []types.Contract
	facetSet  map[string]*compiled // facet namespace → schema
	intentSet map[string]*compiled // intent kind (Intent/Certificate) → spec schema
)

// load parses, hashes, and compiles every embedded document exactly once.
func load() {
	byName = map[string]*compiled{}
	facetSet = map[string]*compiled{}
	intentSet = map[string]*compiled{}
	compiler := jsonschema.NewCompiler()

	var paths []string
	_ = fs.WalkDir(contracts.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".schema.json") {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)

	for _, path := range paths {
		raw, err := fs.ReadFile(contracts.FS, path)
		if err != nil {
			loadErr = fmt.Errorf("contract: read %s: %w", path, err)
			return
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			loadErr = fmt.Errorf("contract: parse %s: %w", path, err)
			return
		}
		if err := compiler.AddResource(path, doc); err != nil {
			loadErr = fmt.Errorf("contract: add %s: %w", path, err)
			return
		}
		sch, err := compiler.Compile(path)
		if err != nil {
			loadErr = fmt.Errorf("contract: compile %s: %w", path, err)
			return
		}
		name := strings.TrimSuffix(path, ".schema.json")
		// Version bumps are sibling files: os.kernel.v2.schema.json is
		// version 2 of facets/os.kernel — same name, new pin row (ADR-0015).
		version := 1
		if i := strings.LastIndex(name, ".v"); i > 0 {
			if n, err := strconv.Atoi(name[i+2:]); err == nil && n > 0 {
				name, version = name[:i], n
			}
		}
		sum := sha256.Sum256(raw)
		c := &compiled{
			contract: types.Contract{
				Name:    name,
				Version: version,
				Rung:    types.RungHandWritten,
				Hash:    hex.EncodeToString(sum[:]),
				Schema:  raw,
			},
			schema: sch,
		}
		byName[name] = c
		ordered = append(ordered, c.contract)
		if ns, ok := strings.CutPrefix(name, "facets/"); ok {
			facetSet[ns] = c
		}
		if base, ok := strings.CutPrefix(name, "intents/"); ok {
			intentSet[intentKindFromFile(base)] = c
		}
	}
}

// intentKindFromFile maps an intents/<base>.schema.json basename to its Named
// Kind (charter §2.4): "certificate" → "Intent/Certificate". Filenames are
// lowercase because a kind's slash cannot live in a path. Multi-word kinds
// whose canonical spelling is not a simple first-letter capitalization (the
// frozen §2 vocabulary, e.g. "FileSet") are mapped explicitly — the spelling is
// API and must round-trip exactly.
func intentKindFromFile(base string) string {
	if base == "" {
		return ""
	}
	if kind, ok := intentKindSpelling[base]; ok {
		return kind
	}
	return "Intent/" + strings.ToUpper(base[:1]) + base[1:]
}

// intentKindSpelling pins the exact Named-Kind spelling for intent filenames
// that are not a plain first-letter capitalization (§2 vocabulary is frozen).
var intentKindSpelling = map[string]string{
	"fileset":   "Intent/FileSet",
	"dnsrecord": "Intent/DnsRecord",
}

func ensure() error {
	once.Do(load)
	return loadErr
}

// All returns every embedded Contract (documents + pins), name-ordered.
func All() ([]types.Contract, error) {
	if err := ensure(); err != nil {
		return nil, err
	}
	return ordered, nil
}

var (
	fpOnce sync.Once
	fpVal  string
	fpErr  error
)

// Fingerprint is a single sha256 over the sorted (name, version, hash) triples
// of the pinned registry (ADR-0044 slice 4) — a cheap version stamp a Cell
// advertises so the federation router can BLOCK a merge with a peer on a
// divergent Contract/Facet registry (§1.5: schema drift is blocking, never a
// silent union). Computed once (the booted set is authoritative — boot aborts
// on drift), memoized.
func Fingerprint() (string, error) {
	fpOnce.Do(func() {
		all, err := All()
		if err != nil {
			fpErr = err
			return
		}
		h := sha256.New()
		for _, c := range all {
			fmt.Fprintf(h, "%s\t%d\t%s\n", c.Name, c.Version, c.Hash)
		}
		fpVal = hex.EncodeToString(h.Sum(nil))
	})
	return fpVal, fpErr
}

// Get returns one Contract by name (e.g. "actuators/script.input").
func Get(name string) (types.Contract, bool, error) {
	if err := ensure(); err != nil {
		return types.Contract{}, false, err
	}
	c, ok := lookup(name)
	if !ok {
		return types.Contract{}, false, nil
	}
	return c.contract, true, nil
}

// ValidationError carries the schema violation with JSON-pointer locations —
// diagnosis is never hidden (§1.8).
type ValidationError struct {
	Contract string
	Detail   string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("does not satisfy contract %s: %s", e.Contract, e.Detail)
}

// validate evaluates raw JSON against a compiled schema.
func (c *compiled) validate(raw []byte) error {
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return &ValidationError{Contract: c.contract.Name, Detail: "not valid JSON: " + err.Error()}
	}
	if err := c.schema.Validate(inst); err != nil {
		verr, ok := err.(*jsonschema.ValidationError)
		if !ok {
			return &ValidationError{Contract: c.contract.Name, Detail: err.Error()}
		}
		return &ValidationError{Contract: c.contract.Name, Detail: flatten(verr)}
	}
	return nil
}

// flatten renders the causes as "/json/pointer: message" lines.
func flatten(v *jsonschema.ValidationError) string {
	leaves := v.Causes
	if len(leaves) == 0 {
		leaves = []*jsonschema.ValidationError{v}
	}
	printer := message.NewPrinter(language.English)
	parts := make([]string, 0, len(leaves))
	for _, c := range leaves {
		loc := "/" + strings.Join(c.InstanceLocation, "/")
		parts = append(parts, fmt.Sprintf("%s: %s", loc, c.ErrorKind.LocalizedString(printer)))
	}
	return strings.Join(parts, "; ")
}

// ValidateActuatorParams checks Step params against the Actuator's input
// Contract. Actuators without a registered Contract are refused — an
// uncontracted Step surface must not exist (§2.3).
func ValidateActuatorParams(actuator string, params json.RawMessage) error {
	return ValidateActuatorParamsFor(actuator, "", params)
}

// ValidateActuatorParamsFor is ValidateActuatorParams with the Actuator's PLUGIN
// IDENTITY available, which is what makes a second declaration of the same plugin
// usable at all.
//
// An input Contract belongs to the TOOL, not to the local name an estate gives one
// of its Actuators (§1.5 — the plugin's declared seam is the contract). Resolution
// by name alone meant per-Step EE selection (ADR-0117 D3a) worked in the dispatcher
// and was unreachable from an estate: declaring `ansible-crypto` (pluginIdentity
// ansible) to select a content-bearing EE produced an Actuator whose every Step was
// rejected at parse time as uncontracted — the mechanism shipped, the thing it
// existed for did not. Found by the app-cert demo, which is what a demo is for.
//
// Name is still tried FIRST, so a plugin that ships a contract under a specific
// Actuator name keeps it; the identity is a fallback, never an override.
func ValidateActuatorParamsFor(actuator, pluginIdentity string, params json.RawMessage) error {
	if err := ensure(); err != nil {
		return err
	}
	c, ok := lookup("actuators/" + actuator + ".input")
	if !ok && pluginIdentity != "" && pluginIdentity != actuator {
		c, ok = lookup("actuators/" + pluginIdentity + ".input")
	}
	if !ok {
		if pluginIdentity != "" && pluginIdentity != actuator {
			return fmt.Errorf("contract: no input contract for actuator %q (nor for its plugin identity %q)",
				actuator, pluginIdentity)
		}
		return fmt.Errorf("contract: no input contract for actuator %q", actuator)
	}
	if len(params) == 0 {
		params = []byte(`{}`)
	}
	return c.validate(params)
}

// ValidateActionInput checks an Action's params against its input Contract
// (charter §2.2: an Action declares an input Contract, ADR-0031). An Action
// with no input contract is refused — an uncontracted operation must not exist.
func ValidateActionInput(action string, params json.RawMessage) error {
	if err := ensure(); err != nil {
		return err
	}
	c, ok := lookup("actions/" + action + ".input")
	if !ok {
		return fmt.Errorf("contract: no input contract for action %q", action)
	}
	if len(params) == 0 {
		params = []byte(`{}`)
	}
	return c.validate(params)
}

// ValidateActionOutput checks an Action's produced outputs against its output
// Contract (§2.2: an Action declares an OUTPUT Contract — the direction that
// makes an Action more than an Actuator). An Action with no output contract is
// refused. Dry-run plans are not validated here (a plan is not the contracted
// output); the caller skips this for dryRun (ADR-0031).
func ValidateActionOutput(action string, outputs json.RawMessage) error {
	if err := ensure(); err != nil {
		return err
	}
	c, ok := lookup("actions/" + action + ".output")
	if !ok {
		return fmt.Errorf("contract: no output contract for action %q", action)
	}
	if len(outputs) == 0 {
		outputs = []byte(`{}`)
	}
	return c.validate(outputs)
}

// ValidateNamed validates a document against a pinned Contract by its exact name (e.g. a
// class-level capability Contract like "capabilities/statestore.output", ADR-0105 D3 — used when
// the core reconciles a capability provider's resolve-Action output against the CLASS-level shape
// rather than a plugin-scoped one). An unknown name is refused (a capability with no pinned
// Contract must not resolve — §1.5).
func ValidateNamed(name string, doc json.RawMessage) error {
	if err := ensure(); err != nil {
		return err
	}
	c, ok := lookup(name)
	if !ok {
		return fmt.Errorf("contract: no pinned contract %q", name)
	}
	if len(doc) == 0 {
		doc = []byte(`{}`)
	}
	return c.validate(doc)
}

// ResolveActionParams binds a launch-time param map's {{.ns.x}} templates
// (ADR-0024/0031 cross-Step binding) then re-validates against the Action's
// input Contract — the Action counterpart of ResolveActuatorParams.
func ResolveActionParams(action string, params map[string]any, ns template.Namespaces) (json.RawMessage, error) {
	return resolveParamsAgainst(params, ns, func(raw json.RawMessage) error {
		return ValidateActionInput(action, raw)
	})
}

// ResolveCapabilityParams is ResolveActionParams for a Step that names a capability CLASS
// rather than an Action (ADR-0140 D3 row 2). The params are validated against the CLASS
// Contract — `capabilities/<class>.input` — never the resolved provider's own Action Contract.
//
// That is the whole point of writing the Step against a class: the author wrote
// provider-agnostic params, and validating them against whichever provider happens to be bound
// would make the Step's validity depend on the binding. It is also what ADR-0112 D2 already does
// on the `requires:` path, where the class Contract governs a capability call's output.
func ResolveCapabilityParams(class string, params map[string]any, ns template.Namespaces) (json.RawMessage, error) {
	return resolveParamsAgainst(params, ns, func(raw json.RawMessage) error {
		return ValidateNamed(CapabilityInput(class), raw)
	})
}

// CapabilityInput/CapabilityOutput name a capability class's Contracts. One spelling, so the
// convention lives in one place rather than being re-concatenated at each call site.
func CapabilityInput(class string) string  { return "capabilities/" + class + ".input" }
func CapabilityOutput(class string) string { return "capabilities/" + class + ".output" }

func resolveParamsAgainst(params map[string]any, ns template.Namespaces, validate func(json.RawMessage) error) (json.RawMessage, error) {
	resolved, err := template.SubstituteParams(params, ns)
	if err != nil {
		return nil, err
	}
	raw := json.RawMessage(`{}`)
	if resolved != nil {
		if raw, err = json.Marshal(resolved); err != nil {
			return nil, err
		}
	}
	if err := validate(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// ResolveActuatorParams binds a launch-time param map's {{.ns.x}} templates
// (ADR-0024), then re-validates the resolved params against the Actuator's
// input Contract and returns the JSON the Actuator receives. This moves a
// template-dependent field's validation from declaration time to launch —
// the resolved value, not the placeholder, is what must satisfy the schema —
// while guaranteeing the Actuator never sees unvalidated params (§1.5, §1.8).
func ResolveActuatorParams(actuator string, params map[string]any, ns template.Namespaces) (json.RawMessage, error) {
	return ResolveActuatorParamsFor(actuator, "", params, ns)
}

// ResolveActuatorParamsFor is ResolveActuatorParams with the Actuator's plugin
// identity available — see ValidateActuatorParamsFor for why a Contract cannot be
// resolved by the local Actuator name alone.
func ResolveActuatorParamsFor(actuator, pluginIdentity string, params map[string]any, ns template.Namespaces) (json.RawMessage, error) {
	resolved, err := template.SubstituteParams(params, ns)
	if err != nil {
		return nil, err
	}
	raw := json.RawMessage(`{}`)
	if resolved != nil {
		if raw, err = json.Marshal(resolved); err != nil {
			return nil, err
		}
	}
	if err := ValidateActuatorParamsFor(actuator, pluginIdentity, raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// ValidateDocument evaluates an instance against a schema document that is
// not embedded — e.g. a DB-pinned rung-2/3 Contract (ADR-0022). The schema
// compiles ad hoc; contractName only labels the error (§1.8 pointer detail).
func ValidateDocument(contractName string, schema, instance json.RawMessage) error {
	compiler := jsonschema.NewCompiler()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return &ValidationError{Contract: contractName, Detail: "schema is not valid JSON: " + err.Error()}
	}
	if err := compiler.AddResource("schema.json", doc); err != nil {
		return &ValidationError{Contract: contractName, Detail: "schema: " + err.Error()}
	}
	sch, err := compiler.Compile("schema.json")
	if err != nil {
		return &ValidationError{Contract: contractName, Detail: "schema does not compile: " + err.Error()}
	}
	c := &compiled{contract: types.Contract{Name: contractName}, schema: sch}
	return c.validate(instance)
}

// ValidateFacet checks a Facet value when its namespace has a pinned schema.
// covered=false means no schema exists for the namespace — allowed by
// design: a Facet schema may exist only when a shipping Contract demands it
// (§1.1); absence is not an error.
func ValidateFacet(namespace string, value json.RawMessage) (covered bool, err error) {
	if err := ensure(); err != nil {
		return false, err
	}
	c, ok := facetSet[namespace]
	if !ok {
		return false, nil
	}
	return true, c.validate(value)
}

// HasIntentKind reports whether an Intent kind has a registered spec schema —
// the definition of "implemented" (§1.1). Used to gate Blueprints without
// validating a spec.
func HasIntentKind(kind string) (bool, error) {
	if err := ensure(); err != nil {
		return false, err
	}
	_, ok := intentSet[kind]
	return ok, nil
}

// ValidateIntentSpec checks an Intent's spec against its kind's schema
// (charter §2.4: each Intent kind has a schema driving forms/validation).
// This is the first place an Intent payload is typed at its seam (§1.1) —
// covered=false means the kind has no registered spec schema, which the caller
// treats as "kind not implemented" rather than "anything goes".
func ValidateIntentSpec(kind string, spec json.RawMessage) (covered bool, err error) {
	if err := ensure(); err != nil {
		return false, err
	}
	c, ok := intentSet[kind]
	if !ok {
		return false, nil
	}
	if len(spec) == 0 {
		spec = []byte(`{}`)
	}
	return true, c.validate(spec)
}

// ValidateIntentSpecPartial validates a PARTIAL Intent spec — e.g. a Blueprint's
// `defaults`, which need not be a complete spec — against the composed kind's Contract.
// Every field that IS present must satisfy the schema (type, enum, additionalProperties,
// …); a missing top-level `required` field is TOLERATED, since defaults supply a subset
// the Intent completes. This closes the §1.1 seam for author-supplied default values
// without demanding a whole spec (ADR-0083 §5). covered=false ⇒ the kind has no Contract
// (nothing to check).
//
// As of ADR-0118 D1 this validates an INTENT's own spec too, not just Blueprint defaults:
// with values spread across defaults, the Intent and the Assignment, no single layer is a
// complete spec. The "full resolved-spec revalidation at compile" this doc used to book as
// a follow-up now exists — compiler.validateResolvedSpec — and it is the ONLY place
// completeness is judged, so this function must never be mistaken for one that guarantees
// a usable spec.
func ValidateIntentSpecPartial(kind string, spec json.RawMessage) (covered bool, err error) {
	if err := ensure(); err != nil {
		return false, err
	}
	c, ok := intentSet[kind]
	if !ok {
		return false, nil
	}
	if len(spec) == 0 {
		return true, nil // no defaults ⇒ nothing to validate
	}
	stripped, err := stripTopRequired(c.contract.Schema)
	if err != nil {
		return true, fmt.Errorf("contract: partial schema for %s: %w", kind, err)
	}
	return true, ValidateDocument(c.contract.Name, stripped, spec)
}

// stripTopRequired returns the schema with its top-level `required` array removed, so a
// partial instance validates present fields without failing on absent required ones.
func stripTopRequired(schema json.RawMessage) (json.RawMessage, error) {
	var doc map[string]any
	if err := json.Unmarshal(schema, &doc); err != nil {
		return nil, err
	}
	delete(doc, "required")
	return json.Marshal(doc)
}

// ── estate-resident self contracts (ADR-0138 D3/D4) ──────────────────────────
//
// A SELF contract describes only its own plugin — `actuators/<plugin>.input.vN`,
// `actions/<plugin>/<verb>.input|output`. Nothing else reads it; it constrains one Actuator's
// Steps. Such a plugin cannot shadow another's contract and cannot satisfy one, so ADR-0047 §4's
// threat model is not engaged and residence may follow ownership.
//
// A SEAM contract — `capabilities/`, `facets/`, `intents/`, `policy/`, `outputs/` — describes what
// one module may rely on another to MEAN. Those stay core-shipped permanently, not as migration
// debt but as the definition of a seam (§1.5; ADR-0104 D1's "a plugin never mints a capability's
// meaning", pointed the other way). RegisterEstate refuses one outright.
//
// WHERE THIS IS CALLED FROM IS LOAD-BEARING. Registration happens in strattd's BOOT-TIME estate
// parse, which runs on EVERY replica and before the API handler is built. Both matter:
//
//   - The desired-state controller is LEADER-ONLY, so registering during its reconcile would give
//     the leader a contract set its followers lack — and the Temporal worker runs on every
//     replica, validating action params at dispatch. That is the ADR-0103 D3 routing hazard,
//     reproduced in the validation layer.
//   - Fingerprint() is captured ONCE in api.Server.Handler() as the cross-Cell federation
//     RegistryVersion, so a later registration would ship peers a stale value.
//
// Fingerprint() and All() deliberately keep covering the SHIPPED set only. Federation compares
// whether two Cells present one logical estate, which is a question about SEAMS — a peer that does
// not run helm must not be rejected over helm's param schema. D3's own distinction answers it.

var (
	estateMu  sync.RWMutex
	estateSet = map[string]*compiled{}
)

// seamPrefixes are the contract families a plugin may never ship. Everything else is self.
var seamPrefixes = []string{"capabilities/", "facets/", "intents/", "policy/", "outputs/"}

// RegisterEstate adds a plugin's own self contracts, keyed by contract name (no `.schema.json`).
// owner is the plugin whose estate shipped them, used to enforce that a plugin ships only its OWN.
//
// Idempotent by content: re-registering identical bytes is a no-op, so repeated parses are safe.
// The SAME name with DIFFERENT bytes is refused — that is drift, and §1.5 says drift is blocking,
// never silently absorbed.
func RegisterEstate(owner string, docs map[string][]byte) error {
	if err := ensure(); err != nil {
		return err
	}
	names := make([]string, 0, len(docs))
	for n := range docs {
		names = append(names, n)
	}
	sort.Strings(names)

	estateMu.Lock()
	defer estateMu.Unlock()
	for _, name := range names {
		raw := docs[name]
		if err := checkSelfContract(owner, name); err != nil {
			return err
		}
		// A plugin must not shadow a core-shipped document. This is §4's actual threat, and it is
		// the one check that must not be relaxed for convenience.
		if _, shipped := byName[name]; shipped {
			return fmt.Errorf("contract: plugin %q ships %q, which is core-shipped — a plugin may own its "+
				"own self contracts, never shadow a shipped one (ADR-0047 §4, ADR-0138 D3)", owner, name)
		}
		sum := sha256.Sum256(raw)
		hash := hex.EncodeToString(sum[:])
		// VERSION SIBLINGS are legitimate and must behave exactly as the shipped loader does:
		// `ansible.input.v7` is version 7 of `actuators/ansible.input`, a sibling FILE under the
		// same name, and the highest version is the live one. Names are walked in sorted order so
		// the highest lands last — mirroring load()'s own overwrite rather than inventing a second
		// rule for estate-resident documents.
		base, version := name, 1
		if i := strings.LastIndex(base, ".v"); i > 0 {
			if n, err := strconv.Atoi(base[i+2:]); err == nil && n > 0 {
				base, version = base[:i], n
			}
		}
		if prior, ok := estateSet[base]; ok {
			switch {
			case prior.contract.Version == version && prior.contract.Hash == hash:
				continue // identical bytes at the same version; a repeated parse is a no-op
			case prior.contract.Version == version:
				return fmt.Errorf("contract: %q v%d registered twice with different content (%s vs %s) — schema "+
					"drift is blocking, never silently absorbed (§1.5)", base, version, prior.contract.Hash[:12], hash[:12])
			case prior.contract.Version > version:
				continue // a higher version already won
			}
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			return fmt.Errorf("contract: parse %s (plugin %s): %w", name, owner, err)
		}
		compiler := jsonschema.NewCompiler()
		res := name + ".schema.json"
		if err := compiler.AddResource(res, doc); err != nil {
			return fmt.Errorf("contract: add %s: %w", name, err)
		}
		sch, err := compiler.Compile(res)
		if err != nil {
			return fmt.Errorf("contract: compile %s (plugin %s): %w", name, owner, err)
		}
		estateSet[base] = &compiled{
			contract: types.Contract{
				Name: base, Version: version, Rung: types.RungHandWritten,
				Hash: hash, Schema: raw,
			},
			schema: sch,
		}
	}
	return nil
}

// checkSelfContract enforces D3's boundary: a plugin ships only contracts about ITSELF.
func checkSelfContract(owner, name string) error {
	for _, p := range seamPrefixes {
		if strings.HasPrefix(name, p) {
			return fmt.Errorf("contract: plugin %q ships %q, which is a SEAM contract — %s documents describe "+
				"what one module may rely on another to MEAN, so they are core's permanently. A plugin may own "+
				"only its own actuators/ and actions/ documents (ADR-0138 D3)", owner, name, p)
		}
	}
	switch {
	case strings.HasPrefix(name, "actions/"):
		// actions/<plugin>/<verb>.input — the plugin segment must be the owner's.
		rest := strings.TrimPrefix(name, "actions/")
		seg, _, ok := strings.Cut(rest, "/")
		if !ok {
			return fmt.Errorf("contract: %q is not a well-formed action contract (want actions/<plugin>/<verb>.input)", name)
		}
		if seg != owner {
			return fmt.Errorf("contract: plugin %q ships %q, which belongs to %q — a plugin owns its OWN self "+
				"contracts and no one else's (ADR-0138 D3)", owner, name, seg)
		}
	case strings.HasPrefix(name, "actuators/"):
		// actuators/<tool>.input[.vN] — the tool must be the owner's.
		base := strings.TrimPrefix(name, "actuators/")
		tool, _, _ := strings.Cut(base, ".")
		if tool != owner {
			return fmt.Errorf("contract: plugin %q ships %q, which belongs to %q — a plugin owns its OWN self "+
				"contracts and no one else's (ADR-0138 D3)", owner, name, tool)
		}
	default:
		return fmt.Errorf("contract: plugin %q ships %q, which is neither an actuators/ nor an actions/ "+
			"document — only those are self contracts (ADR-0138 D3)", owner, name)
	}
	return nil
}

// lookup resolves a contract by name across the shipped set and the estate-resident one. Shipped
// wins, though RegisterEstate refuses a shadow so the two can never actually overlap.
func lookup(name string) (*compiled, bool) {
	if c, ok := byName[name]; ok {
		return c, true
	}
	estateMu.RLock()
	defer estateMu.RUnlock()
	c, ok := estateSet[name]
	return c, ok
}

// EstateContracts returns the registered estate-resident documents, for pinning into the graph
// with blocking drift (D4 — "core stops embedding and instead pins at registration").
func EstateContracts() []types.Contract {
	estateMu.RLock()
	defer estateMu.RUnlock()
	out := make([]types.Contract, 0, len(estateSet))
	for _, c := range estateSet {
		out = append(out, c.contract)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ResetEstateForTest clears the estate-resident set. Tests parse many different trees through one
// process, and a registry that accumulated across them would make one test's estate visible to
// another — passing or failing for reasons neither test states.
func ResetEstateForTest() {
	estateMu.Lock()
	defer estateMu.Unlock()
	estateSet = map[string]*compiled{}
}
