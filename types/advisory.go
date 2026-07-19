package types

// PackageAdvisory is one patch/vulnerability rule (ADR-0080): a package is affected
// when its installed version is below Fixed (upgrade-to-fix, the common patch case)
// OR appears in Affected (explicit versions). The ruleset is compliance-as-data
// (ADR-0033 lineage) — who decides "vulnerable" stays DECLARABLE in the estate,
// never hardcoded — so it carries wire tags for the estate loader and the graph
// check to share one type.
type PackageAdvisory struct {
	ID       string   `json:"id" yaml:"id"`             // advisory / CVE id, e.g. "CVE-2022-3602"
	Package  string   `json:"package" yaml:"package"`   // affected package name
	Fixed    string   `json:"fixed" yaml:"fixed"`       // affected if installed < Fixed (dotted-numeric compare); empty ⇒ ignore
	Affected []string `json:"affected" yaml:"affected"` // OR: explicit affected versions
	Severity string   `json:"severity" yaml:"severity"` // critical | high | medium | low
	Title    string   `json:"title" yaml:"title"`       // human summary
}
