package authz

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	yaml "go.yaml.in/yaml/v3"
)

// TupleAuthorizer evaluates the ADR-0009 v1 model over OpenFGA-shaped tuples
// loaded from a CaC manifest (authz/tuples.yaml in the declarations repo).
// Implemented semantics — deliberately the minimal subset of the v1 DSL so
// the OpenFGA swap stays mechanical:
//
//	org:    admin;  member = direct ∪ admin
//	team:   org;    admin = direct ∪ org.admin;  member = direct ∪ admin
//	credential_ref: owner_team; admin = direct ∪ owner_team.admin;
//	                reader = direct ∪ team#member usersets ∪ admin;
//	                user   = direct ∪ team#member usersets   (implies NOTHING)
//	view:   owner_team; admin = direct ∪ owner_team.admin;
//	                reader = direct ∪ team#member usersets ∪ admin;
//	                runner = direct ∪ team#member usersets ∪ admin  (§2.5
//	                View-scoped execution; NO org/team-admin bypass, ADR-0028)
type TupleAuthorizer struct {
	mu     sync.RWMutex
	tuples []Tuple
}

type Tuple struct {
	User     string `yaml:"user"`     // principal:<id> | team:<name>#member | org:<name> | team:<name>
	Relation string `yaml:"relation"` // admin | member | reader | user | owner_team | org
	Object   string `yaml:"object"`   // org:<n> | team:<n> | credential_ref:<n>
}

type tupleDoc struct {
	Tuples []Tuple `yaml:"tuples"`
}

// LoadTuples (re)loads the manifest at <root>/authz/tuples.yaml. A missing
// file loads an empty set — deny-everything is the safe default; a present
// but unparseable file is an error (never silently drop grants).
func (a *TupleAuthorizer) LoadTuples(root string) error {
	path := filepath.Join(root, "authz", "tuples.yaml")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		a.mu.Lock()
		a.tuples = nil
		a.mu.Unlock()
		return nil
	}
	if err != nil {
		return fmt.Errorf("authz: read tuples: %w", err)
	}
	var doc tupleDoc
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return fmt.Errorf("authz: %s: %w", path, err)
	}
	for i, t := range doc.Tuples {
		if t.User == "" || t.Relation == "" || t.Object == "" {
			return fmt.Errorf("authz: %s: tuple %d incomplete", path, i)
		}
	}
	a.mu.Lock()
	a.tuples = doc.Tuples
	a.mu.Unlock()
	return nil
}

// Snapshot returns the currently loaded tuple set — the desired state the
// OpenFGA projection syncs from (same CaC source, one read).
func (a *TupleAuthorizer) Snapshot() []Tuple {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]Tuple, len(a.tuples))
	copy(out, a.tuples)
	return out
}

// Check implements Authorizer for principal subjects.
func (a *TupleAuthorizer) Check(_ context.Context, principalID, relation, object string) (bool, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	subject := "principal:" + principalID
	return a.check(subject, relation, object, 0), nil
}

// check resolves relation membership with the fixed v1 model semantics.
// depth caps traversal (the model has no recursion beyond org→team→object,
// but a malformed tuple set must not loop).
func (a *TupleAuthorizer) check(subject, relation, object string, depth int) bool {
	if depth > 8 {
		return false
	}
	// Direct tuples and usersets.
	for _, t := range a.tuples {
		if t.Object != object || t.Relation != relation {
			continue
		}
		if t.User == subject {
			return true
		}
		// Userset: team:<name>#member grants to every team member.
		if obj, rel, ok := strings.Cut(t.User, "#"); ok {
			if a.check(subject, rel, obj, depth+1) {
				return true
			}
		}
	}
	// Implied relations (the v1 model, ADR-0009).
	objType, _, _ := strings.Cut(object, ":")
	switch {
	case relation == RelationMember && (objType == "org" || objType == "team"):
		// member = direct ∪ admin
		return a.check(subject, RelationAdmin, object, depth+1)
	case relation == RelationAdmin && objType == "team":
		// team admin = direct ∪ org admin
		for _, t := range a.tuples {
			if t.Object == object && t.Relation == "org" {
				if a.check(subject, RelationAdmin, t.User, depth+1) {
					return true
				}
			}
		}
	case relation == RelationAdmin && objType == "credential_ref":
		// object admin = direct ∪ owner team admin
		for _, t := range a.tuples {
			if t.Object == object && t.Relation == "owner_team" {
				if a.check(subject, RelationAdmin, t.User, depth+1) {
					return true
				}
			}
		}
	case relation == RelationReader && objType == "credential_ref":
		// reader = direct ∪ admin. NOTE: user implies nothing — deliberately
		// absent here (use-without-read, §2.5).
		return a.check(subject, RelationAdmin, object, depth+1)
	case relation == RelationAdmin && objType == "view":
		// view admin = direct ∪ owner-team admin (mirror credential_ref).
		for _, t := range a.tuples {
			if t.Object == object && t.Relation == "owner_team" {
				if a.check(subject, RelationAdmin, t.User, depth+1) {
					return true
				}
			}
		}
	case (relation == RelationReader || relation == RelationRunner) && objType == "view":
		// reader/runner = direct ∪ admin (§2.5 View-scoped execution, ADR-0028).
		// There is NO org/team-admin bypass: admin here is the VIEW's admin
		// (direct or owner-team), reached via the case above — org admin does
		// not imply runner on a View it does not own.
		return a.check(subject, RelationAdmin, object, depth+1)
	}
	return false
}
