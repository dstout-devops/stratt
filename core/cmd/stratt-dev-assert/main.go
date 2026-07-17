// Command stratt-dev-assert is the functional-e2e driver for the local kind
// deploy: it port-forwards to the in-cluster strattd (client-go, no kubectl),
// launches plugin Runs over the HTTP API, and asserts the outcome — proving BOTH
// plugin transports actually work in Kubernetes.
//
//	-mode=actuator : seed already ran; launch a `script` then an `ansible` Run
//	                 against the dev-hosts View, assert each reaches terminal
//	                 success AND a real K8s Job (labeled stratt.dev/run-id=<id>)
//	                 executed and completed — the EE-Job Actuator transport.
//	-mode=syncer   : wait for the vcenter gRPC Syncer plugin to project vcsim
//	                 `vm` Entities (GET /views/dev-vms/entities), then launch a
//	                 `script` Run against those REAL targets and assert success.
//
// Exit 0 = green. Any assertion failure is fatal (non-zero).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

const principal = "dev-runner" // matches dev/declarations/authz/tuples.yaml

func main() {
	mode := flag.String("mode", "actuator", "actuator | syncer")
	kctx := flag.String("context", "kind-stratt-dev", "kube context")
	ns := flag.String("namespace", "stratt", "namespace strattd runs in")
	flag.Parse()

	cfg, cs := mustKube(*kctx)
	pod := mustPod(cs, *ns, "app.kubernetes.io/name=stratt")
	log.Printf("port-forwarding to pod %s/%s …", *ns, pod)
	local, stop := mustForward(cfg, *ns, pod)
	defer close(stop)
	c := &apiClient{base: fmt.Sprintf("http://127.0.0.1:%d/api/v1", local)}
	c.waitReady()

	switch *mode {
	case "actuator":
		assertActuator(c, cs, *ns)
	case "syncer":
		assertSyncer(c, cs, *ns)
	default:
		log.Fatalf("unknown -mode %q", *mode)
	}
	log.Printf("✓ mode=%s: all assertions passed", *mode)
}

// ── the two functional assertions ───────────────────────────────────────────

func assertActuator(c *apiClient, cs *kubernetes.Clientset, ns string) {
	scriptParams := map[string]any{"script": "echo hello from $STRATT_TARGET_NAME; exit 0"}
	ansibleParams := map[string]any{"play": "- hosts: all\n  gather_facts: false\n  tasks:\n    - ansible.builtin.debug: {msg: hello}\n"}
	for _, tc := range []struct {
		actuator string
		params   map[string]any
	}{
		{"script", scriptParams},
		{"ansible", ansibleParams},
	} {
		runID := c.startRun("dev-hosts", tc.actuator, tc.params)
		log.Printf("[%s] launched run %s against dev-hosts", tc.actuator, runID)
		out := c.awaitSuccess(runID, 120*time.Second)
		assertJobRan(cs, ns, runID)
		log.Printf("✓ [%s] run %s succeeded, governed K8s Job ran (outputs: %v)", tc.actuator, runID, out)
	}
}

func assertSyncer(c *apiClient, cs *kubernetes.Clientset, ns string) {
	// The vcenter gRPC plugin (in-cluster Deployment) enumerates vcsim and
	// streams ObservedEntities; strattd governs + projects them. Wait for the
	// dev-vms View to resolve to real vcsim VMs.
	deadline := time.Now().Add(150 * time.Second)
	var n int
	for time.Now().Before(deadline) {
		n = c.resolveViewCount("dev-vms")
		if n > 0 {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if n == 0 {
		log.Fatalf("vcenter syncer projected no vm Entities into dev-vms within timeout")
	}
	log.Printf("✓ vcenter gRPC Syncer plugin projected %d vm Entities into dev-vms", n)

	// Actuate the REAL projected targets (no synthetic seed).
	runID := c.startRun("dev-vms", "script", map[string]any{"script": "echo hello; exit 0"})
	log.Printf("[script] launched run %s against dev-vms (%d real targets)", runID, n)
	out := c.awaitSuccess(runID, 150*time.Second)
	assertJobRan(cs, ns, runID)
	log.Printf("✓ [script] run %s over vcsim targets succeeded, governed K8s Job ran (outputs: %v)", runID, out)
}

func assertJobRan(cs *kubernetes.Clientset, ns, runID string) {
	jobs, err := cs.BatchV1().Jobs(ns).List(context.Background(), metav1.ListOptions{
		LabelSelector: "stratt.dev/run-id=" + runID,
	})
	if err != nil {
		log.Fatalf("list jobs for run %s: %v", runID, err)
	}
	if len(jobs.Items) == 0 {
		log.Fatalf("run %s succeeded but NO K8s Job labeled stratt.dev/run-id=%s exists — the EE-Job transport did not run in-cluster", runID, runID)
	}
}

// ── HTTP API client (X-Stratt-Principal dev header) ─────────────────────────

type apiClient struct{ base string }

func (c *apiClient) do(method, path string, body any) (*http.Response, []byte) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		log.Fatalf("build request %s %s: %v", method, path, err)
	}
	req.Header.Set("X-Stratt-Principal", principal)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("request %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func (c *apiClient) waitReady() {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		resp, _ := c.do(http.MethodGet, "/views", nil)
		if resp.StatusCode == http.StatusOK {
			return
		}
		time.Sleep(2 * time.Second)
	}
	log.Fatal("strattd API not ready (GET /views) within timeout")
}

func (c *apiClient) startRun(view, actuator string, params map[string]any) string {
	body := map[string]any{"viewName": view, "actuator": actuator, "params": params}
	// The declarations reconcile (View + runner grant) may lag pod-ready; retry
	// a 4xx (View not yet reconciled / grant not yet loaded) briefly.
	deadline := time.Now().Add(60 * time.Second)
	for {
		resp, data := c.do(http.MethodPost, "/runs", body)
		if resp.StatusCode == http.StatusCreated {
			var r struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(data, &r); err != nil || r.ID == "" {
				log.Fatalf("startRun %s/%s: decode id: %v (%s)", view, actuator, err, data)
			}
			return r.ID
		}
		if time.Now().After(deadline) {
			log.Fatalf("startRun %s/%s: HTTP %d after retries: %s", view, actuator, resp.StatusCode, data)
		}
		time.Sleep(3 * time.Second)
	}
}

func (c *apiClient) awaitSuccess(runID string, timeout time.Duration) map[string]any {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, data := c.do(http.MethodGet, "/runs/"+runID, nil)
		if resp.StatusCode == http.StatusOK {
			var r struct {
				Status  string         `json:"status"`
				Outputs map[string]any `json:"outputs"`
			}
			if err := json.Unmarshal(data, &r); err != nil {
				log.Fatalf("await run %s: decode: %v (%s)", runID, err, data)
			}
			switch r.Status {
			case "succeeded":
				if f, ok := r.Outputs["failed"].(float64); ok && f > 0 {
					log.Fatalf("run %s reported succeeded but %v targets failed: %v", runID, f, r.Outputs)
				}
				return r.Outputs
			case "failed", "canceled", "partial":
				log.Fatalf("run %s terminal but not success: %s (%v)", runID, r.Status, r.Outputs)
			}
		}
		time.Sleep(2 * time.Second)
	}
	log.Fatalf("run %s did not reach terminal success within %s", runID, timeout)
	return nil
}

func (c *apiClient) resolveViewCount(view string) int {
	resp, data := c.do(http.MethodGet, "/views/"+view+"/entities", nil)
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	var r struct {
		Entities []json.RawMessage `json:"entities"`
	}
	if json.Unmarshal(data, &r) != nil {
		return 0
	}
	return len(r.Entities)
}

// ── kube plumbing (client-go) ───────────────────────────────────────────────

func mustKube(kctx string) (*rest.Config, *kubernetes.Clientset) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{CurrentContext: kctx}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		log.Fatalf("kube config (context %s): %v", kctx, err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Fatalf("kube client: %v", err)
	}
	return cfg, cs
}

func mustPod(cs *kubernetes.Clientset, ns, selector string) string {
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		pods, err := cs.CoreV1().Pods(ns).List(context.Background(), metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			log.Fatalf("list pods %s/%s: %v", ns, selector, err)
		}
		for _, p := range pods.Items {
			if p.Status.Phase == "Running" {
				ready := true
				for _, cs := range p.Status.ContainerStatuses {
					if !cs.Ready {
						ready = false
					}
				}
				if ready {
					return p.Name
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	log.Fatalf("no ready pod for %s in %s within timeout", selector, ns)
	return ""
}

func mustForward(cfg *rest.Config, ns, pod string) (int, chan struct{}) {
	roundTripper, upgrader, err := spdy.RoundTripperFor(cfg)
	if err != nil {
		log.Fatalf("spdy roundtripper: %v", err)
	}
	host, err := url.Parse(cfg.Host)
	if err != nil {
		log.Fatalf("parse kube host %q: %v", cfg.Host, err)
	}
	serverURL := &url.URL{
		Scheme: "https",
		Path:   fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", ns, pod),
		Host:   host.Host,
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: roundTripper}, http.MethodPost, serverURL)
	stopCh := make(chan struct{})
	readyCh := make(chan struct{})
	fw, err := portforward.New(dialer, []string{"0:8080"}, stopCh, readyCh, io.Discard, os.Stderr)
	if err != nil {
		log.Fatalf("portforward.New: %v", err)
	}
	go func() {
		if err := fw.ForwardPorts(); err != nil {
			log.Printf("port-forward ended: %v", err)
		}
	}()
	select {
	case <-readyCh:
	case <-time.After(30 * time.Second):
		log.Fatal("port-forward not ready within timeout")
	}
	ports, err := fw.GetPorts()
	if err != nil || len(ports) == 0 {
		log.Fatalf("port-forward get ports: %v", err)
	}
	return int(ports[0].Local), stopCh
}
