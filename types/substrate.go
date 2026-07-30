package types

import "sort"

// Substrate names WHERE a provisioning provider builds — the coarse landscape it speaks to
// (ADR-0151 D1). It is a fact a PROVIDER declares about itself, in the same category as its
// `provides:` classes and its `identitySchemes:`.
//
// NOTHING ABOVE A PROVIDER MAY NAME ONE. An Intent, Blueprint, Assignment or View that mentions a
// substrate cannot migrate, because the name IS the coupling — the failure this constant set exists
// to make impossible to reach by accident. An `Intent/Application` is apache; it is never "apache
// on AWS". The substrate is chosen once, in a capability-binding, and changing that one line moves
// the whole topology (ADR-0151 D2).
//
// DELIBERATELY COARSE. These are landscapes, not products: "aws" covers EC2, floci and anything
// else speaking that API surface, because a provider's job is to hide the rest (§1.5). A set that
// grew a token per product would be a vendor list, and the estate would start meaning things by it.
const (
	SubstrateAWS        = "aws"
	SubstrateKubernetes = "kubernetes"
	SubstrateVSphere    = "vsphere"
	// SubstrateVM is the generic hypervisor-or-metal landscape for a provider that manages machines
	// without one of the named clouds beneath it.
	SubstrateVM = "vm"
)

// substrates is the closed set. Extending it is a core decision that ships with its first provider
// — the same rule capabilityClasses follows, and for the same reason: a plugin does not get to mint
// a landscape and have the estate mean something by it (§1.5, §2).
var substrates = map[string]bool{
	SubstrateAWS:        true,
	SubstrateKubernetes: true,
	SubstrateVSphere:    true,
	SubstrateVM:         true,
}

// ValidSubstrate reports whether tok is a known substrate.
func ValidSubstrate(tok string) bool { return substrates[tok] }

// Substrates returns the closed set, sorted — for a diagnosis that tells an author what WAS
// available rather than only that their token was not (§1.8).
func Substrates() []string {
	out := make([]string, 0, len(substrates))
	for s := range substrates {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
