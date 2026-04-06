package workspace

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	workspacev1alpha1 "workspace-operator/api/v1alpha1"
)

// ConditionTypeReady is the Workspace status condition that mirrors readiness for use.
const ConditionTypeReady = "Ready"

// StatusSummary carries observed state for a single status patch.
type StatusSummary struct {
	Phase           workspacev1alpha1.WorkspacePhase
	PodName         string
	ServiceEndpoint string
	Message         string
	MessageOverride string
	RemediationHint string
	// ReadyReason is the metav1.Condition Reason for the Ready condition (e.g. ImagePullBackOff).
	// If empty, a default is chosen from Phase.
	ReadyReason string
}

// ApplyStatusSummary writes summary fields onto ws.Status (including Ready condition).
func ApplyStatusSummary(ws *workspacev1alpha1.Workspace, sum StatusSummary) {
	msg := sum.Message
	if sum.MessageOverride != "" {
		msg = sum.MessageOverride
	}
	ws.Status.Phase = sum.Phase
	ws.Status.PodName = sum.PodName
	ws.Status.ServiceEndpoint = sum.ServiceEndpoint
	ws.Status.Message = msg
	ws.Status.RemediationHint = sum.RemediationHint
	syncReadyCondition(ws, sum, msg)
}

func syncReadyCondition(ws *workspacev1alpha1.Workspace, sum StatusSummary, message string) {
	reason := sum.ReadyReason
	if reason == "" {
		reason = defaultReadyReason(sum.Phase)
	}
	ready := metav1.ConditionFalse
	if sum.Phase == workspacev1alpha1.WorkspacePhaseRunning {
		ready = metav1.ConditionTrue
	}
	condMsg := message
	if condMsg == "" && sum.Phase == workspacev1alpha1.WorkspacePhaseRunning {
		condMsg = "Workspace pod is ready."
	}
	meta.SetStatusCondition(&ws.Status.Conditions, metav1.Condition{
		Type:               ConditionTypeReady,
		Status:             ready,
		ObservedGeneration: ws.Generation,
		Reason:             reason,
		Message:            condMsg,
	})
}

func defaultReadyReason(phase workspacev1alpha1.WorkspacePhase) string {
	switch phase {
	case workspacev1alpha1.WorkspacePhaseRunning:
		return ReasonRunning
	case workspacev1alpha1.WorkspacePhaseCreating:
		return ReasonProgressing
	case workspacev1alpha1.WorkspacePhaseFailed:
		return ReasonFailed
	case workspacev1alpha1.WorkspacePhaseStopped:
		return ReasonStopped
	default:
		return ReasonProgressing
	}
}
