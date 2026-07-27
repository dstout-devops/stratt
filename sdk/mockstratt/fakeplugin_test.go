package mockstratt

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// A fake plugin, built the way the real EE-Job transport builds one: a ONE-SHOT
// binary that reads the sovereign ApplyRequest from its runner directory and
// writes proto-JSON frames on stdout.
//
// It is the TEST BINARY re-executing itself under an env var rather than a
// compiled fixture, so these tests need no toolchain at run time and genuinely
// exercise os/exec, the pipe, the scanner and the frame/diagnostic split. A fake
// that ran in-process would test the governor twice and the transport never.

const fakeEnv = "MOCKSTRATT_FAKE_PLUGIN"

func TestMain(m *testing.M) {
	if scenario := os.Getenv(fakeEnv); scenario != "" {
		os.Exit(fakePlugin(scenario))
	}
	os.Exit(m.Run())
}

// fake builds a Subprocess that re-execs this test binary in the named scenario.
func fake(t *testing.T, scenario string) *Subprocess {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &Subprocess{Binary: self, Env: []string{fakeEnv + "=" + scenario}}
}

func emit(resp *pluginv1.ApplyResponse) {
	b, err := protojson.Marshal(resp)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(b))
}

func event(level pluginv1.TaskEvent_Level, msg string) *pluginv1.TaskEvent {
	return &pluginv1.TaskEvent{Level: level, Message: msg, At: timestamppb.Now()}
}

func terminal(ok bool, msg string) *pluginv1.ApplyResponse {
	ev := event(pluginv1.TaskEvent_LEVEL_INFO, msg)
	ev.Terminal, ev.Ok = true, ok
	return &pluginv1.ApplyResponse{Event: ev}
}

func result(name string, st pluginv1.ItemResult_Status) *pluginv1.ApplyResponse {
	return &pluginv1.ApplyResponse{Result: &pluginv1.ItemResult{ItemKey: name, Status: st}}
}

// fakePlugin is the child process. Each scenario is a shape a real plugin has
// taken, several of them defects this repo has actually shipped.
func fakePlugin(scenario string) int {
	req := readRequest()

	switch scenario {
	case "ok":
		for _, t := range req.GetTargets() {
			emit(result(t.GetName(), pluginv1.ItemResult_STATUS_CHANGED))
		}
		emit(terminal(true, "converged"))

	case "vacuous":
		// Actuated nothing, declared success. The most expensive failure the
		// platform can produce, because it is indistinguishable from convergence.
		emit(terminal(true, "converged"))

	case "torn":
		// Emitted work, then died before terminating. The core folds this to
		// FAILED; a plugin author relying on "no news is good news" does not.
		emit(result(req.GetTargets()[0].GetName(), pluginv1.ItemResult_STATUS_OK))

	case "silent-failure":
		// A red terminal with nothing to say — ADR-0117 D5c's dead end from the
		// plugin's side.
		emit(terminal(false, ""))

	case "loud-failure":
		emit(result(req.GetTargets()[0].GetName(), pluginv1.ItemResult_STATUS_UNREACHABLE))
		emit(terminal(false, "ssh: connect to host web-1 port 22: no route to host"))

	case "green-then-crash":
		// Said it worked, then died. Only the exit-status half of the fold catches
		// this, which is why executeJobPlugin ANDs both signals.
		for _, t := range req.GetTargets() {
			emit(result(t.GetName(), pluginv1.ItemResult_STATUS_OK))
		}
		emit(terminal(true, "converged"))
		return 137 // OOMKilled

	case "confused-deputy":
		emit(result("a-host-nobody-asked-for", pluginv1.ItemResult_STATUS_OK))
		emit(terminal(true, "converged"))

	case "noisy":
		// The stdout a real runner produces around its frames.
		fmt.Println("PLAY [all] *********************************************************")
		emit(result(req.GetTargets()[0].GetName(), pluginv1.ItemResult_STATUS_OK))
		fmt.Println("{ this is not valid json")
		fmt.Fprintln(os.Stderr, "warning: something on stderr")
		emit(terminal(true, "converged"))

	case "overreach":
		// Emits beyond its grant on every axis. In production all of it is dropped
		// silently and the plugin looks healthy.
		emit(&pluginv1.ApplyResponse{
			WriteBack: []*pluginv1.ObservedEntity{{
				Kind:         "host",
				IdentityKeys: map[string]string{"host.name": "web-1", "dns.fqdn": "web-1.example.com"},
				Labels:       map[string]string{"env": "prod", "secret": "no"},
				Facets: map[string][]byte{
					"os.kernel":  []byte(`{"version":"6.1"}`),
					"app.config": []byte(`{"port":"8080"}`),
					"billing":    []byte(`{"cost":9000}`),
				},
			}},
		})
		emit(terminal(true, "converged"))

	case "writes-into-project":
		// The ADR-0134 collision: laying a file over the mounted content root.
		// Passes on a writable temp dir, EACCES against a real ConfigMap mount.
		if err := os.WriteFile(filepath.Join(os.Getenv("STRATT_RUNNER_DIR"), "project", "play.yml"), []byte("- hosts: all\n"), 0o644); err != nil {
			emit(terminal(false, "write into project/: "+err.Error()))
			return 1
		}
		emit(terminal(true, "wrote into project/"))

	case "echo":
		// Reports what it was handed, so the staging half can be asserted.
		content := map[string]string{}
		root := filepath.Join(os.Getenv("STRATT_RUNNER_DIR"), "project")
		_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil //nolint:nilerr // a missing project/ is legal: no contentDir was declared
			}
			b, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(root, p)
			content[filepath.ToSlash(rel)] = string(b)
			return nil
		})
		fields := map[string]string{
			"params":  string(req.GetDesired().GetBytes()),
			"dry_run": fmt.Sprint(req.GetDryRun()),
			"creds":   fmt.Sprint(len(req.GetEnvelope().GetCreds())),
		}
		for k, v := range content {
			fields["project/"+k] = v
		}
		for _, t := range req.GetTargets() {
			fields["target/"+t.GetName()] = t.GetAddress()
		}
		ev := event(pluginv1.TaskEvent_LEVEL_INFO, "echo")
		ev.Fields, ev.Terminal, ev.Ok = fields, true, true
		emit(&pluginv1.ApplyResponse{Event: ev})

	default:
		fmt.Fprintln(os.Stderr, "unknown scenario:", scenario)
		return 2
	}
	return 0
}

func readRequest() *pluginv1.ApplyRequest {
	raw, err := os.ReadFile(os.Getenv("STRATT_REQUEST"))
	if err != nil {
		panic(err)
	}
	req := &pluginv1.ApplyRequest{}
	if err := protojson.Unmarshal(raw, req); err != nil {
		panic(err)
	}
	return req
}

// chanStream feeds hand-written frames to the governor, for the rules that are
// awkward to provoke through a real process.
type chanStream struct {
	frames []*pluginv1.ApplyResponse
	i      int
}

func (s *chanStream) Recv() (*pluginv1.ApplyResponse, error) {
	if s.i >= len(s.frames) {
		return nil, io.EOF
	}
	s.i++
	return s.frames[s.i-1], nil
}
