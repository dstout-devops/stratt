package types

import "testing"

func TestInScope(t *testing.T) {
	cases := []struct {
		name   string
		envs   []string
		active string
		want   bool
	}{
		{"unscoped daemon, untagged decl", nil, "", true},
		{"unscoped daemon sees tagged decl", []string{"prod"}, "", true},
		{"untagged decl is in every environment", nil, "dev", true},
		{"empty (not nil) tag is all environments", []string{}, "dev", true},
		{"matching single env", []string{"dev"}, "dev", true},
		{"non-matching env is out of scope", []string{"prod"}, "dev", false},
		{"member of a multi-env set", []string{"dev", "staging"}, "staging", true},
		{"non-member of a multi-env set", []string{"dev", "staging"}, "prod", false},
	}
	for _, c := range cases {
		if got := InScope(c.envs, c.active); got != c.want {
			t.Errorf("%s: InScope(%v, %q) = %v, want %v", c.name, c.envs, c.active, got, c.want)
		}
	}
}

// ScopedEnvironments must reflect the declaration's field verbatim (membership,
// never precedence).
func TestScopedEnvironments(t *testing.T) {
	if got := (Assignment{Environments: []string{"dev"}}).ScopedEnvironments(); len(got) != 1 || got[0] != "dev" {
		t.Errorf("Assignment.ScopedEnvironments = %v", got)
	}
	if got := (Trigger{Environments: []string{"prod"}}).ScopedEnvironments(); len(got) != 1 || got[0] != "prod" {
		t.Errorf("Trigger.ScopedEnvironments = %v", got)
	}
	if got := (Baseline{}).ScopedEnvironments(); got != nil {
		t.Errorf("Baseline.ScopedEnvironments (untagged) = %v, want nil", got)
	}
	// Interface satisfaction (compile-time): all three are EnvScoped.
	var _ EnvScoped = Assignment{}
	var _ EnvScoped = Trigger{}
	var _ EnvScoped = Baseline{}
}
