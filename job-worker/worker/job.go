package worker

import (
	"errors"
	"sync"
	"time"
)

var (
	ErrEmptyJobID             = errors.New("job ID is empty")
	ErrEmptyOwner             = errors.New("job owner is empty")
	ErrEmptyArgv              = errors.New("job argv is empty - no any command")
	ErrInvalidStateTransition = errors.New("invalid job state transition")
	ErrNilJobOutput           = errors.New("job output is nil")
)

type Job struct {
	mu sync.RWMutex

	id        string
	owner     string
	argv      []string
	startedAt time.Time
	state     State
	output    *Output
}

type JobSnapshot struct {
	ID        string
	Owner     string
	Argv      []string
	StartedAt time.Time
	State     State
}

func NewJob(id, owner string, argv []string, startedAt time.Time, output *Output) (*Job, error) {
	switch {
	case id == "":
		return nil, ErrEmptyJobID
	case owner == "":
		return nil, ErrEmptyOwner
	case len(argv) == 0:
		return nil, ErrEmptyArgv
	case output == nil:
		return nil, ErrNilJobOutput
	}

	return &Job{
		id:        id,
		owner:     owner,
		argv:      append([]string(nil), argv...),
		startedAt: startedAt,
		state:     StateRunning,
		output:    output,
	}, nil

}

func (j *Job) Snapshot() JobSnapshot {
	j.mu.RLock()
	defer j.mu.RUnlock()

	return JobSnapshot{
		ID:        j.id,
		Owner:     j.owner,
		Argv:      append([]string(nil), j.argv...),
		StartedAt: j.startedAt,
		State:     j.state,
	}
}

func (j *Job) transition(next State) error {

	j.mu.Lock()
	defer j.mu.Unlock()

	switch {
	case j.state == StateRunning:
		if next == StateStopping || next == StateExited || next == StateFailed {
			j.state = next
			return nil
		} else {
			return ErrInvalidStateTransition
		}
	case j.state == StateStopping:
		if next == StateStopped || next == StateFailed {
			j.state = next
			return nil

		}
	}
	return ErrInvalidStateTransition
}
