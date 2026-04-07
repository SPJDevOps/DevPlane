package observability

import (
	"github.com/go-logr/logr"

	workspacev1alpha1 "workspace-operator/api/v1alpha1"
)

// LogWorkspacePhaseTransition emits a structured log line when status.phase changes.
func LogWorkspacePhaseTransition(log logr.Logger, ws *workspacev1alpha1.Workspace, from, to workspacev1alpha1.WorkspacePhase, podName, serviceEndpoint, message string) {
	if ws == nil {
		return
	}
	uid := ""
	if ws.Spec.User.ID != "" {
		uid = ws.Spec.User.ID
	}
	log.Info("workspace phase transition",
		LogKeyAuditSchema, AuditSchemaVersion,
		LogKeyComponent, ComponentWorkspaceController,
		LogKeyEvent, EventPhaseTransition,
		LogKeyWorkspace, ws.Name,
		LogKeyNamespace, ws.Namespace,
		LogKeyUserID, uid,
		LogKeyFromPhase, string(from),
		LogKeyToPhase, string(to),
		"pod", podName,
		"serviceEndpoint", serviceEndpoint,
		"message", message,
	)
}
