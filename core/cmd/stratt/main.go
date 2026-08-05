// Command stratt is the operator CLI. Phase-1 surface: plan/apply of the
// Git-declared desired state (charter §1.2) — through the platform API only,
// the same surface the UI, CI, and agents use (§1.6).
//
//	stratt plan  -d <declarations-dir> [-s http://localhost:8080]
//	stratt apply -d <declarations-dir> [-s http://localhost:8080]
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/dstout-devops/stratt/core/internal/desiredstate"
	"github.com/dstout-devops/stratt/core/internal/policy"
)

// verb is one accepted CLI command AND its banner line, in one place.
//
// ── ONE LIST, BECAUSE TWO LISTS DRIFTED ──────────────────────────────────────────────────────
//
// This was a dispatch chain plus a hand-written usage string, and they disagreed: the banner
// advertised `stratt import awx …` long after ADR-0086 D1 retired the verb and ADR-0089 D5 deleted
// import.go. An operator who ran what the banner told them to run got exit 2 — the abstraction
// hiding a failure rather than a mechanism (§1.8), in the one place a new user starts.
//
// The accepted set and the advertised set are now the same value. Adding a verb without advertising
// it, or advertising one that does not dispatch, is no longer possible to do by accident —
// verbs_test.go fails on either.
//
// `import` is NOT coming back. We never import: the projection is always-on (ADR-0025's own
// amendment), and `adopt` is the per-object strangler-fig path. A bounded `bulk-adopt` is booked in
// ADR-0089 if demand appears — never a silent full-estate one-shot.
type verb struct {
	name  string
	usage string
	run   func(args []string) error
}

func verbs() []verb {
	return []verb{
		{"plan", "stratt plan [-d declarations-dir] [-s server-url]", planApply("plan")},
		{"apply", "stratt apply [-d declarations-dir] [-s server-url]", planApply("apply")},
		// `adopt <kind> <identity>` (ADR-0086) materializes ONE already-observed object into a
		// reviewable Named-Kind bundle, from the live projection catalog + a targeted deep-read.
		{"adopt", "stratt adopt <kind> <identity> --endpoint <url> [--token <t>] [-s server] -o <out-dir>", runAdopt},
		// `bundle` packages Step content into a cosign-signable OCI Bundle for pull-mode Sites
		// (ADR-0032); it talks to a registry, not the platform API.
		{"bundle", "stratt bundle push <content-dir> <ref> --name N --version V --actuator A", runBundle},
		// `pack` lists/shows/installs in-tree content packs (ADR-0033); install materializes a pack
		// into the operator's desired-state Git (§1.2), never touching the platform API.
		{"pack", "stratt pack <list|show|install> [name] --view V -o <cac-dir>", runPack},
	}
}

// planApply builds the run func for the two verbs that share the desired-state flag set.
func planApply(cmd string) func([]string) error {
	return func(args []string) error {
		fs := flag.NewFlagSet(cmd, flag.ExitOnError)
		dir := fs.String("d", ".", "declarations directory (contains views/)")
		server := fs.String("s", envOr("STRATT_SERVER", "http://localhost:8080"), "control-plane base URL")
		_ = fs.Parse(args)
		return run(cmd, *dir, *server)
	}
}

func lookup(name string) (verb, bool) {
	for _, v := range verbs() {
		if v.name == name {
			return v, true
		}
	}
	return verb{}, false
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	v, ok := lookup(os.Args[1])
	if !ok {
		usage()
		os.Exit(2)
	}
	if err := v.run(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "stratt:", err)
		os.Exit(1)
	}
}

// usage renders FROM the table, so it cannot advertise a verb that does not dispatch.
func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	for _, v := range verbs() {
		fmt.Fprintln(os.Stderr, "  "+v.usage)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func run(cmd, dir, server string) error {
	decls, err := desiredstate.ParseDir(dir, policy.CEL{})
	if err != nil {
		return err
	}
	// Declarations' JSON shape is the wire DesiredState (views +
	// credentialRefs) — pointer metadata only, never material (§2.5).
	body, err := json.Marshal(decls)
	if err != nil {
		return err
	}
	resp, err := http.Post(server+"/api/v1/desired-state/"+cmd, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", resp.Status, payload)
	}
	var plan desiredstate.Plan
	if err := json.Unmarshal(payload, &plan); err != nil {
		return fmt.Errorf("decode plan: %w", err)
	}
	return render(cmd, plan)
}

func render(cmd string, plan desiredstate.Plan) error {
	symbols := map[desiredstate.Action]string{
		desiredstate.ActionCreate: "+",
		desiredstate.ActionUpdate: "~",
		desiredstate.ActionAdopt:  ">",
		desiredstate.ActionDelete: "-",
		desiredstate.ActionNoop:   "=",
	}
	failed := 0
	for _, e := range plan.Entries {
		if e.Action == desiredstate.ActionNoop {
			continue
		}
		kind := e.Kind
		if kind == "" {
			kind = desiredstate.KindView
		}
		if kind == desiredstate.KindView {
			fmt.Printf("%s %-8s %s/%s  (members: %d)\n", symbols[e.Action], e.Action, kind, e.Name, e.MemberCount)
		} else {
			fmt.Printf("%s %-8s %s/%s\n", symbols[e.Action], e.Action, kind, e.Name)
		}
		if e.OldSelector != nil && e.NewSelector != nil {
			old, _ := json.Marshal(e.OldSelector)
			new_, _ := json.Marshal(e.NewSelector)
			fmt.Printf("    - %s\n    + %s\n", old, new_)
		}
		if e.Error != "" {
			failed++
			fmt.Printf("    ! %s\n", e.Error)
		}
	}
	verb := "to change"
	if cmd == "apply" {
		verb = "changed"
	}
	fmt.Printf("%d view(s) %s, %d unchanged.\n", plan.Changes(), verb, len(plan.Entries)-plan.Changes())
	if failed > 0 {
		return fmt.Errorf("%d action(s) failed", failed)
	}
	return nil
}
