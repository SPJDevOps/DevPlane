// Package controllers contains the Workspace reconciler.
package controllers

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	workspacev1alpha1 "workspace-operator/api/v1alpha1"
	"workspace-operator/pkg/security"
	"workspace-operator/pkg/workspace"
)

// WorkspaceReconciler reconciles a Workspace object.
type WorkspaceReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	WorkspaceImage string
	LLMNamespaces  []string
	// EgressPorts is the operator-level default list of TCP ports allowed for
	// egress to external IPs (0.0.0.0/0).  Individual Workspace CRs may override
	// this via spec.aiConfig.egressPorts.  When empty, security.DefaultEgressPorts
	// is used.
	EgressPorts []int32
}

//+kubebuilder:rbac:groups=workspace.devplane.io,resources=workspaces,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=workspace.devplane.io,resources=workspaces/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=workspace.devplane.io,resources=workspaces/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=pods;persistentvolumeclaims;services;serviceaccounts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings;roles,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete

// Reconcile moves the current state of the cluster closer to the desired state.
func (r *WorkspaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var ws workspacev1alpha1.Workspace
	if err := r.Get(ctx, req.NamespacedName, &ws); err != nil {
		log.Error(err, "Unable to fetch Workspace")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle stopped workspaces — do not reconcile further.
	if ws.Status.Phase == "Stopped" {
		return ctrl.Result{}, nil
	}

	if err := workspace.ValidateSpec(&ws); err != nil {
		log.Error(err, "Invalid Workspace spec")
		if updateErr := r.updateStatus(ctx, &ws, "Failed", "", "", "", err.Error()); updateErr != nil {
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{}, nil
	}

	userID := ws.Spec.User.ID
	pvcName := workspace.PVCName(userID)
	podName := workspace.PodName(userID)
	svcName := workspace.ServiceName(userID)
	nn := req.NamespacedName

	// Ensure ServiceAccount
	saName := workspace.ServiceAccountName(userID)
	var sa corev1.ServiceAccount
	if err := r.Get(ctx, client.ObjectKey{Namespace: nn.Namespace, Name: saName}, &sa); err != nil {
		if !errors.IsNotFound(err) {
			log.Error(err, "Failed to get ServiceAccount")
			return ctrl.Result{}, err
		}
		saObj, buildErr := security.BuildServiceAccount(&ws, r.Scheme)
		if buildErr != nil {
			log.Error(buildErr, "Failed to build ServiceAccount")
			return ctrl.Result{}, buildErr
		}
		if err := r.Create(ctx, saObj); err != nil {
			log.Error(err, "Failed to create ServiceAccount")
			return ctrl.Result{}, err
		}
		log.Info("Created ServiceAccount", "serviceAccount", saName)
	}

	// Ensure Role
	var role rbacv1.Role
	if err := r.Get(ctx, client.ObjectKey{Namespace: nn.Namespace, Name: saName}, &role); err != nil {
		if !errors.IsNotFound(err) {
			log.Error(err, "Failed to get Role")
			return ctrl.Result{}, err
		}
		roleObj, buildErr := security.BuildRole(&ws, r.Scheme)
		if buildErr != nil {
			log.Error(buildErr, "Failed to build Role")
			return ctrl.Result{}, buildErr
		}
		if err := r.Create(ctx, roleObj); err != nil {
			log.Error(err, "Failed to create Role")
			return ctrl.Result{}, err
		}
		log.Info("Created Role", "role", saName)
	}

	// Ensure RoleBinding
	var rb rbacv1.RoleBinding
	if err := r.Get(ctx, client.ObjectKey{Namespace: nn.Namespace, Name: saName}, &rb); err != nil {
		if !errors.IsNotFound(err) {
			log.Error(err, "Failed to get RoleBinding")
			return ctrl.Result{}, err
		}
		rbObj, buildErr := security.BuildRoleBinding(&ws, r.Scheme)
		if buildErr != nil {
			log.Error(buildErr, "Failed to build RoleBinding")
			return ctrl.Result{}, buildErr
		}
		if err := r.Create(ctx, rbObj); err != nil {
			log.Error(err, "Failed to create RoleBinding")
			return ctrl.Result{}, err
		}
		log.Info("Created RoleBinding", "roleBinding", saName)
	}

	// Ensure deny-all NetworkPolicy
	denyAllName := fmt.Sprintf("%s-workspace-deny-all", userID)
	var npDenyAll networkingv1.NetworkPolicy
	if err := r.Get(ctx, client.ObjectKey{Namespace: nn.Namespace, Name: denyAllName}, &npDenyAll); err != nil {
		if !errors.IsNotFound(err) {
			log.Error(err, "Failed to get deny-all NetworkPolicy")
			return ctrl.Result{}, err
		}
		npObj, buildErr := security.BuildDenyAllNetworkPolicy(&ws, r.Scheme)
		if buildErr != nil {
			log.Error(buildErr, "Failed to build deny-all NetworkPolicy")
			return ctrl.Result{}, buildErr
		}
		if err := r.Create(ctx, npObj); err != nil {
			log.Error(err, "Failed to create deny-all NetworkPolicy")
			return ctrl.Result{}, err
		}
		log.Info("Created deny-all NetworkPolicy", "networkPolicy", denyAllName)
	}

	// Ensure egress NetworkPolicy — use CreateOrUpdate so that changes to
	// egressPorts or egressNamespaces (from spec or operator env) are applied to
	// existing workspaces on the next reconcile without requiring deletion.
	egressName := fmt.Sprintf("%s-workspace-egress", userID)

	llmNamespaces := ws.Spec.AIConfig.EgressNamespaces
	if len(llmNamespaces) == 0 {
		llmNamespaces = r.LLMNamespaces
	}
	if len(llmNamespaces) == 0 {
		llmNamespaces = []string{"ai-system"}
	}

	egressPorts := ws.Spec.AIConfig.EgressPorts
	if len(egressPorts) == 0 {
		egressPorts = r.EgressPorts
	}
	if len(egressPorts) == 0 {
		egressPorts = security.DefaultEgressPorts
	}

	desiredEgress, buildErr := security.BuildEgressNetworkPolicy(&ws, llmNamespaces, egressPorts, r.Scheme)
	if buildErr != nil {
		log.Error(buildErr, "Failed to build egress NetworkPolicy")
		return ctrl.Result{}, buildErr
	}

	var npEgress networkingv1.NetworkPolicy
	if err := r.Get(ctx, client.ObjectKey{Namespace: nn.Namespace, Name: egressName}, &npEgress); err != nil {
		if !errors.IsNotFound(err) {
			log.Error(err, "Failed to get egress NetworkPolicy")
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, desiredEgress); err != nil {
			log.Error(err, "Failed to create egress NetworkPolicy")
			return ctrl.Result{}, err
		}
		log.Info("Created egress NetworkPolicy", "networkPolicy", egressName)
	} else {
		// Update egress rules in place so port / namespace changes take effect.
		npEgress.Spec.Egress = desiredEgress.Spec.Egress
		if err := r.Update(ctx, &npEgress); err != nil {
			log.Error(err, "Failed to update egress NetworkPolicy")
			return ctrl.Result{}, err
		}
		log.Info("Reconciled egress NetworkPolicy", "networkPolicy", egressName)
	}

	// Ensure ingress-gateway NetworkPolicy
	ingressGwName := fmt.Sprintf("%s-workspace-ingress-gateway", userID)
	var npIngressGw networkingv1.NetworkPolicy
	if err := r.Get(ctx, client.ObjectKey{Namespace: nn.Namespace, Name: ingressGwName}, &npIngressGw); err != nil {
		if !errors.IsNotFound(err) {
			log.Error(err, "Failed to get ingress-gateway NetworkPolicy")
			return ctrl.Result{}, err
		}
		npObj, buildErr := security.BuildIngressFromGatewayNetworkPolicy(&ws, r.Scheme)
		if buildErr != nil {
			log.Error(buildErr, "Failed to build ingress-gateway NetworkPolicy")
			return ctrl.Result{}, buildErr
		}
		if err := r.Create(ctx, npObj); err != nil {
			log.Error(err, "Failed to create ingress-gateway NetworkPolicy")
			return ctrl.Result{}, err
		}
		log.Info("Created ingress-gateway NetworkPolicy", "networkPolicy", ingressGwName)
	}

	// Ensure PVC
	var pvc corev1.PersistentVolumeClaim
	if err := r.Get(ctx, client.ObjectKey{Namespace: nn.Namespace, Name: pvcName}, &pvc); err != nil {
		if !errors.IsNotFound(err) {
			log.Error(err, "Failed to get PVC")
			return ctrl.Result{}, err
		}
		pvcObj, buildErr := workspace.BuildPVC(&ws, r.Scheme)
		if buildErr != nil {
			log.Error(buildErr, "Failed to build PVC")
			if updateErr := r.updateStatus(ctx, &ws, "Failed", "", "", "", buildErr.Error()); updateErr != nil {
				return ctrl.Result{}, updateErr
			}
			return ctrl.Result{}, nil
		}
		if err := r.Create(ctx, pvcObj); err != nil {
			log.Error(err, "Failed to create PVC")
			if updateErr := r.updateStatus(ctx, &ws, "Failed", "", "", "", err.Error()); updateErr != nil {
				return ctrl.Result{}, updateErr
			}
			return ctrl.Result{}, nil
		}
		log.Info("Created PVC", "pvc", pvcName)
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// Only block on a permanently lost PVC — a Pending PVC with WaitForFirstConsumer
	// binding mode will not bind until a pod consuming it is scheduled, so we must
	// proceed to pod creation and let Kubernetes resolve the binding.
	if pvc.Status.Phase == corev1.ClaimLost {
		msg := "PVC lost — manual intervention required"
		if updateErr := r.updateStatus(ctx, &ws, "Failed", "", "", "", msg); updateErr != nil {
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{}, nil
	}

	// Ensure Pod
	var pod corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{Namespace: nn.Namespace, Name: podName}, &pod); err != nil {
		if !errors.IsNotFound(err) {
			log.Error(err, "Failed to get Pod")
			return ctrl.Result{}, err
		}
		image := r.WorkspaceImage
		if image == "" {
			image = "workspace:latest"
		}
		podObj, buildErr := workspace.BuildPod(&ws, pvcName, image, r.Scheme)
		if buildErr != nil {
			log.Error(buildErr, "Failed to build Pod")
			if updateErr := r.updateStatus(ctx, &ws, "Failed", "", "", "", buildErr.Error()); updateErr != nil {
				return ctrl.Result{}, updateErr
			}
			return ctrl.Result{}, nil
		}
		if err := r.Create(ctx, podObj); err != nil {
			log.Error(err, "Failed to create Pod")
			if updateErr := r.updateStatus(ctx, &ws, "Failed", "", "", "", err.Error()); updateErr != nil {
				return ctrl.Result{}, updateErr
			}
			return ctrl.Result{}, nil
		}
		log.Info("Created Pod", "pod", podName)
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// Ensure headless Service
	var svc corev1.Service
	if err := r.Get(ctx, client.ObjectKey{Namespace: nn.Namespace, Name: svcName}, &svc); err != nil {
		if !errors.IsNotFound(err) {
			log.Error(err, "Failed to get Service")
			return ctrl.Result{}, err
		}
		svcObj, buildErr := workspace.BuildHeadlessService(&ws, r.Scheme)
		if buildErr != nil {
			log.Error(buildErr, "Failed to build Service")
			if updateErr := r.updateStatus(ctx, &ws, "Failed", "", "", "", buildErr.Error()); updateErr != nil {
				return ctrl.Result{}, updateErr
			}
			return ctrl.Result{}, nil
		}
		if err := r.Create(ctx, svcObj); err != nil {
			log.Error(err, "Failed to create Service")
			if updateErr := r.updateStatus(ctx, &ws, "Failed", "", "", "", err.Error()); updateErr != nil {
				return ctrl.Result{}, updateErr
			}
			return ctrl.Result{}, nil
		}
		log.Info("Created Service", "service", svcName)
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// Update status from pod state
	serviceEndpoint := fmt.Sprintf("%s.%s.svc.cluster.local", svcName, nn.Namespace)
	if pod.Status.Phase == corev1.PodRunning && isPodReady(&pod) {
		if updateErr := r.updateStatus(ctx, &ws, "Running", podName, serviceEndpoint, "", ""); updateErr != nil {
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{}, nil
	}

	// Check for pod failure conditions
	if pod.Status.Phase == corev1.PodFailed {
		msg := fmt.Sprintf("Pod failed: %s", pod.Status.Reason)
		if updateErr := r.updateStatus(ctx, &ws, "Failed", podName, "", "", msg); updateErr != nil {
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{}, nil
	}

	// Check container waiting reasons for failure states
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil {
			reason := cs.State.Waiting.Reason
			if reason == "CrashLoopBackOff" || reason == "ImagePullBackOff" || reason == "ErrImagePull" || reason == "InvalidImageName" {
				msg := fmt.Sprintf("Pod stuck: %s — %s", reason, cs.State.Waiting.Message)
				if updateErr := r.updateStatus(ctx, &ws, "Failed", podName, "", "", msg); updateErr != nil {
					return ctrl.Result{}, updateErr
				}
				return ctrl.Result{}, nil
			}
		}
	}

	// Pod exists but not running/ready — still creating
	msg := "Pod starting"
	if pod.Status.Phase != "" {
		msg = fmt.Sprintf("Pod phase: %s", pod.Status.Phase)
	}
	if updateErr := r.updateStatus(ctx, &ws, "Creating", podName, serviceEndpoint, msg, ""); updateErr != nil {
		return ctrl.Result{}, updateErr
	}
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// updateStatus sets the Workspace status and updates via the status subresource.
func (r *WorkspaceReconciler) updateStatus(ctx context.Context, ws *workspacev1alpha1.Workspace, phase, podName, serviceEndpoint, message, messageOverride string) error {
	msg := message
	if messageOverride != "" {
		msg = messageOverride
	}
	ws.Status.Phase = phase
	ws.Status.PodName = podName
	ws.Status.ServiceEndpoint = serviceEndpoint
	ws.Status.Message = msg
	return r.Status().Update(ctx, ws)
}

// isPodReady returns true if the pod has a Ready condition that is true.
func isPodReady(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkspaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&workspacev1alpha1.Workspace{}).
		Owns(&corev1.Pod{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Complete(r)
}
