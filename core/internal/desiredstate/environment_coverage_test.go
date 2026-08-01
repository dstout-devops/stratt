package desiredstate

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// TestEveryEnvironmentScopedKindIsRefChecked derives, by reflection over Declarations, every
// declaration kind that carries an `Environments []string` filter, and asserts validateEnvironmentRefs
// actually checks each one.
//
// ADR-0142 D2 built that check over a hand-written list, with a comment arguing the list should stay
// explicit "so that adding a kind with `environments` and forgetting it here is a visible omission in
// review". The reasoning is sound and the outcome was not: `Actuator` was already missing when that
// sentence was written, and stayed missing. An Actuator scoped to a typo'd environment is filtered out
// of provisioning-provider assembly in EVERY environment (assembleProvisioningProviders → InScope),
// so the Intent it should build for resolves to no provider at all — reported as "no verified
// provider", which points the reader at the plugin registry instead of at the typo one file away.
//
// So the list stays explicit — a reader still sees which kinds are covered — and this test supplies
// the completeness the comment assumed a reviewer would. It is derived rather than enumerated on
// purpose: a test carrying its own second list would fail in exactly the same way as the first.
func TestEveryEnvironmentScopedKindIsRefChecked(t *testing.T) {
	scoped := environmentScopedKinds(t)
	if len(scoped) == 0 {
		t.Fatal("reflection found no environment-scoped declaration kinds — the Declarations shape " +
			"changed and this guard is checking nothing")
	}
	t.Logf("environment-scoped declaration kinds: %v", scoped)

	// Build a Declarations carrying ONE declaration of every scoped kind, each referencing an
	// environment nothing declares, alongside a declared environment (the check is opt-in on an
	// estate declaring at least one). Every kind must be reported.
	for _, kind := range scoped {
		t.Run(kind, func(t *testing.T) {
			d := Declarations{Environments: []types.Environment{{Name: "dev"}}}
			setEnvironmentsOn(t, &d, kind, []string{"typo-env"})
			err := validateEnvironmentRefs(d)
			if err == nil {
				t.Fatalf("a %s scoped to an undeclared environment was ACCEPTED. It is not a no-op: "+
					"it filters that declaration out of every environment, silently and permanently "+
					"(§1.8, ADR-0142 D2). Add it to the refs list in validateEnvironmentRefs", kind)
			}
			if !strings.Contains(err.Error(), "typo-env") {
				t.Errorf("the diagnostic must name the offending value; got %q", err)
			}
		})
	}
}

// environmentScopedKinds returns the Declarations field names whose element type has an
// `Environments []string` field.
func environmentScopedKinds(t *testing.T) []string {
	t.Helper()
	var out []string
	dt := reflect.TypeOf(Declarations{})
	for i := range dt.NumField() {
		f := dt.Field(i)
		if f.Type.Kind() != reflect.Slice {
			continue
		}
		elem := f.Type.Elem()
		if elem.Kind() != reflect.Struct {
			continue
		}
		if ef, ok := elem.FieldByName("Environments"); ok && ef.Type == reflect.TypeOf([]string{}) {
			out = append(out, f.Name)
		}
	}
	sort.Strings(out)
	return out
}

// setEnvironmentsOn appends one declaration of the named Declarations field, with a name and the
// given environments — reflectively, so a new scoped kind needs no edit here either.
func setEnvironmentsOn(t *testing.T, d *Declarations, field string, envs []string) {
	t.Helper()
	fv := reflect.ValueOf(d).Elem().FieldByName(field)
	if !fv.IsValid() {
		t.Fatalf("no Declarations field %q", field)
	}
	elem := reflect.New(fv.Type().Elem()).Elem()
	if nf := elem.FieldByName("Name"); nf.IsValid() && nf.Kind() == reflect.String {
		nf.SetString("probe-" + strings.ToLower(field))
	}
	ef := elem.FieldByName("Environments")
	if !ef.IsValid() {
		t.Fatalf("%s has no Environments field", field)
	}
	ef.Set(reflect.ValueOf(envs))
	fv.Set(reflect.Append(fv, elem))
}
