// Package dispatch turns Steps into K8s Jobs — the only execution primitive
// (charter §3). Pods are ephemeral; their event stream is published to NATS
// as it happens and never lands in Postgres.
//
// Phase-0 note: the charter's event-shipper sidecar is approximated by the
// dispatcher following the pod log stream — each Actuator's tool content
// emits one JSON event per stdout line, which that Actuator interprets — and
// publishing each event to the bus. The pod stays dumb; nothing in-cluster
// needs NATS reachability yet. The sidecar shape lands with Sites (Phase 3).
package dispatch

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/core/internal/events"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
	"github.com/dstout-devops/stratt/types"
)

// Config for the dispatcher.
type Config struct {
	// Namespace the Jobs run in.
	Namespace string
	// EEImage is the execution-environment image (ansible-runner inside).
	EEImage string
	// FSGroup is the pod-level fsGroup: credential Secret volumes are
	// projected root:fsGroup mode 0440 (kubelet-owned, no world access), so
	// the EE's non-root user reads them via group. Must match the EE image's
	// runtime gid. <=0 defaults to 1000 (the stratt-ee `runner` user).
	FSGroup int64
	// Site is the execution locus this dispatcher runs at (ADR-0032). The hub
	// leaves it empty (⇒ "local"); a remote Site's stratt-agent sets its Site
	// name. Every published event and per-target result is stamped with it so
	// §1.8 descent shows *where* a target ran.
	Site string
}

// Dispatcher creates and follows execution Jobs.
type Dispatcher struct {
	cfg    Config
	client kubernetes.Interface
	bus    *events.Bus
	log    *slog.Logger
}

// New builds a Dispatcher on an existing clientset and event bus.
func New(cfg Config, client kubernetes.Interface, bus *events.Bus, log *slog.Logger) *Dispatcher {
	return &Dispatcher{cfg: cfg, client: client, bus: bus, log: log.With("component", "dispatch")}
}

// site is this dispatcher's execution locus, defaulting to the built-in local
// central cluster when unset (ADR-0032).
func (d *Dispatcher) site() string {
	if d.cfg.Site == "" {
		return types.LocalSite
	}
	return d.cfg.Site
}

// CredentialMount is a resolved CredentialRef pointer, ready to project into
// the pod spec (§2.5, ADR-0009). Pure metadata: the kubelet dereferences the
// Secret; this struct — like everything in the control plane — cannot hold
// material.
type CredentialMount struct {
	// RefName is the CredentialRef name (audit/labels).
	RefName string
	// SecretNamespace must be empty or equal to the Job's namespace —
	// secretKeyRef cannot cross namespaces; a mismatch fails dispatch.
	SecretNamespace string
	// SecretName is the K8s Secret holding the material.
	SecretName string
	// Injection is the per-key projection policy (env | file).
	Injection []types.CredentialInjection
}

// Interpreter turns a pod's stdout lines into task events + results. Both
// actuators.Actuator and actions.Action satisfy it, so the dispatcher serves
// Actuator tool-content and Action typed-operations through one pod path
// (§1.4 — no parallel execution stack; ADR-0031).
type Interpreter interface {
	Interpret(line []byte) (actuators.Interpreted, bool)
}

// Result summarizes one Job execution — per-target outcomes plus the facts
// each target reported (to project back with Run provenance, §8).
type Result struct {
	Succeeded bool
	// PerTarget maps target name → status (ok | changed | failed |
	// unreachable). Failures are sticky: a target that ever failed is never
	// downgraded by a later ok.
	PerTarget map[string]string
	// SiteByTarget maps target name → the execution locus it ran at (ADR-0032)
	// — "local" for the central cluster, a Site name for a remote leaf. Feeds
	// the Run's Sites union and §1.8 descent. Empty on legacy/local-only Runs.
	SiteByTarget map[string]string
	// Facts by target name → facet namespace → value.
	Facts map[string]map[string]json.RawMessage
	// Entities are tool-declared Entity observations (ADR-0017), projected
	// with Run provenance by the orchestration layer.
	Entities []actuators.EntityObservation
	// OutputsContract is the Step's tool-derived outputs schema, when the
	// tool emitted one (§2.2 rung 2).
	OutputsContract json.RawMessage
	// Outputs are an Action's typed output VALUES (ADR-0031), validated against
	// its output Contract and captured on the Run for cross-Step binding.
	Outputs json.RawMessage
	// Drift accumulates observed-vs-expected fragments per target from a
	// check-mode execution (ADR-0019) — redacted upstream, size-capped here
	// with a visible truncation marker (§1.8: truncation is never silent).
	Drift map[string][]json.RawMessage
	// SpawnLatency is Job-creation → pod-running, the §8 pod-spawn gate.
	SpawnLatency time.Duration
}

// driftCapBytes bounds the accumulated drift detail per target. Findings
// carry the capped detail; the full stream stays on the Run's events.
const driftCapBytes = 16 * 1024

// driftTruncated is the visible cap marker appended exactly once.
var driftTruncated = json.RawMessage(`{"truncated":true}`)

// Run creates the Job for the prepared Step slice and follows it to
// completion, publishing every task event to the bus under (runID, slice).
// The Actuator that prepared the spec interprets the pod's stdout lines —
// the dispatcher stays tool-agnostic.
// heartbeat is the activity-heartbeat callback the orchestration layer passes
// so Temporal can deliver cancellation to a long-running Job (nil = no-op, e.g.
// in tests). It is invoked from the pod-wait/log-follow/job-wait loops.
func hb(f func()) {
	if f != nil {
		f()
	}
}

func (d *Dispatcher) Run(ctx context.Context, runID string, slice int, spec actuators.JobSpec, act Interpreter, creds []CredentialMount, heartbeat func()) (*Result, error) {
	// Full Run id + slice index: the name keys ConfigMap and Job, and
	// AlreadyExists is treated as adoption — a truncated id would let two
	// Runs (or two slices) adopt each other's execution (ADR-0008 review).
	jobName := fmt.Sprintf("stratt-run-%s-s%d", runID, slice)
	created := time.Now()

	if err := d.createContent(ctx, jobName, spec.Files); err != nil {
		return nil, err
	}
	defer d.cleanupContent(jobName)

	if err := d.createJob(ctx, jobName, runID, spec, creds); err != nil {
		return nil, err
	}

	pod, err := d.waitForPod(ctx, jobName, heartbeat)
	if err != nil {
		return nil, err
	}
	spawn := time.Since(created)
	d.log.Info("pod running", "pod", pod, "slice", slice, "spawn", spawn.String())

	res := &Result{
		PerTarget:    map[string]string{},
		SiteByTarget: map[string]string{},
		Facts:        map[string]map[string]json.RawMessage{},
		SpawnLatency: spawn,
	}
	unclaimed, interpreted, err := d.followLogs(ctx, runID, slice, pod, act, res, heartbeat)
	if err != nil {
		return nil, err
	}
	ok, err := d.waitForJob(ctx, jobName, heartbeat)
	if err != nil {
		return nil, err
	}
	res.Succeeded = ok

	// Diagnostic floor (§1.8): surface retained uninterpretable output when
	// the tool never spoke its protocol at all, or the Job died abnormally
	// (a mid-stream crash after valid events). A clean failure (interpreted
	// events, Job reporting the play's own failure) keeps its stream free of
	// banner noise. Seqs are deterministic — retries dedup.
	if unclaimed.len() > 0 && (interpreted == 0 || !ok) {
		d.log.Warn("publishing diagnostic output", "pod", pod, "lines", unclaimed.len(), "interpreted", interpreted)
		for i, line := range unclaimed.lines {
			ev := types.RunEvent{
				RunID:   runID,
				Slice:   slice,
				Seq:     unclaimed.maxSeq + int64(i) + 1,
				Kind:    "diagnostic-output",
				Payload: map[string]any{"line": line},
			}
			if err := d.bus.Publish(ctx, ev); err != nil {
				return nil, err
			}
		}
	}
	return res, nil
}

// RunStream runs an EE-Job that speaks the sovereign port (the stratt-ansible shim,
// ADR-0051) and STREAMS its typed stdout to onResp — the subprocess transport beside
// gRPC. Unlike Run it FOLDS and INTERPRETS nothing (MF1/MF2): the hub-side governor
// (pluginhost.GovernStream) is the sole authority over the decoded ApplyResponses.
// The dispatcher only (a) decodes each line as a port ApplyResponse and, when it
// carries a TaskEvent, publishes it for §1.8 descent, then (b) hands the response to
// onResp. A line that is NOT a decodable ApplyResponse (a shim panic, a leaked
// banner) goes to the diagnostic ring (MF5), surfaced iff the Job died or spoke no
// typed shape at all. Returns the Job's exit success — descriptive only; the
// authoritative Succeeded is the governor's core-side fold, never this bool.
func (d *Dispatcher) RunStream(ctx context.Context, runID string, slice int, spec actuators.JobSpec, creds []CredentialMount, heartbeat func(), onResp func(*pluginv1.ApplyResponse)) (bool, time.Duration, error) {
	jobName := fmt.Sprintf("stratt-run-%s-s%d", runID, slice)
	created := time.Now()

	if err := d.createContent(ctx, jobName, spec.Files); err != nil {
		return false, 0, err
	}
	defer d.cleanupContent(jobName)

	if err := d.createJob(ctx, jobName, runID, spec, creds); err != nil {
		return false, 0, err
	}

	pod, err := d.waitForPod(ctx, jobName, heartbeat)
	if err != nil {
		return false, 0, err
	}
	spawn := time.Since(created)
	d.log.Info("pod running (typed transport)", "pod", pod, "slice", slice, "spawn", spawn.String())

	unclaimed, interpreted, err := d.followTyped(ctx, runID, slice, pod, onResp, heartbeat)
	if err != nil {
		return false, spawn, err
	}
	ok, err := d.waitForJob(ctx, jobName, heartbeat)
	if err != nil {
		return false, spawn, err
	}

	// Diagnostic floor (§1.8, MF5): the shim forwards ansible-runner banners as typed
	// diagnostics itself, so a line reaching the ring here is a line the shim never
	// managed to type at all (a panic, a torn stream) — surfaced only when the Job
	// died or emitted no typed shape.
	if unclaimed.len() > 0 && (interpreted == 0 || !ok) {
		d.log.Warn("publishing diagnostic output (typed transport)", "pod", pod, "lines", unclaimed.len(), "interpreted", interpreted)
		for i, line := range unclaimed.lines {
			ev := types.RunEvent{RunID: runID, Slice: slice, Seq: unclaimed.maxSeq + int64(i) + 1, Kind: "diagnostic-output", Payload: map[string]any{"line": line}}
			if err := d.bus.Publish(ctx, ev); err != nil {
				return false, spawn, err
			}
		}
	}
	return ok, spawn, nil
}

// followTyped follows the EE-Job's stdout, decoding each line as a port ApplyResponse.
// It publishes each response's TaskEvent for §1.8 descent (stamped RunID/Slice/Site,
// like followLogs) and hands the whole response to onResp for hub-side governance —
// it folds nothing. Undecodable lines land in the diagnostic ring (MF5).
func (d *Dispatcher) followTyped(ctx context.Context, runID string, slice int, pod string, onResp func(*pluginv1.ApplyResponse), heartbeat func()) (unclaimedRing, int, error) {
	var unclaimed unclaimedRing
	req := d.client.CoreV1().Pods(d.cfg.Namespace).GetLogs(pod, &corev1.PodLogOptions{Follow: true})
	stream, err := req.Stream(ctx)
	if err != nil {
		return unclaimed, 0, fmt.Errorf("dispatch: log stream: %w", err)
	}
	defer stream.Close()

	interpreted := 0
	var seq int64
	sc := bufio.NewScanner(stream)
	sc.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024) // fact payloads are large
	for sc.Scan() {
		hb(heartbeat)
		resp := &pluginv1.ApplyResponse{}
		if uerr := protojson.Unmarshal(sc.Bytes(), resp); uerr != nil {
			if line := strings.TrimSpace(sc.Text()); line != "" {
				unclaimed.lines = append(unclaimed.lines, line)
				if len(unclaimed.lines) > diagnosticRing {
					unclaimed.lines = unclaimed.lines[1:]
				}
			}
			continue
		}
		interpreted++
		seq++
		unclaimed.maxSeq = seq
		if ev := resp.GetEvent(); ev != nil {
			re := types.RunEvent{
				RunID: runID, Slice: slice, Seq: seq, Site: d.site(),
				Kind:    typedEventKind(ev),
				Target:  ev.GetFields()["host"],
				Payload: map[string]any{"message": ev.GetMessage()},
			}
			if ev.GetAt() != nil {
				re.At = ev.GetAt().AsTime()
			}
			if err := d.bus.Publish(ctx, re); err != nil {
				return unclaimed, interpreted, err
			}
		}
		onResp(resp)
	}
	return unclaimed, interpreted, nil
}

// typedEventKind renders a port TaskEvent's kind for the §1.8 event stream: the
// shim stamps Fields["kind"] (the ansible-runner event name, or "diagnostic"); a
// terminal falls back to "task-terminal".
func typedEventKind(ev *pluginv1.TaskEvent) string {
	if k := ev.GetFields()["kind"]; k != "" {
		return k
	}
	if ev.GetTerminal() {
		return "task-terminal"
	}
	return "task"
}

// cmKey flattens a JobSpec file path into a legal ConfigMap key (keys may not
// contain "/"); the pod mount restores the real path under /runner/.
func cmKey(path string) string { return strings.ReplaceAll(path, "/", "__") }

func (d *Dispatcher) createContent(ctx context.Context, name string, files map[string]string) error {
	data := make(map[string]string, len(files))
	for path, content := range files {
		data[cmKey(path)] = content
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"app.kubernetes.io/managed-by": "stratt"}},
		Data:       data,
	}
	// AlreadyExists is adoption: an activity retry re-entering, and the name
	// derives from the Run id, so the existing content is this Run's.
	_, err := d.client.CoreV1().ConfigMaps(d.cfg.Namespace).Create(ctx, cm, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("dispatch: create content: %w", err)
	}
	return nil
}

func (d *Dispatcher) cleanupContent(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = d.client.CoreV1().ConfigMaps(d.cfg.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

// DeleteRunJobs deletes every K8s Job for a Run (all slices), killing their
// pods (Background propagation), so a cancellation actually stops execution —
// nothing else deletes a Job before its finish TTL. Jobs carry the
// stratt.dev/run-id label (createJob); ConfigMaps are cleaned by the returning
// Execute activity's own defer. Idempotent: absent Jobs are not an error.
func (d *Dispatcher) DeleteRunJobs(ctx context.Context, runID string) error {
	sel := "stratt.dev/run-id=" + runID
	jobs, err := d.client.BatchV1().Jobs(d.cfg.Namespace).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return fmt.Errorf("dispatch: list run jobs %s: %w", runID, err)
	}
	bg := metav1.DeletePropagationBackground
	for i := range jobs.Items {
		if derr := d.client.BatchV1().Jobs(d.cfg.Namespace).Delete(ctx, jobs.Items[i].Name,
			metav1.DeleteOptions{PropagationPolicy: &bg}); derr != nil && !apierrors.IsNotFound(derr) {
			err = fmt.Errorf("dispatch: delete job %s: %w", jobs.Items[i].Name, derr)
		}
	}
	return err
}

func (d *Dispatcher) createJob(ctx context.Context, name, runID string, spec actuators.JobSpec, creds []CredentialMount) error {
	backoff := int32(0)
	ttl := int32(3600)

	image := spec.Image
	if image == "" {
		image = d.cfg.EEImage
	}
	// One mount per content file, restored to its JobSpec path. Sorted so the
	// pod spec is deterministic (map iteration is not).
	paths := make([]string, 0, len(spec.Files))
	for p := range spec.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	mounts := []corev1.VolumeMount{{Name: "workdir", MountPath: "/runner/artifacts"}}
	for _, p := range paths {
		mounts = append(mounts, corev1.VolumeMount{Name: "content", MountPath: "/runner/" + p, SubPath: cmKey(p)})
	}

	// Credential projection (§2.5, ADR-0009): the pod spec references the
	// Secret; the KUBELET resolves material — the control plane composes
	// coordinates only. env → secretKeyRef; file → read-only Secret volume
	// items (0400) under /runner/credentials/.
	var env []corev1.EnvVar
	// Actuator-supplied env (JobSpec.Env, ADR-0016): plain values the
	// Prepare step computed (e.g. the state-backend credential) — sorted for
	// a deterministic pod spec.
	envKeys := make([]string, 0, len(spec.Env))
	for k := range spec.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		env = append(env, corev1.EnvVar{Name: k, Value: spec.Env[k]})
	}
	// A CredentialRef env injection colliding with an actuator-set name
	// would be K8s last-wins ambiguity — refuse instead (ADR-0016 note).
	for _, c := range creds {
		for _, inj := range c.Injection {
			if inj.As == types.InjectEnv {
				if _, clash := spec.Env[inj.Name]; clash {
					return fmt.Errorf("dispatch: credential_ref %s env %q collides with an actuator-set variable", c.RefName, inj.Name)
				}
			}
		}
	}
	var volumes []corev1.Volume
	// 0440 + pod fsGroup (not 0400): Secret volume files are root-owned by
	// the kubelet, and the EE runs non-root — group is the only read path
	// that doesn't require running the tool as root. No world access.
	fileMode := int32(0o440)
	fsGroup := d.cfg.FSGroup
	if fsGroup <= 0 {
		fsGroup = 1000
	}
	for ci, c := range creds {
		if c.SecretNamespace != "" && c.SecretNamespace != d.cfg.Namespace {
			return fmt.Errorf("dispatch: credential_ref %s: secret namespace %q differs from job namespace %q (secretKeyRef cannot cross namespaces)",
				c.RefName, c.SecretNamespace, d.cfg.Namespace)
		}
		var items []corev1.KeyToPath
		for _, inj := range c.Injection {
			switch inj.As {
			case types.InjectEnv:
				env = append(env, corev1.EnvVar{
					Name: inj.Name,
					ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: c.SecretName},
						Key:                  inj.Key,
					}},
				})
			case types.InjectFile:
				items = append(items, corev1.KeyToPath{Key: inj.Key, Path: inj.Name, Mode: &fileMode})
			}
		}
		if len(items) > 0 {
			volName := fmt.Sprintf("credential-%d", ci)
			volumes = append(volumes, corev1.Volume{
				Name: volName,
				VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
					SecretName: c.SecretName,
					Items:      items,
				}},
			})
			mounts = append(mounts, corev1.VolumeMount{
				Name:      volName,
				MountPath: "/runner/credentials/" + c.RefName,
				ReadOnly:  true,
			})
		}
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "stratt",
				"stratt.dev/run-id":            runID,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"stratt.dev/run-id": runID},
				},
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{FSGroup: &fsGroup},
					RestartPolicy:   corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:         "ee",
						Image:        image,
						Command:      spec.Command,
						Env:          env,
						VolumeMounts: mounts,
					}},
					Volumes: append([]corev1.Volume{
						{Name: "content", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: name},
						}}},
						{Name: "workdir", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					}, volumes...),
				},
			},
		},
	}
	// AlreadyExists is adoption: a Temporal activity retry, and the Job name
	// derives from the Run id — follow the existing Job instead of failing
	// the retry (activities must be idempotent).
	if _, err := d.client.BatchV1().Jobs(d.cfg.Namespace).Create(ctx, job, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("dispatch: create job: %w", err)
	}
	return nil
}

// waitForPod polls until the Job's pod is running or terminal.
func (d *Dispatcher) waitForPod(ctx context.Context, jobName string, heartbeat func()) (string, error) {
	for {
		pods, err := d.client.CoreV1().Pods(d.cfg.Namespace).List(ctx, metav1.ListOptions{
			LabelSelector: "job-name=" + jobName,
		})
		if err != nil {
			return "", fmt.Errorf("dispatch: list pods: %w", err)
		}
		for _, p := range pods.Items {
			switch p.Status.Phase {
			case corev1.PodRunning, corev1.PodSucceeded, corev1.PodFailed:
				return p.Name, nil
			}
		}
		hb(heartbeat)
		select {
		case <-time.After(250 * time.Millisecond):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// diagnosticRing caps how many uninterpretable lines are retained for the
// §1.8 diagnostic floor.
const diagnosticRing = 50

// statusRank orders per-target statuses for the escalating fold. Unknown
// (including the empty "no status yet") ranks lowest.
func statusRank(status string) int {
	switch status {
	case actuators.StatusChanged:
		return 1
	case actuators.StatusFailed, actuators.StatusUnreachable:
		return 2
	default:
		return 0
	}
}

// unclaimedRing is the bounded retention of lines an Actuator's Interpret
// rejected, plus the highest interpreted seq (the base for deterministic
// synthetic diagnostic seqs).
type unclaimedRing struct {
	lines  []string
	maxSeq int64
}

func (u unclaimedRing) len() int { return len(u.lines) }

// followLogs streams the pod's stdout, publishing each interpreted event to
// the bus and folding per-target results and facts into res. Lines the
// Actuator cannot interpret are retained (bounded) and returned so Run can
// publish them as the §1.8 diagnostic floor once the Job's verdict is known.
// The Run-level stream-end marker is published by the Workflow once every
// slice has finished.
func (d *Dispatcher) followLogs(ctx context.Context, runID string, slice int, pod string, act Interpreter, res *Result, heartbeat func()) (unclaimedRing, int, error) {
	var unclaimed unclaimedRing
	req := d.client.CoreV1().Pods(d.cfg.Namespace).GetLogs(pod, &corev1.PodLogOptions{Follow: true})
	stream, err := req.Stream(ctx)
	if err != nil {
		return unclaimed, 0, fmt.Errorf("dispatch: log stream: %w", err)
	}
	defer stream.Close()

	interpreted := 0
	driftBytes := map[string]int{}
	sc := bufio.NewScanner(stream)
	sc.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024) // fact payloads are large
	for sc.Scan() {
		hb(heartbeat) // each output line keeps the activity heartbeat alive
		iv, ok := act.Interpret(sc.Bytes())
		if !ok {
			if line := strings.TrimSpace(sc.Text()); line != "" {
				unclaimed.lines = append(unclaimed.lines, line)
				if len(unclaimed.lines) > diagnosticRing {
					unclaimed.lines = unclaimed.lines[1:]
				}
			}
			continue
		}
		interpreted++
		if iv.Event.Seq > unclaimed.maxSeq {
			unclaimed.maxSeq = iv.Event.Seq
		}
		iv.Event.RunID = runID
		iv.Event.Slice = slice
		iv.Event.Site = d.site() // §1.8: descent shows where this event ran
		if err := d.bus.Publish(ctx, iv.Event); err != nil {
			return unclaimed, interpreted, err
		}
		if r := iv.Result; r != nil && r.Target != "" {
			status := r.Status
			if status == "" { // seam default for status-less actuators
				status = actuators.StatusOK
				if r.Failed {
					status = actuators.StatusFailed
				}
			}
			// Statuses only escalate: ok < changed < failed/unreachable.
			// A later ok (e.g. a skipped task) never hides that the target
			// was mutated or failed earlier in the play.
			if statusRank(status) >= statusRank(res.PerTarget[r.Target]) {
				res.PerTarget[r.Target] = status
			}
			res.SiteByTarget[r.Target] = d.site()
		}
		if iv.Facts != nil && iv.Event.Target != "" {
			// Accumulate per namespace across the play's fact-emitting events:
			// a gather play may project several Facet namespaces from separate
			// tasks (e.g. os.kernel from setup, os.hardening.* from a later
			// set_fact), and each namespace is its own seam — a later event
			// must not erase an earlier namespace. Same namespace twice: last
			// wins.
			byNS := res.Facts[iv.Event.Target]
			if byNS == nil {
				byNS = map[string]json.RawMessage{}
				res.Facts[iv.Event.Target] = byNS
			}
			for ns, v := range iv.Facts {
				byNS[ns] = v
			}
		}
		if len(iv.Entities) > 0 {
			res.Entities = append(res.Entities, iv.Entities...)
		}
		if len(iv.OutputsContract) > 0 {
			res.OutputsContract = iv.OutputsContract
		}
		if len(iv.Outputs) > 0 {
			res.Outputs = iv.Outputs
		}
		if len(iv.Drift) > 0 && iv.Event.Target != "" {
			target := iv.Event.Target
			if res.Drift == nil {
				res.Drift = map[string][]json.RawMessage{}
			}
			switch {
			case driftBytes[target] > driftCapBytes:
				// already marked truncated
			case driftBytes[target]+len(iv.Drift) > driftCapBytes:
				res.Drift[target] = append(res.Drift[target], driftTruncated)
				driftBytes[target] = driftCapBytes + 1
			default:
				res.Drift[target] = append(res.Drift[target], iv.Drift)
				driftBytes[target] += len(iv.Drift)
			}
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return unclaimed, interpreted, fmt.Errorf("dispatch: read logs: %w", err)
	}
	return unclaimed, interpreted, nil
}

// waitForJob polls the Job to a terminal condition.
func (d *Dispatcher) waitForJob(ctx context.Context, jobName string, heartbeat func()) (bool, error) {
	for {
		job, err := d.client.BatchV1().Jobs(d.cfg.Namespace).Get(ctx, jobName, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Errorf("dispatch: get job: %w", err)
		}
		if job.Status.Succeeded > 0 {
			return true, nil
		}
		if job.Status.Failed > 0 {
			return false, nil
		}
		hb(heartbeat)
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}
