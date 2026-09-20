package worker

import (
	"testing"
	"time"
)

func TestRegistryAddAndGet(t *testing.T) {
	registery := NewRegistry()
	job, err := NewJob(
		"job-1",
		"zhu",
		[]string{"ls", "-la"},
		time.Now(),
	)
	if err != nil {
		t.Fatalf("newjob failed %v", err)
	}

	if err := registery.Add(job); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	got, err := registery.Get(job.id)
	if err != nil {
		t.Fatalf("fail to get job %v", err)
	}

	if got != job {
		t.Fatalf("got %v and job %v not equal", got, job)
	}

}
