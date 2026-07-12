// Package rules compiles and evaluates Trigger CEL expressions (charter
// Phase 2: "Emitter event × CEL rule"; ADR-0018). CEL is hermetic by design
// (no I/O, no side effects, non-Turing-complete); this wrapper adds the
// cost bounds the dependency scout mandated: expressions are rejected at
// declaration parse when their static worst-case cost is absurd, and every
// evaluation runs under a hard runtime cost limit.
package rules

import (
	"fmt"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker"
)

const (
	// maxStaticCost rejects pathological expressions at declaration time.
	maxStaticCost = 1_000_000
	// evalCostLimit aborts a runaway evaluation (defense-in-depth against
	// pathological payloads).
	evalCostLimit = 1_000_000
)

// Program is a compiled, cost-bounded Trigger rule.
type Program struct {
	prg cel.Program
}

func environment() (*cel.Env, error) {
	return cel.NewEnv(
		cel.Variable("event", cel.DynType),
		cel.Variable("emitter", cel.StringType),
	)
}

// Compile checks and plans a `when` expression. The result type must be
// bool — a rule that cannot decide is a declaration error, caught at parse
// (§1.8: fail the file, never silently at event time).
func Compile(expr string) (*Program, error) {
	env, err := environment()
	if err != nil {
		return nil, err
	}
	ast, iss := env.Compile(expr)
	if iss.Err() != nil {
		return nil, fmt.Errorf("rules: %w", iss.Err())
	}
	if ast.OutputType() != cel.BoolType {
		return nil, fmt.Errorf("rules: expression must evaluate to bool, got %s", ast.OutputType())
	}
	cost, err := env.EstimateCost(ast, defaultEstimator{})
	if err != nil {
		// Fail closed: an unestimable expression does not get to skip the
		// static gate (the runtime CostLimit still applies regardless).
		return nil, fmt.Errorf("rules: cost estimation failed: %w", err)
	}
	if cost.Max > maxStaticCost {
		return nil, fmt.Errorf("rules: expression worst-case cost %d exceeds the limit %d", cost.Max, maxStaticCost)
	}
	prg, err := env.Program(ast, cel.CostLimit(evalCostLimit))
	if err != nil {
		return nil, fmt.Errorf("rules: plan: %w", err)
	}
	return &Program{prg: prg}, nil
}

// defaultEstimator uses the checker's built-in cost model (nil answers mean
// "use defaults" for both size and call cost).
type defaultEstimator struct{}

func (defaultEstimator) EstimateSize(checker.AstNode) *checker.SizeEstimate { return nil }
func (defaultEstimator) EstimateCallCost(string, string, *checker.AstNode, []checker.AstNode) *checker.CallEstimate {
	return nil
}

// Eval runs the rule against one event payload. Evaluation errors (type
// mismatches against this payload, missing keys) are returned — the engine
// logs them and does not launch; they are not silent falses.
func (p *Program) Eval(emitter string, payload map[string]any) (bool, error) {
	out, _, err := p.prg.Eval(map[string]any{
		"event":   payload,
		"emitter": emitter,
	})
	if err != nil {
		return false, fmt.Errorf("rules: eval: %w", err)
	}
	b, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("rules: expression produced %T, not bool", out.Value())
	}
	return b, nil
}
