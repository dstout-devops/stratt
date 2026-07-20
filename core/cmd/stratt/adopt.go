package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/dstout-devops/stratt/core/internal/awximport"
)

// runAdopt dispatches `stratt adopt <kind> <identity>` (ADR-0086/0088): materialize an
// already-observed foreign object into a reviewable Git-declared Named-Kind bundle. We never
// import — the projection is the always-on catalog. This is API-first (§1.6): the CLI is one
// client of POST /api/v1/adoptions. The server resolves the object from the graph and launches
// an async Run whose pod does the credential-bearing deep-read + transform, resolving the AWX
// CredentialRef via the SecretBroker at pod spawn (§2.5, use-without-read — the CLI never
// custodies the AWX token, only names the credential). The CLI posts, then polls the Run for
// the emitted bundle.
func runAdopt(args []string) error {
	fs := flag.NewFlagSet("adopt", flag.ExitOnError)
	endpoint := fs.String("endpoint", "", "source system base URL for the targeted deep-read, e.g. https://awx.example.com")
	credRef := fs.String("credential-ref", envOr("STRATT_AWX_CREDENTIAL_REF", ""), "CredentialRef name holding the AWX token (default $STRATT_AWX_CREDENTIAL_REF); resolved in-pod, never read by the caller (§2.5)")
	server := fs.String("s", envOr("STRATT_SERVER", "http://localhost:8080"), "control-plane base URL")
	out := fs.String("o", "", "output directory for the adopted declaration bundle (must be fresh)")
	timeout := fs.Duration("timeout", 2*time.Minute, "how long to poll the adoption Run before giving up")
	_ = fs.Parse(args)

	rest := fs.Args()
	if len(rest) != 2 {
		return fmt.Errorf("usage: stratt adopt <kind> <identity> --endpoint <url> --credential-ref <name> [-s server] -o <out-dir>\n" +
			"  e.g. stratt adopt ansible.template ctrl-a/10 --endpoint https://awx.example.com --credential-ref awx-token -o ./adopted")
	}
	if *endpoint == "" || *out == "" || *credRef == "" {
		return fmt.Errorf("adopt: --endpoint, --credential-ref, and -o are required")
	}

	// Launch the async adoption Run.
	body, _ := json.Marshal(map[string]string{
		"kind": rest[0], "identity": rest[1], "endpoint": *endpoint, "credentialRef": *credRef,
	})
	resp, err := http.Post(*server+"/api/v1/adoptions", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	respBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("adopt: %s: %s", resp.Status, string(respBody))
	}
	var accepted struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(respBody, &accepted); err != nil || accepted.RunID == "" {
		return fmt.Errorf("adopt: decode accept response: %w (%s)", err, string(respBody))
	}
	fmt.Printf("stratt: adoption Run %s launched; polling…\n", accepted.RunID)

	// Poll the Run until terminal.
	status, files, report, err := pollAdoption(*server, accepted.RunID, *timeout)
	if err != nil {
		return err
	}
	if status != "succeeded" {
		return fmt.Errorf("adopt: Run %s ended %s (descend: stratt run %s)", accepted.RunID, status, accepted.RunID)
	}
	if err := awximport.WriteBundle(*out, &awximport.Emit{Files: files, Report: report}); err != nil {
		return err
	}
	fmt.Printf("stratt: adopted %s %s → %d declaration file(s) in %s\n", rest[0], rest[1], len(files), *out)
	fmt.Printf("stratt: review %s/migration-report.md, then run: stratt plan -d %s\n", *out, *out)
	return nil
}

// pollAdoption polls GET /adoptions/{runId} until the Run reaches a terminal state or the
// deadline passes, returning the terminal status and (on success) the emitted bundle.
func pollAdoption(server, runID string, timeout time.Duration) (status string, files map[string]string, report string, err error) {
	deadline := time.Now().Add(timeout)
	for {
		resp, gerr := http.Get(server + "/api/v1/adoptions/" + runID)
		if gerr != nil {
			return "", nil, "", gerr
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", nil, "", fmt.Errorf("adopt: poll %s: %s: %s", runID, resp.Status, string(raw))
		}
		var st struct {
			Status string            `json:"status"`
			Files  map[string]string `json:"files"`
			Report string            `json:"report"`
		}
		if uerr := json.Unmarshal(raw, &st); uerr != nil {
			return "", nil, "", fmt.Errorf("adopt: decode status: %w", uerr)
		}
		switch st.Status {
		case "succeeded", "failed", "canceled", "partial":
			return st.Status, st.Files, st.Report, nil
		}
		if time.Now().After(deadline) {
			return "", nil, "", fmt.Errorf("adopt: Run %s still %s after %s (descend: stratt run %s)", runID, st.Status, timeout, runID)
		}
		time.Sleep(time.Second)
	}
}
