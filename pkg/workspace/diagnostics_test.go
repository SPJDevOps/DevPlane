package workspace

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestClassifyAPIError_Forbidden(t *testing.T) {
	err := apierrors.NewForbidden(schema.GroupResource{Group: "", Resource: "pods"}, "x", errors.New("no"))
	rem, reason := ClassifyAPIError(err)
	if reason != ReasonForbidden {
		t.Fatalf("reason = %q, want %s", reason, ReasonForbidden)
	}
	if rem != RemediationForbidden {
		t.Fatalf("remediation = %q", rem)
	}
}

func TestClassifyAPIError_Timeout(t *testing.T) {
	err := apierrors.NewTimeoutError("slow", 1)
	rem, reason := ClassifyAPIError(err)
	if reason != ReasonTimeout {
		t.Fatalf("reason = %q, want %s", reason, ReasonTimeout)
	}
	if rem != RemediationTimeout {
		t.Fatalf("remediation = %q", rem)
	}
}

func TestClassifyAPIError_DeadlineExceeded(t *testing.T) {
	rem, reason := ClassifyAPIError(context.DeadlineExceeded)
	if reason != ReasonTimeout {
		t.Fatalf("reason = %q, want %s", reason, ReasonTimeout)
	}
	if rem != RemediationTimeout {
		t.Fatalf("remediation = %q", rem)
	}
}

func TestClassifyAPIError_WebhookMessage(t *testing.T) {
	err := errors.New("Internal error occurred: failed calling admission webhook \"pod-policy.example.com\"")
	rem, reason := ClassifyAPIError(err)
	if reason != ReasonAdmissionWebhook {
		t.Fatalf("reason = %q, want %s", reason, ReasonAdmissionWebhook)
	}
	if rem != RemediationWebhook {
		t.Fatalf("remediation = %q", rem)
	}
}

func TestErrorDetailsForRBAC_PrefersClassification(t *testing.T) {
	err := apierrors.NewForbidden(schema.GroupResource{Group: "rbac.authorization.k8s.io", Resource: "roles"}, "r", errors.New("no"))
	rem, rr := ErrorDetailsForRBAC(err)
	if rr != ReasonForbidden {
		t.Fatalf("readyReason = %q", rr)
	}
	if rem != RemediationForbidden {
		t.Fatalf("remediation = %q", rem)
	}
}

func TestErrorDetailsForRBAC_Default(t *testing.T) {
	err := errors.New("some other failure")
	rem, rr := ErrorDetailsForRBAC(err)
	if rr != ReasonRBACReconcileFailed {
		t.Fatalf("readyReason = %q, want %s", rr, ReasonRBACReconcileFailed)
	}
	if rem != RemediationRBAC {
		t.Fatalf("remediation = %q", rem)
	}
}

func TestRemediationForPodWaitingReason_ImagePull(t *testing.T) {
	hint, reason := RemediationForPodWaitingReason("ImagePullBackOff")
	if reason != ReasonImagePullBackOff {
		t.Fatalf("reason = %q", reason)
	}
	if hint != RemediationImagePull {
		t.Fatalf("hint = %q", hint)
	}
}

func TestRemediationForPodWaitingReason_UnknownReasonUsesMessage(t *testing.T) {
	hint, reason := RemediationForPodWaitingReason("CreateContainerConfigError")
	if reason != "CreateContainerConfigError" {
		t.Fatalf("reason = %q", reason)
	}
	if hint != RemediationPodFailed {
		t.Fatalf("hint = %q", hint)
	}
}

func TestRemediationForPodWaitingReason_Empty(t *testing.T) {
	hint, reason := RemediationForPodWaitingReason("")
	if reason != ReasonFailed {
		t.Fatalf("reason = %q", reason)
	}
	if hint != RemediationPodFailed {
		t.Fatalf("hint = %q", hint)
	}
}
