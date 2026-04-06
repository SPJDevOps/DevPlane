// Package observability registers DevPlane-specific metrics and log helpers for
// the workspace operator and related components.
package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

const (
	// Log keys — use consistently so log aggregators can index incidents.
	LogKeyComponent = "devplane.component"
	LogKeyEvent     = "devplane.event"
	LogKeyWorkspace = "workspace"
	LogKeyNamespace = "namespace"
	LogKeyUserID    = "userId"
	LogKeyFromPhase = "fromPhase"
	LogKeyToPhase   = "toPhase"
)

// Operator component value for structured logs.
const ComponentWorkspaceController = "workspace-controller"

// Event names for structured logs (stable identifiers).
const EventPhaseTransition = "workspace.phase.transition"

var (
	// WorkspacePhaseTransitions counts successful status patches where Phase changed.
	WorkspacePhaseTransitions = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "devplane",
			Subsystem: "workspace",
			Name:      "phase_transitions_total",
			Help:      "Workspace CR status.phase changes after a successful status patch.",
		},
		[]string{"from_phase", "to_phase"},
	)

	// WorkspaceStatusPatchFailures counts failed Status().Patch operations.
	WorkspaceStatusPatchFailures = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: "devplane",
			Subsystem: "workspace",
			Name:      "status_patch_failures_total",
			Help:      "Failed patches to Workspace status subresource.",
		},
	)
)

func init() {
	crmetrics.Registry.MustRegister(WorkspacePhaseTransitions, WorkspaceStatusPatchFailures)
}

// PhaseLabel normalizes an empty phase for Prometheus label values.
func PhaseLabel(p string) string {
	if p == "" {
		return "None"
	}
	return p
}
