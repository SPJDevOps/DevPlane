// Package controllers contains the Workspace reconciler.
package controllers

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	workspacev1alpha1 "workspace-operator/api/v1alpha1"
	"workspace-operator/pkg/workspace"
)

// WorkspaceReconciler reconciles a Workspace object.
type WorkspaceReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	WorkspaceImage string
}

//+kubebuilder:rbac:groups=workspace.devplane.io,resources=workspaces,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=workspace.devplane.io,resources=workspaces/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=workspace.devplane.io,resources=workspaces/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=pods;persistentvolumeclaims;services;serviceaccounts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings;roles,verbs=get;list;watch;create;update;patch;delete

// Reconcile moves the current state of the cluster closer to the desired state.
func (r *WorkspaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var ws workspacev1alpha1.Workspace
	if err := r.Get(ctx, req.NamespacedName, &ws); err != nil {
		log.Error(err, "Unable to fetch Workspace")
		return ctrl.Result{}, client.IgnoreNotFound(err)
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

	// Wait for PVC to be bound
	if pvc.Status.Phase != corev1.ClaimBound {
		if updateErr := r.updateStatus(ctx, &ws, "Creating", "", "", "", "Waiting for PVC to bind"); updateErr != nil {
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
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

	// Pod exists but not running/ready
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
		Complete(r)
}
