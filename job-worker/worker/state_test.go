package worker

import (
	"testing"
)

func TestStateTerminal(t *testing.T) {
	test := []struct {
		name     string
		state    State
		terminal bool
	}{
		{name: "running", state: StateRunning, terminal: false},
		{name: "stopping", state: StateStopping, terminal: false},
		{name: "exited", state: StateExited, terminal: true},
		{name: "stopped", state: StateStopped, terminal: true},
		{name: "failed", state: StateFailed, terminal: true},
	}

	for _, test := range test {
		t.Run(test.name, func(t *testing.T) {
			if got := test.state.Terminal(); got != test.terminal {
				t.Fatalf("Terminal() = %v, want %v", got, test.terminal)
			}
		})
	}
}
