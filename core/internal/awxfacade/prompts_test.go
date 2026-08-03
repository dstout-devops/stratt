package awxfacade

import (
	"encoding/json"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func wfWith(inputs string, params map[string]any) (types.Workflow, types.Step) {
	st := types.Step{Name: "converge", ViewName: "prod", Actuator: "ansible", Params: params}
	wf := types.Workflow{Name: "patch", Steps: []types.Step{st}}
	if inputs != "" {
		wf.Inputs = json.RawMessage(inputs)
	}
	return wf, st
}

const oneInput = `{"type":"object","additionalProperties":false,"properties":{"hostLimit":{"type":"string"}}}`

// THE POINT OF ADR-0160 D2: the mechanism already worked and the façade under-reported it. AWX
// tooling READS these booleans to decide what to prompt for, so a migrated template silently lost
// prompts the engine would have honoured.
//
// FALSIFICATION: hardcode any of these and the matching case fails.
func TestAskFieldsAreDerivedFromTheBinding(t *testing.T) {
	// Declared AND bound → the prompt is real.
	wf, st := wfWith(oneInput, map[string]any{"limit": "{{.launch.hostLimit}}"})
	if askFields(wf, st, true)["ask_limit_on_launch"] != true {
		t.Error("a Workflow that declares an input AND binds it into `limit` HAS a promptable limit")
	}

	// Bound but NOT declared → a token nothing can fill. The launch resolver rejects an answer to a
	// question the Workflow does not ask (ADR-0118 D2), so advertising it invites a 400.
	wf, st = wfWith("", map[string]any{"limit": "{{.launch.hostLimit}}"})
	if askFields(wf, st, true)["ask_limit_on_launch"] != false {
		t.Error("a binding with no declared input is a prompt that cannot be answered")
	}

	// Declared but NOT bound → the answer would reach nothing. Advertising it invites a caller to
	// supply a value that changes no behaviour, which is worse than saying no.
	wf, st = wfWith(oneInput, map[string]any{"limit": "web*"})
	if askFields(wf, st, true)["ask_limit_on_launch"] != false {
		t.Error("a declared input nothing binds changes no behaviour")
	}
}

// Nested params count: a caller prompting for a branch is prompting for scm_branch however deep the
// token sits.
func TestNestedBindingsCountForTheirTopLevelParam(t *testing.T) {
	wf, st := wfWith(`{"properties":{"branch":{"type":"string"}}}`,
		map[string]any{"scm": map[string]any{"repo": "https://git/x", "ref": "{{.launch.branch}}"}})
	if askFields(wf, st, true)["ask_scm_branch_on_launch"] != true {
		t.Error("params.scm.ref bound from a declared input IS a promptable scm_branch")
	}
}

// Only `.launch` is a prompt. `.event` and `.steps` bind from elsewhere and no caller can supply them.
func TestOnlyTheLaunchNamespaceIsAPrompt(t *testing.T) {
	for _, tok := range []string{"{{.event.limit}}", "{{.steps.gather.outputs.limit}}"} {
		wf, st := wfWith(`{"properties":{"limit":{"type":"string"}}}`, map[string]any{"limit": tok})
		if askFields(wf, st, true)["ask_limit_on_launch"] != false {
			t.Errorf("%s is not launch-supplied and must not advertise a prompt", tok)
		}
	}
}

// EVERY field AWX defines is emitted, including the false ones: a migrating client reads an absent
// key as "this server is old" rather than "this prompt is off", and the two must be distinguishable.
func TestEveryAskFieldIsPublishedEvenWhenFalse(t *testing.T) {
	wf, st := wfWith("", nil)
	got := askFields(wf, st, false)
	for _, f := range []string{
		"ask_variables_on_launch", "ask_limit_on_launch", "ask_inventory_on_launch",
		"ask_credential_on_launch", "ask_scm_branch_on_launch", "ask_job_type_on_launch",
		"ask_tags_on_launch", "ask_skip_tags_on_launch", "ask_diff_mode_on_launch",
		"ask_verbosity_on_launch", "ask_forks_on_launch", "ask_timeout_on_launch",
		"ask_execution_environment_on_launch", "ask_instance_groups_on_launch",
		"ask_labels_on_launch", "ask_job_slice_count_on_launch",
	} {
		if _, ok := got[f]; !ok {
			t.Errorf("%s is not published — a client cannot tell 'off' from 'unsupported'", f)
		}
	}
}

// `credential` stays FALSE until ADR-0160 D4 ships the declared permitted set. A launch-supplied
// credentialRef today would bypass the §2.5 use-check, which is authorized against the Step's
// DECLARED refs — so advertising the prompt would invite exactly the escalation D4 exists to make
// safe. This test is the guard on that ordering.
func TestCredentialIsAdvertisedOnlyWhenTheEstateDeclaredAChoice(t *testing.T) {
	// One ref is a declaration, not a choice — there is nothing to select between.
	wf, st := wfWith("", nil)
	st.CredentialRefs = []string{"only-one"}
	if askFields(wf, st, true)["ask_credential_on_launch"] != false {
		t.Error("a single declared credentialRef offers no selection")
	}
	// Two or more IS a choice: the launch narrows, and the §2.5 use-check still runs per survivor.
	st.CredentialRefs = []string{"prod", "dev"}
	if askFields(wf, st, true)["ask_credential_on_launch"] != true {
		t.Error("a Step declaring several refs offers a launch-time selection (ADR-0160 D4)")
	}
	// A launch-BOUND credentialRef is still not a prompt: binding it would put the value outside the
	// declared set the §2.5 check is authorized against.
	wf2, st2 := wfWith(`{"properties":{"cred":{"type":"string"}}}`,
		map[string]any{"connection": map[string]any{"credentialRef": "{{.launch.cred}}"}})
	if askFields(wf2, st2, true)["ask_credential_on_launch"] != false {
		t.Error("a bound credentialRef is not the mechanism: D4 selects from a DECLARED set")
	}
}

// The execution environment is advertised exactly when the Actuator declares a permitted set.
func TestExecutionEnvironmentIsAdvertisedFromThePermittedSet(t *testing.T) {
	prev := actuatorImages
	t.Cleanup(func() { actuatorImages = prev })

	wf, st := wfWith("", nil)
	st.Actuator = "ansible-apache"

	actuatorImages = func(string) []string { return nil }
	if askFields(wf, st, true)["ask_execution_environment_on_launch"] != false {
		t.Error("an Actuator declaring no `images` offers no choice — ADR-0117 D3a's single image")
	}
	actuatorImages = func(string) []string { return []string{"stratt-ee-crypto:dev"} }
	if askFields(wf, st, true)["ask_execution_environment_on_launch"] != true {
		t.Error("a declared permitted set IS a launch-time choice (ADR-0160 D4)")
	}
}

// A DAG's prompts are the UNION across its Steps: an answer resolves into the Step that asked for it
// and is simply absent from the others, which is what the launch resolver already does.
func TestWorkflowPromptsAreTheUnionAcrossSteps(t *testing.T) {
	wf := types.Workflow{
		Name:   "two-step",
		Inputs: json.RawMessage(`{"properties":{"hostLimit":{"type":"string"},"v":{"type":"integer"}}}`),
		Steps: []types.Step{
			{Name: "gather", ViewName: "prod", Actuator: "a", Params: map[string]any{"limit": "{{.launch.hostLimit}}"}},
			{Name: "deliver", ViewName: "prod", Actuator: "a", Params: map[string]any{"verbosity": "{{.launch.v}}"}},
		},
	}
	got := askFieldsForWorkflow(wf)
	if got["ask_limit_on_launch"] != true || got["ask_verbosity_on_launch"] != true {
		t.Errorf("both Steps' prompts must survive the union: %v", got)
	}
	if got["ask_forks_on_launch"] != false {
		t.Errorf("a field no Step binds stays false: %v", got)
	}
}
