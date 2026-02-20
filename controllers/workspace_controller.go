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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	workspacev1alpha1 "workspace-operator/api/v1alpha1"
	"workspace-operator/pkg/security"
	"workspace-operator/pkg/workspace"
)

// workspaceFinalizer is registered on every Workspace CR so that the operator
// can perform cleanup before the object is removed from the API server.
const workspaceFinalizer = "workspace.devplane.io/finalizer"

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
	// IdleTimeout is how long a Running workspace may be idle (LastAccessed not
	// updated) before its pod is deleted and the workspace is set to Stopped.
	// Zero disables the idle check.
	IdleTimeout time.Duration
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

	// Handle deletion before all other processing.
	if !ws.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &ws)
	}

	if err := workspace.ValidateSpec(&ws); err != nil {
		log.Error(err, "Invalid Workspace spec")
		if updateErr := r.updateStatus(ctx, &ws, workspacev1alpha1.WorkspacePhaseFailed, "", "", "", err.Error()); updateErr != nil {
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{}, nil
	}

	// Ensure the finalizer is registered so we can handle deletion gracefully.
	if !controllerutil.ContainsFinalizer(&ws, workspaceFinalizer) {
		controllerutil.AddFinalizer(&ws, workspaceFinalizer)
		if err := r.Update(ctx, &ws); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Handle stopped workspaces — do not reconcile further.
	if ws.Status.Phase == workspacev1alpha1.WorkspacePhaseStopped {
		return ctrl.Result{}, nil
	}

	userID := ws.Spec.User.ID
	pvcName := workspace.PVCName(userID)
	podName := workspace.PodName(userID)
	svcName := workspace.ServiceName(userID)
	nn := req.NamespacedName

	// Ensure RBAC resources (ServiceAccount, Role, RoleBinding).
	if err := r.ensureRBAC(ctx, &ws); err != nil {
		log.Error(err, "Failed to ensure RBAC resources")
		return ctrl.Result{}, err
	}

	// Ensure NetworkPolicies (deny-all, egress, ingress-from-gateway).
	if err := r.ensureNetworkPolicies(ctx, &ws); err != nil {
		log.Error(err, "Failed to ensure NetworkPolicies")
		return ctrl.Result{}, err
	}

	// Ensure PVC — only create; Kubernetes does not support shrinking PVC storage.
	var pvc corev1.PersistentVolumeClaim
	if err := r.Get(ctx, client.ObjectKey{Namespace: nn.Namespace, Name: pvcName}, &pvc); err != nil {
		if !errors.IsNotFound(err) {
			log.Error(err, "Failed to get PVC")
			return ctrl.Result{}, err
		}
		pvcObj, buildErr := workspace.BuildPVC(&ws, r.Scheme)
		if buildErr != nil {
			log.Error(buildErr, "Failed to build PVC")
			if updateErr := r.updateStatus(ctx, &ws, workspacev1alpha1.WorkspacePhaseFailed, "", "", "", buildErr.Error()); updateErr != nil {
				return ctrl.Result{}, updateErr
			}
			return ctrl.Result{}, nil
		}
		if err := r.Create(ctx, pvcObj); err != nil {
			log.Error(err, "Failed to create PVC")
			if updateErr := r.updateStatus(ctx, &ws, workspacev1alpha1.WorkspacePhaseFailed, "", "", "", err.Error()); updateErr != nil {
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
		if updateErr := r.updateStatus(ctx, &ws, workspacev1alpha1.WorkspacePhaseFailed, "", "", "", msg); updateErr != nil {
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{}, nil
	}

	// Ensure Pod — create if missing, delete and requeue if image changed.
	image := r.WorkspaceImage
	if image == "" {
		image = "workspace:latest"
	}

	var pod corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{Namespace: nn.Namespace, Name: podName}, &pod); err != nil {
		if !errors.IsNotFound(err) {
			log.Error(err, "Failed to get Pod")
			return ctrl.Result{}, err
		}
		podObj, buildErr := workspace.BuildPod(&ws, pvcName, image, r.Scheme)
		if buildErr != nil {
			log.Error(buildErr, "Failed to build Pod")
			if updateErr := r.updateStatus(ctx, &ws, workspacev1alpha1.WorkspacePhaseFailed, "", "", "", buildErr.Error()); updateErr != nil {
				return ctrl.Result{}, updateErr
			}
			return ctrl.Result{}, nil
		}
		if err := r.Create(ctx, podObj); err != nil {
			log.Error(err, "Failed to create Pod")
			if updateErr := r.updateStatus(ctx, &ws, workspacev1alpha1.WorkspacePhaseFailed, "", "", "", err.Error()); updateErr != nil {
				return ctrl.Result{}, updateErr
			}
			return ctrl.Result{}, nil
		}
		log.Info("Created Pod", "pod", podName)
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// If the pod's container image no longer matches the desired image, delete the
	// pod so the next reconcile recreates it.  Only act when the pod is not already
	// being deleted and has at least one container spec.
	if len(pod.Spec.Containers) > 0 &&
		pod.Spec.Containers[0].Image != image &&
		pod.DeletionTimestamp.IsZero() {
		log.Info("Pod image changed, deleting for recreation",
			"pod", podName,
			"current", pod.Spec.Containers[0].Image,
			"desired", image)
		if err := r.Delete(ctx, &pod); err != nil && !errors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("delete outdated pod: %w", err)
		}
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// Ensure headless Service via CreateOrUpdate so label/port changes are applied.
	svcLabels := workspace.Labels(userID)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: svcName, Namespace: nn.Namespace},
	}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		svc.Labels = svcLabels
		svc.Spec.ClusterIP = corev1.ClusterIPNone
		svc.Spec.Selector = svcLabels
		svc.Spec.Ports = []corev1.ServicePort{
			{Name: "ttyd", Port: 7681, Protocol: corev1.ProtocolTCP},
		}
		return controllerutil.SetControllerReference(&ws, svc, r.Scheme)
	}); err != nil {
		log.Error(err, "Failed to ensure Service")
		if updateErr := r.updateStatus(ctx, &ws, workspacev1alpha1.WorkspacePhaseFailed, "", "", "", err.Error()); updateErr != nil {
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{}, nil
	}

	serviceEndpoint := fmt.Sprintf("%s.%s.svc.cluster.local", svcName, nn.Namespace)

	// Idle-timeout check: stop the workspace if it has been idle longer than IdleTimeout.
	if pod.Status.Phase == corev1.PodRunning && isPodReady(&pod) && r.IdleTimeout > 0 {
		if !ws.Status.LastAccessed.IsZero() &&
			time.Since(ws.Status.LastAccessed.Time) > r.IdleTimeout {
			log.Info("Workspace idle timeout reached, stopping pod",
				"workspace", ws.Name, "idleTimeout", r.IdleTimeout)
			if err := r.Delete(ctx, &pod); err != nil && !errors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("delete idle pod: %w", err)
			}
			if updateErr := r.updateStatus(ctx, &ws, workspacev1alpha1.WorkspacePhaseStopped, "", "", "Workspace stopped due to inactivity", ""); updateErr != nil {
				return ctrl.Result{}, updateErr
			}
			return ctrl.Result{}, nil
		}
	}

	// Update status from pod state.
	if pod.Status.Phase == corev1.PodRunning && isPodReady(&pod) {
		if updateErr := r.updateStatus(ctx, &ws, workspacev1alpha1.WorkspacePhaseRunning, podName, serviceEndpoint, "", ""); updateErr != nil {
			return ctrl.Result{}, updateErr
		}
		// Requeue periodically so the idle-timeout check fires even without events.
		if r.IdleTimeout > 0 {
			return ctrl.Result{RequeueAfter: r.IdleTimeout / 4}, nil
		}
		return ctrl.Result{}, nil
	}

	// Check for pod failure conditions.
	if pod.Status.Phase == corev1.PodFailed {
		msg := fmt.Sprintf("Pod failed: %s", pod.Status.Reason)
		if updateErr := r.updateStatus(ctx, &ws, workspacev1alpha1.WorkspacePhaseFailed, podName, "", "", msg); updateErr != nil {
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{}, nil
	}

	// Check container waiting reasons for failure states.
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil {
			reason := cs.State.Waiting.Reason
			if reason == "CrashLoopBackOff" || reason == "ImagePullBackOff" || reason == "ErrImagePull" || reason == "InvalidImageName" {
				msg := fmt.Sprintf("Pod stuck: %s — %s", reason, cs.State.Waiting.Message)
				if updateErr := r.updateStatus(ctx, &ws, workspacev1alpha1.WorkspacePhaseFailed, podName, "", "", msg); updateErr != nil {
					return ctrl.Result{}, updateErr
				}
				return ctrl.Result{}, nil
			}
		}
	}

	// Pod exists but not running/ready — still creating.
	msg := "Pod starting"
	if pod.Status.Phase != "" {
		msg = fmt.Sprintf("Pod phase: %s", pod.Status.Phase)
	}
	if updateErr := r.updateStatus(ctx, &ws, workspacev1alpha1.WorkspacePhaseCreating, podName, serviceEndpoint, msg, ""); updateErr != nil {
		return ctrl.Result{}, updateErr
	}
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// reconcileDelete removes the finalizer so that Kubernetes garbage collection
// can cascade-delete all owned resources (Pod, PVC, Service, RBAC, NetworkPolicies).
func (r *WorkspaceReconciler) reconcileDelete(ctx context.Context, ws *workspacev1alpha1.Workspace) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.Info("Handling workspace deletion", "workspace", ws.Name)
	controllerutil.RemoveFinalizer(ws, workspaceFinalizer)
	if err := r.Update(ctx, ws); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// ensureRBAC creates or updates the per-user ServiceAccount, Role, and RoleBinding.
func (r *WorkspaceReconciler) ensureRBAC(ctx context.Context, ws *workspacev1alpha1.Workspace) error {
	log := log.FromContext(ctx)
	userID := ws.Spec.User.ID
	saName := workspace.ServiceAccountName(userID)

	rbacLabels := map[string]string{
		"app":        "workspace",
		"user":       userID,
		"managed-by": "devplane",
	}

	// ServiceAccount
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: ws.Namespace},
	}
	if result, err := controllerutil.CreateOrUpdate(ctx, r.Client, sa, func() error {
		sa.Labels = rbacLabels
		return controllerutil.SetControllerReference(ws, sa, r.Scheme)
	}); err != nil {
		return fmt.Errorf("ensure ServiceAccount: %w", err)
	} else if result != controllerutil.OperationResultNone {
		log.Info("ServiceAccount reconciled", "name", saName, "result", result)
	}

	// Role — delegate desired rules to security.BuildRole for a single source of truth.
	desiredRole, err := security.BuildRole(ws, r.Scheme)
	if err != nil {
		return fmt.Errorf("build Role: %w", err)
	}
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: ws.Namespace},
	}
	if result, err := controllerutil.CreateOrUpdate(ctx, r.Client, role, func() error {
		role.Labels = rbacLabels
		role.Rules = desiredRole.Rules
		return controllerutil.SetControllerReference(ws, role, r.Scheme)
	}); err != nil {
		return fmt.Errorf("ensure Role: %w", err)
	} else if result != controllerutil.OperationResultNone {
		log.Info("Role reconciled", "name", saName, "result", result)
	}

	// RoleBinding
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: ws.Namespace},
	}
	if result, err := controllerutil.CreateOrUpdate(ctx, r.Client, rb, func() error {
		rb.Labels = rbacLabels
		rb.Subjects = []rbacv1.Subject{{
			Kind:      rbacv1.ServiceAccountKind,
			Name:      saName,
			Namespace: ws.Namespace,
		}}
		rb.RoleRef = rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     saName,
		}
		return controllerutil.SetControllerReference(ws, rb, r.Scheme)
	}); err != nil {
		return fmt.Errorf("ensure RoleBinding: %w", err)
	} else if result != controllerutil.OperationResultNone {
		log.Info("RoleBinding reconciled", "name", saName, "result", result)
	}

	return nil
}

// ensureNetworkPolicies creates or updates the three NetworkPolicies for a workspace:
// deny-all, egress (dynamic, reacts to spec changes), and ingress-from-gateway.
func (r *WorkspaceReconciler) ensureNetworkPolicies(ctx context.Context, ws *workspacev1alpha1.Workspace) error {
	log := log.FromContext(ctx)

	// Deny-all (static spec — deny all ingress and egress by default).
	denyAll, err := security.BuildDenyAllNetworkPolicy(ws, r.Scheme)
	if err != nil {
		return fmt.Errorf("build deny-all NetworkPolicy: %w", err)
	}
	npDenyAll := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: denyAll.Name, Namespace: ws.Namespace},
	}
	if result, err := controllerutil.CreateOrUpdate(ctx, r.Client, npDenyAll, func() error {
		npDenyAll.Labels = denyAll.Labels
		npDenyAll.Spec = denyAll.Spec
		return controllerutil.SetControllerReference(ws, npDenyAll, r.Scheme)
	}); err != nil {
		return fmt.Errorf("ensure deny-all NetworkPolicy: %w", err)
	} else if result != controllerutil.OperationResultNone {
		log.Info("deny-all NetworkPolicy reconciled", "result", result)
	}

	// Egress (dynamic — reacts to changes in llmNamespaces/egressPorts).
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

	desiredEgress, err := security.BuildEgressNetworkPolicy(ws, llmNamespaces, egressPorts, r.Scheme)
	if err != nil {
		return fmt.Errorf("build egress NetworkPolicy: %w", err)
	}
	npEgress := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: desiredEgress.Name, Namespace: ws.Namespace},
	}
	if result, err := controllerutil.CreateOrUpdate(ctx, r.Client, npEgress, func() error {
		npEgress.Labels = desiredEgress.Labels
		npEgress.Spec = desiredEgress.Spec
		return controllerutil.SetControllerReference(ws, npEgress, r.Scheme)
	}); err != nil {
		return fmt.Errorf("ensure egress NetworkPolicy: %w", err)
	} else if result != controllerutil.OperationResultNone {
		log.Info("egress NetworkPolicy reconciled", "result", result)
	}

	// Ingress-from-gateway (static spec — allow ttyd traffic from gateway pods).
	ingressGw, err := security.BuildIngressFromGatewayNetworkPolicy(ws, r.Scheme)
	if err != nil {
		return fmt.Errorf("build ingress-gateway NetworkPolicy: %w", err)
	}
	npIngressGw := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: ingressGw.Name, Namespace: ws.Namespace},
	}
	if result, err := controllerutil.CreateOrUpdate(ctx, r.Client, npIngressGw, func() error {
		npIngressGw.Labels = ingressGw.Labels
		npIngressGw.Spec = ingressGw.Spec
		return controllerutil.SetControllerReference(ws, npIngressGw, r.Scheme)
	}); err != nil {
		return fmt.Errorf("ensure ingress-gateway NetworkPolicy: %w", err)
	} else if result != controllerutil.OperationResultNone {
		log.Info("ingress-gateway NetworkPolicy reconciled", "result", result)
	}

	return nil
}

// updateStatus sets the Workspace status and updates via the status subresource.
func (r *WorkspaceReconciler) updateStatus(ctx context.Context, ws *workspacev1alpha1.Workspace, phase workspacev1alpha1.WorkspacePhase, podName, serviceEndpoint, message, messageOverride string) error {
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
