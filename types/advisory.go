package types

// SoftwareAdvisory is one patch/vulnerability rule over the software dimension
// (ADR-0080): it targets a named software COMPONENT — a package, a container image,
// a chart — of ANY delivery form, because a CVE is form-agnostic. A component is
// affected when its version is below Fixed (upgrade-to-fix, the common case) OR
// appears in Affected (explicit versions/tags). The ruleset is compliance-as-data
// (ADR-0033 lineage) — WHO decides "vulnerable" stays DECLARABLE in the estate,
// never hardcoded — so it carries wire tags for the estate loader and the check to
// share one type.
type SoftwareAdvisory struct {
	ID        string   `json:"id" yaml:"id"`               // advisory / CVE id, e.g. "CVE-2022-3602"
	Component string   `json:"component" yaml:"component"` // affected component NAME — a package name, an image name, a chart name
	Fixed     string   `json:"fixed" yaml:"fixed"`         // affected if installed < Fixed (dotted-numeric compare); empty ⇒ ignore
	Affected  []string `json:"affected" yaml:"affected"`   // OR: explicit affected versions/tags
	Severity  string   `json:"severity" yaml:"severity"`   // critical | high | medium | low
	Title     string   `json:"title" yaml:"title"`         // human summary
}
