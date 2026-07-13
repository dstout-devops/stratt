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
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]

	// `import awx` is a one-shot migration tool with its own flags; it reads
	// AWX and writes a local bundle, never touching the platform API.
	if cmd == "import" {
		if err := runImport(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "stratt:", err)
			os.Exit(1)
		}
		return
	}

	// `bundle` packages Step content into a cosign-signable OCI Bundle for
	// pull-mode Sites (ADR-0032); it talks to a registry, not the platform API.
	if cmd == "bundle" {
		if err := runBundle(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "stratt:", err)
			os.Exit(1)
		}
		return
	}

	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	dir := fs.String("d", ".", "declarations directory (contains views/)")
	server := fs.String("s", envOr("STRATT_SERVER", "http://localhost:8080"), "control-plane base URL")
	_ = fs.Parse(os.Args[2:])

	switch cmd {
	case "plan", "apply":
		if err := run(cmd, *dir, *server); err != nil {
			fmt.Fprintln(os.Stderr, "stratt:", err)
			os.Exit(1)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  stratt <plan|apply> [-d declarations-dir] [-s server-url]
  stratt import awx --endpoint <url> [--token <t>] -o <out-dir>
  stratt bundle push <content-dir> <ref> --name N --version V --actuator A`)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func run(cmd, dir, server string) error {
	decls, err := desiredstate.ParseDir(dir)
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
