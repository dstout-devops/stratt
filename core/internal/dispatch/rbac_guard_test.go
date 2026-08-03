package dispatch

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// THE VERBS THE CHART GRANTS MUST COVER THE VERBS THIS PACKAGE CALLS.
//
// This guard exists because its absence cost a defect that survived from ADR-0026 until ADR-0157's
// live proof went looking for it: the dispatcher Role granted `create` and `get` on batch/jobs, and
// DeleteRunJobs needs `list` (it selects a Run's Jobs by the stratt.dev/run-id label) and `delete`.
// So every cancellation stamped the Run `canceled` while its pod kept converging a real machine.
//
// NOTHING COULD HAVE CAUGHT IT IN UNIT FORM. TestDeleteRunJobs passes against the fake clientset,
// which grants everything and has no RBAC at all — it proves the label selector is right, which was
// never the problem. `helm lint` renders the Role and cannot know what the Go calls. The failure
// lived exactly in the gap between the two, which is where this test now sits.
//
// It reads the CHART TEMPLATE rather than a rendered manifest on purpose: the rendered output
// depends on values files, and the rule being guarded is unconditional. Skips if the chart is not in
// the tree (a trimmed checkout), because a guard that fails for being unable to find its subject
// teaches people to ignore it.
func TestChartGrantsTheJobVerbsThisPackageCalls(t *testing.T) {
	const chart = "../../../deploy/charts/stratt/templates/serviceaccount.yaml"
	raw, err := os.ReadFile(chart)
	if err != nil {
		t.Skipf("chart not in this checkout (%v)", err)
	}
	text := string(raw)

	// The batch/jobs rule, located by its resource rather than by line number so the guard
	// survives the file being reordered.
	idx := strings.Index(text, `resources: ["jobs"]`)
	if idx < 0 {
		t.Fatal("no batch/jobs rule found in the dispatcher Role — this guard has lost its subject, " +
			"which is exactly the state in which it would silently stop protecting anything")
	}
	verbLine := regexp.MustCompile(`verbs:\s*\[([^\]]*)\]`).FindStringSubmatch(text[idx:])
	if verbLine == nil {
		t.Fatal("the batch/jobs rule grants no verbs list this guard can read")
	}
	granted := map[string]bool{}
	for _, v := range strings.Split(verbLine[1], ",") {
		granted[strings.Trim(strings.TrimSpace(v), `"`)] = true
	}

	// Each verb is paired with the call that needs it, so a failure says what breaks rather than
	// only what is missing.
	for verb, why := range map[string]string{
		"create": "createJob spawns the execution pod",
		"get":    "the dispatcher polls the Job to completion",
		"list":   "DeleteRunJobs SELECTS a Run's Jobs by the stratt.dev/run-id label before deleting them — without this, cancellation cannot even find the pod it is supposed to reap",
		"delete": "DeleteRunJobs removes them — without this, a cancelled Run keeps converging real machines while its row says `canceled`",
	} {
		if !granted[verb] {
			t.Errorf("the dispatcher Role does not grant %q on batch/jobs, and %s", verb, why)
		}
	}
}
