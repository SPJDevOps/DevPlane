package workspace

import (
	"context"
	"errors"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// Remediation snippets for status.remediationHint (no secrets, stable for operators).
const (
	RemediationRBAC       = "Confirm the operator ServiceAccount has RBAC to manage ServiceAccounts, Roles, and RoleBindings in this namespace (see DevPlane operator ClusterRole/RoleBinding)."
	RemediationNetPol     = "Confirm the operator can create and update NetworkPolicies in this namespace."
	RemediationPVCGet     = "Check API server connectivity. If errors mention timeout, investigate apiserver load and admission webhook latency."
	RemediationPVCCreate  = "Confirm the operator can create PersistentVolumeClaims in this namespace and that spec.persistence.storageClass exists."
	RemediationPVCLost    = "PVC entered Lost — check storage backend, reclaim policy, and underlying volume health; you may need to delete the PVC and recreate the Workspace."
	RemediationPodGet     = "Check API server connectivity and that the operator can read Pods in this namespace."
	RemediationPodCreate  = "Confirm the operator can create Pods. If an admission webhook is mentioned, review that webhook's logs and failurePolicy."
	RemediationService    = "Confirm the operator can create Services and that the Workspace namespace allows ClusterIP=None headless services."
	RemediationImagePull  = "Verify WORKSPACE_IMAGE (or the image in the pod spec) exists, is pullable from nodes, and registry credentials are configured if the registry is private."
	RemediationCrashLoop  = "Inspect pod logs and previous container logs; fix startup command, config, or resource limits in the workspace image or Workspace spec."
	RemediationPodFailed  = "Inspect pod status and logs; adjust resource limits or fix the workload."
	RemediationPodUnknown = "Check node and kubelet health; Unknown often means the node is unreachable or the kubelet stopped reporting."
	RemediationValidation = "Fix the Workspace spec fields shown in status.message and re-apply the manifest."
	RemediationForbidden  = "Kubernetes returned Forbidden — grant the operator RBAC required for the resource in this namespace."
	RemediationTimeout    = "Request timed out — check apiserver connectivity, etcd health, and cluster load."
	RemediationWebhook    = "An admission webhook rejected or blocked the request — inspect validating/mutating webhook configuration and webhook pod logs."
	RemediationAPIError   = "See status.message for the Kubernetes API error details."

	// Condition / event reason codes for the Ready condition and Kubernetes events.
	ReasonRunning               = "Running"
	ReasonProgressing           = "Progressing"
	ReasonStopped               = "Stopped"
	ReasonFailed                = "Failed"
	ReasonValidationFailed      = "ValidationFailed"
	ReasonRBACReconcileFailed   = "RBACReconcileFailed"
	ReasonNetPolReconcileFailed = "NetworkPolicyReconcileFailed"
	ReasonPVCReadFailed         = "PVCReadFailed"
	ReasonPVCCreateFailed       = "PVCCreateFailed"
	ReasonPVCLost               = "PVCLost"
	ReasonPodReadFailed         = "PodReadFailed"
	ReasonPodCreateFailed       = "PodCreateFailed"
	ReasonServiceFailed         = "ServiceEnsureFailed"
	ReasonPodFailed             = "PodFailed"
	ReasonPodUnknown            = "PodUnknown"
	ReasonImagePullBackOff      = "ImagePullBackOff"
	ReasonErrImagePull          = "ErrImagePull"
	ReasonInvalidImageName      = "InvalidImageName"
	ReasonCrashLoopBackOff      = "CrashLoopBackOff"
	ReasonForbidden             = "Forbidden"
	ReasonTimeout               = "Timeout"
	ReasonAdmissionWebhook      = "AdmissionWebhook"
	ReasonAPIError              = "APIError"
)

// ErrorDetailsForService classifies errors when ensuring the headless Service.
func ErrorDetailsForService(err error) (remediation string, readyReason string) {
	rem, r := ClassifyAPIError(err)
	if r != ReasonAPIError {
		return rem, r
	}
	return RemediationService, ReasonServiceFailed
}

// ErrorDetailsForRBAC prefers Kubernetes API classification, otherwise RBAC-specific defaults.
func ErrorDetailsForRBAC(err error) (remediation string, readyReason string) {
	rem, r := ClassifyAPIError(err)
	if r != ReasonAPIError {
		return rem, r
	}
	return RemediationRBAC, ReasonRBACReconcileFailed
}

// ErrorDetailsForNetworkPolicy prefers Kubernetes API classification, otherwise NetworkPolicy defaults.
func ErrorDetailsForNetworkPolicy(err error) (remediation string, readyReason string) {
	rem, r := ClassifyAPIError(err)
	if r != ReasonAPIError {
		return rem, r
	}
	return RemediationNetPol, ReasonNetPolReconcileFailed
}

// ErrorDetailsForPVCGet classifies read errors on PersistentVolumeClaim.
func ErrorDetailsForPVCGet(err error) (remediation string, readyReason string) {
	rem, r := ClassifyAPIError(err)
	if r != ReasonAPIError {
		return rem, r
	}
	return RemediationPVCGet, ReasonPVCReadFailed
}

// ErrorDetailsForPVCCreate classifies create errors on PersistentVolumeClaim.
func ErrorDetailsForPVCCreate(err error) (remediation string, readyReason string) {
	rem, r := ClassifyAPIError(err)
	if r != ReasonAPIError {
		return rem, r
	}
	return RemediationPVCCreate, ReasonPVCCreateFailed
}

// ErrorDetailsForPodGet classifies read errors on Pod.
func ErrorDetailsForPodGet(err error) (remediation string, readyReason string) {
	rem, r := ClassifyAPIError(err)
	if r != ReasonAPIError {
		return rem, r
	}
	return RemediationPodGet, ReasonPodReadFailed
}

// ErrorDetailsForPodCreate classifies create errors on Pod.
func ErrorDetailsForPodCreate(err error) (remediation string, readyReason string) {
	rem, r := ClassifyAPIError(err)
	if r != ReasonAPIError {
		return rem, r
	}
	return RemediationPodCreate, ReasonPodCreateFailed
}

// ClassifyAPIError maps common client errors to a stable Ready reason and remediation hint.
func ClassifyAPIError(err error) (remediation string, reason string) {
	if err == nil {
		return "", ReasonAPIError
	}
	if apierrors.IsForbidden(err) {
		return RemediationForbidden, ReasonForbidden
	}
	if apierrors.IsTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
		return RemediationTimeout, ReasonTimeout
	}
	if apierrors.IsServiceUnavailable(err) {
		return RemediationTimeout, ReasonTimeout
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "webhook") || strings.Contains(msg, "admission") {
		return RemediationWebhook, ReasonAdmissionWebhook
	}
	return RemediationAPIError, ReasonAPIError
}

// RemediationForPodWaitingReason returns a hint and Ready reason for a container waiting reason.
func RemediationForPodWaitingReason(waitingReason string) (remediation string, reason string) {
	switch waitingReason {
	case "ImagePullBackOff":
		return RemediationImagePull, ReasonImagePullBackOff
	case "ErrImagePull":
		return RemediationImagePull, ReasonErrImagePull
	case "InvalidImageName":
		return RemediationImagePull, ReasonInvalidImageName
	case "CrashLoopBackOff":
		return RemediationCrashLoop, ReasonCrashLoopBackOff
	default:
		if waitingReason != "" {
			return RemediationPodFailed, waitingReason
		}
		return RemediationPodFailed, ReasonFailed
	}
}
