package gateway

import "testing"

func TestEnsureAction(t *testing.T) {
	if got, want := EnsureAction(EnsureDetails{Created: true}), "create"; got != want {
		t.Errorf("EnsureAction(created) = %q, want %q", got, want)
	}
	if got, want := EnsureAction(EnsureDetails{RestartedFromStopped: true}), "restart"; got != want {
		t.Errorf("EnsureAction(restart) = %q, want %q", got, want)
	}
	if got, want := EnsureAction(EnsureDetails{}), "get"; got != want {
		t.Errorf("EnsureAction(empty) = %q, want %q", got, want)
	}
}
