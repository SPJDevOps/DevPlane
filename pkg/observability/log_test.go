package observability

import "testing"

func TestPhaseLabel(t *testing.T) {
	if got, want := PhaseLabel(""), "None"; got != want {
		t.Errorf("PhaseLabel(\"\") = %q, want %q", got, want)
	}
	if got, want := PhaseLabel("Running"), "Running"; got != want {
		t.Errorf("PhaseLabel(Running) = %q, want %q", got, want)
	}
}
