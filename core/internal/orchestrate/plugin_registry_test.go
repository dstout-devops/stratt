package orchestrate

import (
	"fmt"
	"sync"
	"testing"
)

// TestPluginRegistry_Exclusivity proves the §2.4 exclusive-name guarantee: a name claimed
// twice, or colliding with an in-tree Actuator/Action, is rejected — the incumbent wins,
// never a last-writer/connection-order tiebreak.
func TestPluginRegistry_Exclusivity(t *testing.T) {
	inTreeActuator := map[string]bool{"opentofu": true}
	inTreeAction := map[string]bool{"builtin/x": true}
	r := NewPluginRegistry(
		func(n string) bool { return inTreeActuator[n] },
		func(n string) bool { return inTreeAction[n] },
	)

	if err := r.RegisterActuator("helm", PluginActuator{}); err != nil {
		t.Fatalf("first register must succeed: %v", err)
	}
	if err := r.RegisterActuator("helm", PluginActuator{}); err == nil {
		t.Fatal("a second plugin claiming the same actuator name must be rejected (§2.4)")
	}
	if err := r.RegisterActuator("opentofu", PluginActuator{}); err == nil {
		t.Fatal("an actuator colliding with an in-tree Actuator must be rejected (§2.4)")
	}
	if err := r.RegisterAction("builtin/x", PluginAction{}); err == nil {
		t.Fatal("an action colliding with an in-tree Action must be rejected (§2.4)")
	}
	if err := r.RegisterAction("aws/create-vm", PluginAction{DryRunnable: true}); err != nil {
		t.Fatalf("a free action name must register: %v", err)
	}
	// Deregister frees the name for re-registration (the runtime disable→enable path).
	r.DeregisterActuator("helm")
	if err := r.RegisterActuator("helm", PluginActuator{DryRunnable: true}); err != nil {
		t.Fatalf("a deregistered name must be re-registrable: %v", err)
	}
	if p, ok := r.Actuator("helm"); !ok || !p.DryRunnable {
		t.Fatal("re-registered actuator must be readable with its new value")
	}
}

// TestPluginRegistry_Race is the load-bearing gate (ADR-0103 D1): concurrent runtime
// Register/Deregister (the reconcile loop) racing the worker's Actuator/Action reads must
// be data-race-clean. Run with `go test -race`. Before PluginRegistry these were raw maps
// shared with the worker under no lock.
func TestPluginRegistry_Race(t *testing.T) {
	r := NewPluginRegistry(nil, nil)
	const workers = 8
	const iters = 500
	var wg sync.WaitGroup

	// Writers: churn actuators + actions on and off, as the reconcile loop would.
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				name := fmt.Sprintf("act-%d", (i%3)+w*3)
				_ = r.RegisterActuator(name, PluginActuator{DryRunnable: i%2 == 0})
				action := fmt.Sprintf("c/%d", (i%3)+w*3)
				_ = r.RegisterAction(action, PluginAction{})
				if i%2 == 0 {
					r.DeregisterActuator(name)
					r.DeregisterAction(action)
				}
			}
		}(w)
	}
	// Readers: the worker's routing lookups.
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				_, _ = r.Actuator(fmt.Sprintf("act-%d", (i%3)+w*3))
				_, _ = r.Action(fmt.Sprintf("c/%d", (i%3)+w*3))
			}
		}(w)
	}
	wg.Wait()
}
