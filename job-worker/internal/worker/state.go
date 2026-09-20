package worker

type State string

// running → exited
// running → stopping → stopped succeeds.
// running → failed.
// stopping -> failed
// others are invalid transition
// invalid transition
// running  → stopped     // cannot skip stopping
// exited   → running     // terminal state cannot restart
// stopped  → exited      // terminal state cannot change
// stopping → exited      // Stop was accepted, so terminal result is stopped/failed
const (
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateExited   State = "exited"
	StateStopped  State = "stopped"
	StateFailed   State = "failed"
)

func (s State) Terminal() bool {
	switch s {
	case StateExited, StateStopped, StateFailed:
		return true
	default:
		return false
	}
}
