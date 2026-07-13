package dispatch

import (
	"context"
	"log/slog"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// TestDeleteRunJobs asserts cancellation cleanup deletes exactly the Jobs
// labeled with the run id (and leaves others), so a canceled Run's pods stop.
func TestDeleteRunJobs(t *testing.T) {
	job := func(name, runID string) *batchv1.Job {
		return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "stratt-jobs",
			Labels:    map[string]string{"stratt.dev/run-id": runID},
		}}
	}
	cs := fake.NewSimpleClientset(
		job("stratt-run-target-s0", "target"),
		job("stratt-run-target-s1", "target"),
		job("stratt-run-other-s0", "other"),
	)
	d := New(Config{Namespace: "stratt-jobs"}, cs, nil, slog.Default())

	if err := d.DeleteRunJobs(context.Background(), "target"); err != nil {
		t.Fatal(err)
	}

	remaining, err := cs.BatchV1().Jobs("stratt-jobs").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining.Items) != 1 || remaining.Items[0].Name != "stratt-run-other-s0" {
		t.Fatalf("expected only the other Run's job to remain, got %d: %+v", len(remaining.Items), remaining.Items)
	}
}
