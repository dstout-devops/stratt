package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/dstout-devops/stratt/types"
)

// CheckPackageAdvisories reads every host's software.package inventory and raises a
// patch/advisory Finding for each installed package an advisory affects (ADR-0080)
// — turning the deliverable-software dimension into patch/vulnerability remediation
// signal. Platform-computed (a derivation over projected facts, not an external
// observation), idempotent per (host, package, advisory). Remediation is a patch
// Action against the host (a package upgrade), never a graph edit (§1.2).
func (s *Store) CheckPackageAdvisories(ctx context.Context, advisories []types.PackageAdvisory) error {
	if len(advisories) == 0 {
		return nil
	}
	byPackage := map[string][]types.PackageAdvisory{}
	for _, a := range advisories {
		if a.Package == "" {
			continue
		}
		byPackage[a.Package] = append(byPackage[a.Package], a)
	}

	// Buffer host inventories before writing Findings (the write acquires its own conn).
	type hostInv struct {
		id  string
		raw []byte
	}
	var hosts []hostInv
	rows, err := s.pool.Query(ctx, `
		SELECT f.entity_id, f.value
		FROM graph.facet f
		JOIN graph.entity e ON e.id = f.entity_id
		WHERE f.namespace = 'software.package' AND e.deleted_at IS NULL`)
	if err != nil {
		return fmt.Errorf("package-advisory: read inventories: %w", err)
	}
	for rows.Next() {
		var h hostInv
		if err := rows.Scan(&h.id, &h.raw); err != nil {
			rows.Close()
			return fmt.Errorf("package-advisory: scan inventory: %w", err)
		}
		hosts = append(hosts, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("package-advisory: inventories: %w", err)
	}

	for _, h := range hosts {
		var inv struct {
			Packages []struct{ Name, Version string } `json:"packages"`
		}
		if err := json.Unmarshal(h.raw, &inv); err != nil {
			continue
		}
		for _, pkg := range inv.Packages {
			for _, adv := range byPackage[pkg.Name] {
				affected, assessable := advisoryAffects(adv, pkg.Version)
				if assessable && !affected {
					continue
				}
				target := fmt.Sprintf("%s/%s/%s", h.id, pkg.Name, adv.ID)
				if !assessable {
					// §1.8: a version we cannot confidently compare (epoch/tilde/
					// non-numeric — distro semantics this comparator does not yet
					// support) must NOT silently resolve to "not affected" — that is a
					// silent false-negative on a security advisory. Surface it loudly as
					// a Finding so it is triaged, never hidden.
					detail, _ := json.Marshal(map[string]any{
						"advisory":         adv.ID,
						"package":          pkg.Name,
						"installedVersion": pkg.Version,
						"fixedVersion":     adv.Fixed,
						"reason":           "could not assess this version against the advisory (epoch/tilde/non-numeric version — unsupported comparator); triage manually, do not assume safe",
					})
					if err := s.WriteGovernanceFinding(ctx, "patch/advisory", target+"/unassessable", "warning", "patch/advisory", detail); err != nil {
						return fmt.Errorf("package-advisory: unassessable finding %s: %w", target, err)
					}
					continue
				}
				detail, _ := json.Marshal(map[string]any{
					"advisory":         adv.ID,
					"title":            adv.Title,
					"package":          pkg.Name,
					"installedVersion": pkg.Version,
					"fixedVersion":     adv.Fixed,
					"remediation":      "upgrade the package on the host (a patch Action against the SoR) — never a graph edit (§1.2)",
				})
				// Idempotent per (host, package, advisory): one open Finding each.
				if err := s.WriteGovernanceFinding(ctx, "patch/advisory", target, findingSeverity(adv.Severity), "patch/advisory", detail); err != nil {
					return fmt.Errorf("package-advisory: finding %s: %w", target, err)
				}
			}
		}
	}
	return nil
}

// findingSeverity maps an advisory's CVSS-style severity onto the Finding's
// enum (info | warning | critical, migration 00010): critical/high ⇒ critical,
// low ⇒ info, everything else ⇒ warning.
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
// and whether the determination is trustworthy. An explicit AffectedVersions match
// is always assessable (exact string). A Fixed-version comparison is assessable
// only when BOTH versions are comparable() by the dotted-numeric comparator;
// otherwise assessable=false and the caller must surface it, never assume safe
// (§1.8 — no silent false-negative on a security advisory).
func advisoryAffects(adv types.PackageAdvisory, installed string) (affected, assessable bool) {
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
// else is flagged unassessable rather than silently mis-ranked.
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
// first comparator: numeric segments compared left-to-right, a missing segment is 0,
// a non-numeric segment compares lexically. Full distro semantics (epochs, rpm/dpkg
// tilde/rc ordering) is a follow-up — noted so the limitation is not silent (§1.8).
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
