package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/dstout-devops/stratt/types"
)

// CheckSoftwareAdvisories reads every entity's software.* inventory — packages,
// container images, charts: the whole deliverable-software dimension — and raises a
// patch/advisory Finding for each installed COMPONENT an advisory affects (ADR-0080).
// One check over the dimension, not one per form: a CVE fires identically whether
// the vulnerable component is an apt package or a base image. Platform-computed (a
// derivation over projected facts, not an external observation), idempotent per
// (entity, component, advisory). Remediation is a patch Action at the SoR (a package
// upgrade, an image bump), never a graph edit (§1.2).
func (s *Store) CheckSoftwareAdvisories(ctx context.Context, advisories []types.SoftwareAdvisory) error {
	if len(advisories) == 0 {
		return nil
	}
	byComponent := map[string][]types.SoftwareAdvisory{}
	for _, a := range advisories {
		if a.Component == "" {
			continue
		}
		byComponent[a.Component] = append(byComponent[a.Component], a)
	}

	// Buffer inventories before writing Findings (the write acquires its own conn).
	// Any software.* facet is a component list — the dimension's shared shape.
	type entityInv struct {
		id  string
		raw []byte
	}
	var invs []entityInv
	rows, err := s.pool.Query(ctx, `
		SELECT f.entity_id, f.value
		FROM graph.facet f
		JOIN graph.entity e ON e.id = f.entity_id
		WHERE f.namespace LIKE 'software.%' AND e.deleted_at IS NULL`)
	if err != nil {
		return fmt.Errorf("software-advisory: read inventories: %w", err)
	}
	for rows.Next() {
		var inv entityInv
		if err := rows.Scan(&inv.id, &inv.raw); err != nil {
			rows.Close()
			return fmt.Errorf("software-advisory: scan inventory: %w", err)
		}
		invs = append(invs, inv)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("software-advisory: inventories: %w", err)
	}

	for _, inv := range invs {
		for _, comp := range softwareComponents(inv.raw) {
			for _, adv := range byComponent[comp.Name] {
				affected, assessable := advisoryAffects(adv, comp.Version)
				if assessable && !affected {
					continue
				}
				target := fmt.Sprintf("%s/%s/%s", inv.id, comp.Name, adv.ID)
				if !assessable {
					// §1.8: a version we cannot confidently compare (epoch/tilde, a
					// non-numeric image tag like "latest") must NOT silently resolve to
					// "not affected" — a silent false-negative on a security advisory.
					// Surface it loudly for triage, never hide it.
					detail, _ := json.Marshal(map[string]any{
						"advisory":         adv.ID,
						"component":        comp.Name,
						"installedVersion": comp.Version,
						"fixedVersion":     adv.Fixed,
						"reason":           "could not assess this version/tag against the advisory (epoch/tilde/non-numeric — unsupported comparator); triage manually, do not assume safe",
					})
					if err := s.WriteGovernanceFinding(ctx, "patch/advisory", target+"/unassessable", "warning", "patch/advisory", detail); err != nil {
						return fmt.Errorf("software-advisory: unassessable finding %s: %w", target, err)
					}
					continue
				}
				detail, _ := json.Marshal(map[string]any{
					"advisory":         adv.ID,
					"title":            adv.Title,
					"component":        comp.Name,
					"installedVersion": comp.Version,
					"fixedVersion":     adv.Fixed,
					"remediation":      "upgrade the component at the SoR (a package upgrade / image bump) — never a graph edit (§1.2)",
				})
				// Idempotent per (entity, component, advisory): one open Finding each.
				if err := s.WriteGovernanceFinding(ctx, "patch/advisory", target, findingSeverity(adv.Severity), "patch/advisory", detail); err != nil {
					return fmt.Errorf("software-advisory: finding %s: %w", target, err)
				}
			}
		}
	}
	return nil
}

// softwareComponent is the shared shape of the software dimension: any software.*
// facet is a list of components, each a {name, version} of some delivery form.
type softwareComponent struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// softwareComponents extracts the component list from any software.* facet without
// knowing its form: the facet is a JSON object whose one array-valued property
// (packages / containers / charts) is the component list. This is the convention
// that makes the dimension one queryable surface across forms.
func softwareComponents(raw []byte) []softwareComponent {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	var out []softwareComponent
	for _, v := range obj {
		var comps []softwareComponent
		if err := json.Unmarshal(v, &comps); err != nil {
			continue // not the component array (a scalar/object property)
		}
		for _, c := range comps {
			if c.Name != "" {
				out = append(out, c)
			}
		}
	}
	return out
}

// findingSeverity maps an advisory's CVSS-style severity onto the Finding's enum
// (info | warning | critical, migration 00010): critical/high ⇒ critical, low ⇒
// info, everything else ⇒ warning.
func findingSeverity(advisory string) string {
	switch strings.ToLower(strings.TrimSpace(advisory)) {
	case "critical", "high":
		return "critical"
	case "low":
		return "info"
	default:
		return "warning"
	}
}

// advisoryAffects reports whether an installed version is affected by an advisory,
// and whether the determination is trustworthy. An explicit Affected match is always
// assessable (exact string). A Fixed comparison is assessable only when BOTH versions
// are comparable() by the dotted-numeric comparator; otherwise assessable=false and
// the caller must surface it, never assume safe (§1.8).
func advisoryAffects(adv types.SoftwareAdvisory, installed string) (affected, assessable bool) {
	for _, v := range adv.Affected {
		if v == installed {
			return true, true
		}
	}
	if adv.Fixed == "" {
		return false, true // no version comparison needed; the explicit list is authoritative
	}
	if !comparable(installed) || !comparable(adv.Fixed) {
		return false, false
	}
	return versionLess(installed, adv.Fixed), true
}

// comparable reports whether a version string is one the dotted-numeric comparator
// can rank with confidence: no epoch (`:`) or tilde (`~`) — Debian/RPM ordering the
// comparator does not implement — and every dotted segment a pure integer. Anything
// else (an image tag like "latest" or "1.20-alpine") is flagged unassessable rather
// than silently mis-ranked.
func comparable(v string) bool {
	if v == "" || strings.ContainsAny(v, ":~") {
		return false
	}
	for _, seg := range strings.Split(v, ".") {
		if _, err := strconv.Atoi(seg); err != nil {
			return false
		}
	}
	return true
}

// versionLess compares dotted-numeric versions (e.g. "3.0.5" < "3.0.7"). A pragmatic
// first comparator; full distro semantics (epochs, rpm/dpkg tilde/rc ordering) is a
// follow-up — unrankable versions are flagged unassessable upstream, never silent.
func versionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		var av, bv string
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		ai, aerr := strconv.Atoi(av)
		bi, berr := strconv.Atoi(bv)
		if aerr == nil && berr == nil {
			if ai != bi {
				return ai < bi
			}
			continue
		}
		if av != bv {
			return av < bv
		}
	}
	return false
}
