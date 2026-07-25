package awxfacade

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// These cover resolveLaunchParams — what a /api/v2 launch actually sends to a Run once the
// Workflow it names may declare a launch interface (ADR-0118 D2, ADR-0025 follow-up).
//
// The branch matters because an imported AWX survey now lands as Workflow.inputs and its
// answers bind into the Step through {{.launch.x}}. Before this, the façade merged extra_vars
// straight onto params.extraVars and never looked at the Workflow — which, against an imported
// Workflow, would have handed ansible the literal string "{{.launch.replicas}}" for every
// input the caller happened not to supply, for it to interpret as Jinja.
//
// identityOf is a stub: the real one reads a concrete *graph.Store, and a Postgres-gated test
// is a skipped test in `task ci`.

func surveyedWorkflow() types.Workflow {
	return types.Workflow{
		Name: "awx/deploy-web",
		Inputs: json.RawMessage(`{
			"type": "object",
			"additionalProperties": false,
			"properties": {
				"app_version": {"type": "string", "default": "1.0"},
				"replicas": {"type": "integer", "default": 3}
			},
			"required": ["app_version"]
		}`),
	}
}

func surveyedStep() types.Step {
	return types.Step{
		Name:     "run",
		Actuator: "ansible",
		Params: map[string]any{
			"scm": map[string]any{"repo": "https://example.invalid/r.git", "playbook": "site.yml"},
			"extraVars": map[string]any{
				"app_version": "{{.launch.app_version}}",
				"replicas":    "{{.launch.replicas}}",
			},
		},
	}
}

func stubIdentity(string) string { return "ansible" }

// The motivating case: a caller supplies one answer, the survey's default supplies the other,
// and BOTH arrive as values — not as unresolved tokens.
func TestFacadeLaunchResolvesSurveyAnswersIntoTheStep(t *testing.T) {
	raw, err := resolveLaunchParams(surveyedWorkflow(), surveyedStep(),
		cloneParams(surveyedStep().Params), map[string]any{"app_version": "2.4"}, stubIdentity)
	if err != nil {
		t.Fatalf("a valid launch must resolve: %v", err)
	}
	if strings.Contains(string(raw), "{{.launch") {
		t.Fatalf("an unresolved launch token reached the Run — ansible would read it as Jinja: %s", raw)
	}

	var got struct {
		ExtraVars map[string]any `json:"extraVars"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.ExtraVars["app_version"] != "2.4" {
		t.Errorf("the caller's answer must win: %v", got.ExtraVars["app_version"])
	}
	// The declared default fills the answer the caller omitted, and it must arrive as a
	// NUMBER: template.Substitute preserves the native type of an exact single-token
	// binding, so an integer survey question does not reach the play as the string "3".
	if n, ok := got.ExtraVars["replicas"].(float64); !ok || n != 3 {
		t.Errorf("an omitted input must take its declared default, with its declared type; got %#v", got.ExtraVars["replicas"])
	}
}

// An answer to a question the survey does not ask must be REFUSED, not passed to the play.
// This is the whole point of enforcing an imported survey: AWX would have accepted it.
func TestFacadeLaunchRefusesAnUndeclaredAnswer(t *testing.T) {
	_, err := resolveLaunchParams(surveyedWorkflow(), surveyedStep(),
		cloneParams(surveyedStep().Params),
		map[string]any{"app_version": "2.4", "relicas": 5}, stubIdentity)
	if err == nil {
		t.Fatal("a typo'd answer must be rejected — the survey's schema is closed")
	}
	if !strings.Contains(err.Error(), "relicas") {
		t.Errorf("the refusal must name the offending key (§1.8): %v", err)
	}
}

// A required question with no answer and no default fails at the door rather than reaching a
// play that would fail confusingly on an undefined variable.
func TestFacadeLaunchRefusesAMissingRequiredAnswer(t *testing.T) {
	wf := surveyedWorkflow()
	wf.Inputs = json.RawMessage(`{
		"type": "object",
		"additionalProperties": false,
		"properties": {"app_version": {"type": "string"}},
		"required": ["app_version"]
	}`)
	step := types.Step{Name: "run", Actuator: "ansible", Params: map[string]any{
		"scm":       map[string]any{"repo": "https://example.invalid/r.git", "playbook": "site.yml"},
		"extraVars": map[string]any{"app_version": "{{.launch.app_version}}"},
	}}
	if _, err := resolveLaunchParams(wf, step, cloneParams(step.Params), nil, stubIdentity); err == nil {
		t.Fatal("a required survey question with no answer must be refused")
	}
}

// A Workflow that declares NO inputs keeps the old untyped behaviour exactly: extra_vars
// merge onto the Step's params, launch-time values winning. That is AWX's own
// ask_variables_on_launch, which this compat surface exists to emulate — nothing typed the
// seam, so nothing here may pretend it did.
func TestFacadeLaunchStillMergesExtraVarsWhenNothingIsDeclared(t *testing.T) {
	step := types.Step{Name: "run", Actuator: "ansible", Params: map[string]any{
		"extraVars": map[string]any{"kept": "yes", "overridden": "no"},
	}}
	raw, err := resolveLaunchParams(types.Workflow{Name: "awx/legacy"}, step,
		cloneParams(step.Params), map[string]any{"overridden": "yes", "added": 1}, stubIdentity)
	if err != nil {
		t.Fatalf("an undeclared Workflow must still launch: %v", err)
	}
	var got struct {
		ExtraVars map[string]any `json:"extraVars"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.ExtraVars["kept"] != "yes" || got.ExtraVars["overridden"] != "yes" || got.ExtraVars["added"] != float64(1) {
		t.Errorf("undeclared merge semantics changed: %#v", got.ExtraVars)
	}
}
