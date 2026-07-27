package pluginserve

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// capture is a ServerStreamingServer that records what a plugin sent.
type capture[T any] struct {
	grpc.ServerStream
	sent []*T
}

func (c *capture[T]) Send(m *T) error              { c.sent = append(c.sent, m); return nil }
func (c *capture[T]) Context() context.Context     { return context.Background() }
func (c *capture[T]) SetHeader(metadata.MD) error  { return nil }
func (c *capture[T]) SendHeader(metadata.MD) error { return nil }
func (c *capture[T]) SetTrailer(metadata.MD)       {}

func TestEnvFallsBackToTheDefault(t *testing.T) {
	if got := Env("STRATT_PLUGINSERVE_UNSET_XYZ", "fallback"); got != "fallback" {
		t.Fatalf("an unset variable must take the default, got %q", got)
	}
	t.Setenv("STRATT_PLUGINSERVE_SET_XYZ", "live")
	if got := Env("STRATT_PLUGINSERVE_SET_XYZ", "fallback"); got != "live" {
		t.Fatalf("a set variable must win, got %q", got)
	}
}

// The terminal frame is what the core reads to decide an item's outcome. A stream that ends
// without one leaves a Run waiting on a verdict that never arrives, so the shape is pinned here
// rather than in three plugins that each had their own copy of it.
func TestApplyTerminalShape(t *testing.T) {
	c := &capture[pluginv1.ApplyResponse]{}
	if err := ApplyTerminal(c, false, pluginv1.ItemResult_STATUS_FAILED, "boom"); err != nil {
		t.Fatal(err)
	}
	if len(c.sent) != 1 {
		t.Fatalf("exactly one terminal frame, got %d", len(c.sent))
	}
	ev := c.sent[0].GetEvent()
	if !ev.GetTerminal() {
		t.Error("the frame must be marked terminal — the core keys the item's outcome on it")
	}
	if ev.GetOk() {
		t.Error("ok must reflect the outcome passed in")
	}
	if ev.GetMessage() != "boom" {
		t.Errorf("message: %q", ev.GetMessage())
	}
	if ev.GetFields()["kind"] != "finished" {
		t.Errorf("the finished marker must survive: %v", ev.GetFields())
	}
	if c.sent[0].GetResult().GetStatus() != pluginv1.ItemResult_STATUS_FAILED {
		t.Errorf("status: %v", c.sent[0].GetResult().GetStatus())
	}
}

func TestInvokeTerminalSuccessCarriesOutputs(t *testing.T) {
	c := &capture[pluginv1.InvokeResponse]{}
	req := &pluginv1.InvokeRequest{Envelope: &pluginv1.Envelope{CorrelationId: "corr-1"}}
	err := InvokeTerminal(c, req, true, Terminal{
		OutputContract: "actions/x/y.output", OKMessage: "done", Outputs: []byte(`{"a":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := c.sent[0]
	if !got.GetEvent().GetOk() || got.GetEvent().GetLevel() != pluginv1.TaskEvent_LEVEL_INFO {
		t.Error("a success frame is INFO and ok")
	}
	// The correlation id joins the event to its Run. Dropping it is a §1.8 descent break that
	// surfaces as an orphaned event rather than an error, which is why it is asserted.
	if got.GetEvent().GetCorrelationId() != "corr-1" {
		t.Errorf("correlation id must be echoed from the envelope, got %q", got.GetEvent().GetCorrelationId())
	}
	if string(got.GetResult().GetOutputs().GetBytes()) != `{"a":1}` {
		t.Errorf("outputs: %s", got.GetResult().GetOutputs().GetBytes())
	}
	if got.GetResult().GetOutputContract().GetSchemaId() != "actions/x/y.output" {
		t.Errorf("output contract: %q", got.GetResult().GetOutputContract().GetSchemaId())
	}
}

func TestInvokeTerminalFailureCarriesNoOutputs(t *testing.T) {
	c := &capture[pluginv1.InvokeResponse]{}
	req := &pluginv1.InvokeRequest{Envelope: &pluginv1.Envelope{CorrelationId: "corr-2"}}
	err := InvokeTerminal(c, req, false, Terminal{
		OutputContract: "actions/x/y.output", OKMessage: "done", FailMessage: "failed",
		Detail: "timeout", Outputs: []byte(`{"a":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := c.sent[0]
	if got.GetEvent().GetLevel() != pluginv1.TaskEvent_LEVEL_ERROR || got.GetEvent().GetMessage() != "failed" {
		t.Errorf("a failure frame is ERROR with the failure message: %v %q",
			got.GetEvent().GetLevel(), got.GetEvent().GetMessage())
	}
	if got.GetEvent().GetFields()["detail"] != "timeout" {
		t.Errorf("the failure CLASS rides `detail`: %v", got.GetEvent().GetFields())
	}
	// Outputs were supplied and must still be withheld: a failed Action has not satisfied its
	// output Contract, so shipping a payload anyway would let a consumer bind values from a Run
	// that did not succeed.
	if got.GetResult().GetOutputs() != nil {
		t.Error("a failure frame must carry NO outputs even when the caller passed some")
	}
	// The contract id is still named — the frame says which contract WOULD have been satisfied.
	if got.GetResult().GetOutputContract().GetSchemaId() != "actions/x/y.output" {
		t.Error("the output contract must still be named on failure")
	}
}
