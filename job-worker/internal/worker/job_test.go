package worker

import (
	"reflect"
	"testing"
	"time"
)

func TestNewJob(t *testing.T) {
	startedAt := time.Date(2026, time.September, 12, 10, 0, 0, 0, time.UTC)
	argv := []string{"ls", "-la"}
	output, err := NewOutput(t.TempDir())
	if err != nil {
		t.Fatalf("create job output failed! %v", err)
	}
	job, err := NewJob("job-1", "alice", argv, startedAt, output)
	if err != nil {
		t.Fatalf("newJob() error = %v", err)
	}
	got := job.Snapshot()
	want := JobSnapshot{
		ID:        "job-1",
		Owner:     "alice",
		Argv:      []string{"ls", "-la"},
		StartedAt: startedAt,
		State:     StateRunning,
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Snapshot not equal to Job snapshot = %v, job = %v", got, want)
	}
}
