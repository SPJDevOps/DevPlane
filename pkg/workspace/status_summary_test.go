package workspace

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	workspacev1alpha1 "workspace-operator/api/v1alpha1"
)

func TestApplyStatusSummary_ReadyConditionRunning(t *testing.T) {
	ws := &workspacev1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Generation: 3},
	}
	ApplyStatusSummary(ws, StatusSummary{
		Phase:           workspacev1alpha1.WorkspacePhaseRunning,
		PodName:         "u-workspace-pod",
		ServiceEndpoint: "u-workspace.default.svc.cluster.local",
		ReadyReason:     ReasonRunning,
	})
	cond := meta.FindStatusCondition(ws.Status.Conditions, ConditionTypeReady)
	if cond == nil {
		t.Fatal("expected Ready condition")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Fatalf("Ready status = %q, want True", cond.Status)
	}
	if cond.Reason != ReasonRunning {
		t.Fatalf("Ready reason = %q", cond.Reason)
	}
	if cond.ObservedGeneration != 3 {
		t.Fatalf("ObservedGeneration = %d", cond.ObservedGeneration)
	}
}

func TestApplyStatusSummary_RemediationFailed(t *testing.T) {
	ws := &workspacev1alpha1.Workspace{}
	ApplyStatusSummary(ws, StatusSummary{
		Phase:           workspacev1alpha1.WorkspacePhaseFailed,
		MessageOverride: "Pod stuck: ErrImagePull — no such host",
		RemediationHint: RemediationImagePull,
		ReadyReason:     ReasonErrImagePull,
	})
	if ws.Status.RemediationHint != RemediationImagePull {
		t.Fatalf("remediationHint = %q", ws.Status.RemediationHint)
	}
	cond := meta.FindStatusCondition(ws.Status.Conditions, ConditionTypeReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != ReasonErrImagePull {
		t.Fatalf("unexpected Ready condition: %#v", cond)
	}
}
