// Package dispatch turns Steps into K8s Jobs — the only execution primitive
// (charter §3). Pods are ephemeral; their event stream is published to NATS
// as it happens and never lands in Postgres.
//
// Phase-0 note: the charter's event-shipper sidecar is approximated by the
// dispatcher following the pod log stream (`ansible-runner run -j` emits one
// JSON event per line) and publishing each event to the bus. The pod stays
// dumb; nothing in-cluster needs NATS reachability yet. The sidecar shape
// lands with Sites (Phase 3).
package dispatch

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/dstout-devops/stratt/core/internal/actuators/ansible"
	"github.com/dstout-devops/stratt/core/internal/events"
	"github.com/dstout-devops/stratt/types"
)

// Config for the dispatcher.
type Config struct {
	// Namespace the Jobs run in.
	Namespace string
	// EEImage is the execution-environment image (ansible-runner inside).
	EEImage string
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

// Result summarizes one Job execution — per-target outcomes plus the facts
// each target reported (to project back with Run provenance, §8).
type Result struct {
	Succeeded bool
	PerTarget map[string]string // target name → ok | failed
	// Facts by target name → facet namespace → value.
	Facts map[string]map[string]json.RawMessage
	// SpawnLatency is Job-creation → pod-running, the §8 pod-spawn gate.
	SpawnLatency time.Duration
}

// Run creates the Job for the given content and follows it to completion,
// publishing every task event to the bus under runID.
func (d *Dispatcher) Run(ctx context.Context, runID string, content ansible.Content) (*Result, error) {
	// Full Run id: the name keys ConfigMap and Job, and AlreadyExists is
	// treated as adoption — a truncated id would let two Runs sharing a
	// prefix adopt each other's execution (charter-guardian, ADR-0008).
	jobName := "stratt-run-" + runID
	created := time.Now()

	if err := d.createContent(ctx, jobName, content); err != nil {
		return nil, err
	}
	defer d.cleanupContent(jobName)

	if err := d.createJob(ctx, jobName, runID); err != nil {
		return nil, err
	}

	pod, err := d.waitForPod(ctx, jobName)
	if err != nil {
		return nil, err
	}
	spawn := time.Since(created)
	d.log.Info("pod running", "pod", pod, "spawn", spawn.String())

	res := &Result{
		PerTarget:    map[string]string{},
		Facts:        map[string]map[string]json.RawMessage{},
		SpawnLatency: spawn,
	}
	if err := d.followLogs(ctx, runID, pod, res); err != nil {
		return nil, err
	}
	ok, err := d.waitForJob(ctx, jobName)
	if err != nil {
		return nil, err
	}
	res.Succeeded = ok
	return res, nil
}

func (d *Dispatcher) createContent(ctx context.Context, name string, content ansible.Content) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"app.kubernetes.io/managed-by": "stratt"}},
		Data: map[string]string{
			"play.yml": content.Play,
			"hosts":    content.Hosts,
		},
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

func (d *Dispatcher) createJob(ctx context.Context, name, runID string) error {
	backoff := int32(0)
	ttl := int32(3600)
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
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:  "ee",
						Image: d.cfg.EEImage,
						// -j: one JSON event per stdout line — the event
						// stream the dispatcher ships to NATS.
						Command: []string{"ansible-runner", "run", "/runner", "-p", "play.yml", "-j"},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "content", MountPath: "/runner/project/play.yml", SubPath: "play.yml"},
							{Name: "content", MountPath: "/runner/inventory/hosts", SubPath: "hosts"},
							{Name: "workdir", MountPath: "/runner/artifacts"},
						},
					}},
					Volumes: []corev1.Volume{
						{Name: "content", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: name},
						}}},
						{Name: "workdir", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
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
func (d *Dispatcher) waitForPod(ctx context.Context, jobName string) (string, error) {
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
		select {
		case <-time.After(250 * time.Millisecond):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// followLogs streams the pod's stdout, publishing each runner event to the
// bus and folding per-target results and facts into res.
func (d *Dispatcher) followLogs(ctx context.Context, runID, pod string, res *Result) error {
	req := d.client.CoreV1().Pods(d.cfg.Namespace).GetLogs(pod, &corev1.PodLogOptions{Follow: true})
	stream, err := req.Stream(ctx)
	if err != nil {
		return fmt.Errorf("dispatch: log stream: %w", err)
	}
	defer stream.Close()

	sc := bufio.NewScanner(stream)
	sc.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024) // fact payloads are large
	for sc.Scan() {
		ev, ok := ansible.ParseEvent(sc.Bytes())
		if !ok {
			continue
		}
		if err := d.bus.Publish(ctx, ansible.ToRunEvent(runID, ev)); err != nil {
			return err
		}
		if host, ok, failed := ansible.HostResult(ev); host != "" && (ok || failed) {
			if failed {
				res.PerTarget[host] = "failed"
			} else if res.PerTarget[host] != "failed" {
				res.PerTarget[host] = "ok"
			}
		}
		if facts := ansible.ExtractFacts(ev); facts != nil {
			if host, _ := ev.EventData["host"].(string); host != "" {
				res.Facts[host] = facts
			}
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("dispatch: read logs: %w", err)
	}
	// Terminal marker for tails (§1.8: the descent has a floor, not a gap).
	return d.bus.Publish(ctx, types.RunEvent{RunID: runID, Kind: "stream-end"})
}

// waitForJob polls the Job to a terminal condition.
func (d *Dispatcher) waitForJob(ctx context.Context, jobName string) (bool, error) {
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
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}
