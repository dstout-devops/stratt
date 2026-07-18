package policy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/dstout-devops/stratt/types"
)

// Exec is an external-engine Decider over the SUBPROCESS transport (§1.5,
// ADR-0074): it runs an external policy tool (OPA, Kyverno-JSON, …) that speaks
// the Decision contract over stdin/stdout — the request marshalled to JSON on
// stdin, a types.Decision JSON on stdout. Engine-agnostic; the command is
// configuration. Fail-closed: any transport error, unparseable output, or
// unrecognised outcome DENIES (§1.8), never a silent allow. The core call site
// is unchanged and content-blind — this is just another provider behind the port.
type Exec struct {
	// Run executes the external tool for op ("decide"|"admit"), writing request
	// to the tool's stdin and returning its stdout. Injected so the transport
	// (and tests) are decoupled from the decision logic.
	Run func(ctx context.Context, op string, request []byte) ([]byte, error)
	// Name identifies the engine in provenance (e.g. "opa", "kyverno").
	Name string
}

// Decide runs the external engine for the gate PEP.
func (e Exec) Decide(ctx context.Context, req Request) types.Decision {
	in, err := json.Marshal(req)
	if err != nil {
		return e.failClosed("marshal_error", err.Error())
	}
	return e.eval(ctx, "decide", in)
}

// Admit runs the external engine for the admission PEP.
func (e Exec) Admit(ctx context.Context, req AdmissionRequest) types.Decision {
	in, err := json.Marshal(req)
	if err != nil {
		return e.failClosed("marshal_error", err.Error())
	}
	return e.eval(ctx, "admit", in)
}

func (e Exec) eval(ctx context.Context, op string, input []byte) types.Decision {
	out, err := e.Run(ctx, op, input)
	if err != nil {
		return e.failClosed("exec_error", err.Error())
	}
	var dec types.Decision
	if err := json.Unmarshal(out, &dec); err != nil {
		return e.failClosed("parse_error", err.Error())
	}
	switch dec.Outcome {
	case types.OutcomeAllow, types.OutcomeDeny, types.OutcomeRequireApproval, types.OutcomeEscalate:
	default:
		return e.failClosed("invalid_outcome", fmt.Sprintf("engine returned unrecognised outcome %q", dec.Outcome))
	}
	dec.Provenance.Engine = "exec:" + e.Name
	if dec.Provenance.EvaluatedAt.IsZero() {
		dec.Provenance.EvaluatedAt = time.Now().UTC()
	}
	return dec
}

// Validate delegates to the external engine, which validates its own policy
// dialect at deploy time; the Exec provider does not re-validate the built-in
// CEL provider's inline-Control dialect. v1 accepts.
func (Exec) Validate([]types.Control) error { return nil }

func (e Exec) failClosed(code, msg string) types.Decision {
	return types.Decision{
		Outcome:    types.OutcomeDeny,
		Reasons:    []types.Reason{{Code: code, Message: msg}},
		Provenance: types.DecisionProvenance{Engine: "exec:" + e.Name, EvaluatedAt: time.Now().UTC()},
	}
}

// NewExecCommand builds an Exec provider that runs `bin` with the op ("decide"/
// "admit") as the first argument (so one wrapper routes both PEPs), piping the
// request to stdin and reading a Decision from stdout (ADR-0074). Used by strattd
// when STRATT_POLICY_EXEC_CMD is set.
func NewExecCommand(name, bin string, args ...string) Exec {
	return Exec{Name: name, Run: func(ctx context.Context, op string, request []byte) ([]byte, error) {
		cmd := exec.CommandContext(ctx, bin, append([]string{op}, args...)...)
		cmd.Stdin = bytes.NewReader(request)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("policy engine %q %s: %w: %s", name, op, err, stderr.String())
		}
		return stdout.Bytes(), nil
	}}
}
