package worker

import (
	"errors"
	"sync"
)

var (
	ErrNilJob       = errors.New("empty job")
	ErrDuplicateJob = errors.New("dupicate job")
	ErrJobNotFound  = errors.New("invalid job")
)

type Registry struct {
	mu   sync.RWMutex
	jobs map[string]*Job
}

func NewRegistry() *Registry {

	return &Registry{
		mu:   sync.RWMutex{},
		jobs: make(map[string]*Job),
	}
}

func (r *Registry) Add(job *Job) error {
	if job == nil {
		return ErrNilJob
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.jobs[job.id]; ok {
		return ErrDuplicateJob
	}
	r.jobs[job.id] = job
	return nil
}

func (r *Registry) Get(id string) (*Job, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	job, ok := r.jobs[id]
	if !ok {
		return nil, ErrJobNotFound
	}

	return job, nil
}
