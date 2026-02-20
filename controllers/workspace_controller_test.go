package controllers

import (
	"context"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	workspacev1alpha1 "workspace-operator/api/v1alpha1"
)

var testScheme = func() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(workspacev1alpha1.AddToScheme(s))
	return s
}()

func TestIsPodReady(t *testing.T) {
	tests := []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{
			name: "nil pod",
			pod:  nil,
			want: false,
		},
		{
			name: "empty conditions",
			pod:  &corev1.Pod{Status: corev1.PodStatus{Conditions: nil}},
			want: false,
		},
		{
			name: "Ready true",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{Type: corev1.PodReady, Status: corev1.ConditionTrue},
					},
				},
			},
			want: true,
		},
		{
			name: "Ready false",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{Type: corev1.PodReady, Status: corev1.ConditionFalse},
					},
				},
			},
			want: false,
		},
		{
			name: "other condition true but not Ready",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
					},
				},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPodReady(tt.pod); got != tt.want {
				t.Errorf("isPodReady() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReconcile_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("Failed to start envtest: %v", err)
	}
	defer func() {
		if err := env.Stop(); err != nil {
			t.Errorf("Failed to stop envtest: %v", err)
		}
	}()

	k8sClient, err := client.New(cfg, client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	ws := &workspacev1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "int-test-ws", Namespace: "default"},
		Spec: workspacev1alpha1.WorkspaceSpec{
			User: workspacev1alpha1.UserInfo{ID: "john", Email: "john@example.com"},
			Resources: workspacev1alpha1.ResourceRequirements{
				CPU: "100m", Memory: "128Mi", Storage: "1Gi",
			},
			AIConfig: workspacev1alpha1.AIConfiguration{
				Providers: []workspacev1alpha1.AIProvider{
					{Name: "local", Endpoint: "http://vllm:8000", Models: []string{"model"}},
				},
			},
			Persistence: workspacev1alpha1.PersistenceConfig{},
		},
	}
	if err := k8sClient.Create(ctx, ws); err != nil {
		t.Fatalf("Failed to create Workspace: %v", err)
	}

	reconciler := &WorkspaceReconciler{
		Client:         k8sClient,
		Scheme:         testScheme,
		WorkspaceImage: "workspace:test",
	}

	nn := types.NamespacedName{Name: ws.Name, Namespace: ws.Namespace}

	// First reconcile: registers the finalizer and returns early (Requeue:true).
	// No owned resources are created yet at this stage.
	_, err = reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: nn})
	if err != nil {
		t.Fatalf("Reconcile (finalizer): %v", err)
	}

	// Second reconcile: should create RBAC, NetworkPolicies, and PVC
	_, err = reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: nn})
	if err != nil {
		t.Fatalf("Reconcile (1): %v", err)
	}

	var pvc corev1.PersistentVolumeClaim
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: "john-workspace-pvc"}, &pvc); err != nil {
		t.Fatalf("Get PVC: %v", err)
	}
	if len(pvc.OwnerReferences) != 1 {
		t.Fatalf("PVC expected 1 owner reference, got %d", len(pvc.OwnerReferences))
	}
	if pvc.OwnerReferences[0].Kind != "Workspace" || pvc.OwnerReferences[0].Name != "int-test-ws" {
		t.Errorf("PVC owner ref: Kind=%s Name=%s", pvc.OwnerReferences[0].Kind, pvc.OwnerReferences[0].Name)
	}

	// Verify security resources were created (SA, Role, RoleBinding, NetworkPolicies)
	var sa corev1.ServiceAccount
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: "john-workspace"}, &sa); err != nil {
		t.Fatalf("Get ServiceAccount: %v", err)
	}
	var role rbacv1.Role
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: "john-workspace"}, &role); err != nil {
		t.Fatalf("Get Role: %v", err)
	}
	var rb rbacv1.RoleBinding
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: "john-workspace"}, &rb); err != nil {
		t.Fatalf("Get RoleBinding: %v", err)
	}
	var npDenyAll networkingv1.NetworkPolicy
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: "john-workspace-deny-all"}, &npDenyAll); err != nil {
		t.Fatalf("Get deny-all NetworkPolicy: %v", err)
	}
	var npEgress networkingv1.NetworkPolicy
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: "john-workspace-egress"}, &npEgress); err != nil {
		t.Fatalf("Get egress NetworkPolicy: %v", err)
	}
	var npIngress networkingv1.NetworkPolicy
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: "john-workspace-ingress-gateway"}, &npIngress); err != nil {
		t.Fatalf("Get ingress-gateway NetworkPolicy: %v", err)
	}

	// Patch PVC to Bound so controller proceeds
	pvc.Status.Phase = corev1.ClaimBound
	if err := k8sClient.Status().Update(ctx, &pvc); err != nil {
		t.Fatalf("Patch PVC status: %v", err)
	}

	// Third reconcile: should create Pod
	_, err = reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: nn})
	if err != nil {
		t.Fatalf("Reconcile (2): %v", err)
	}

	var pod corev1.Pod
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: "john-workspace-pod"}, &pod); err != nil {
		t.Fatalf("Get Pod: %v", err)
	}
	if len(pod.OwnerReferences) != 1 || pod.OwnerReferences[0].Kind != "Workspace" {
		t.Errorf("Pod owner ref: %v", pod.OwnerReferences)
	}

	// Fourth reconcile: should create Service
	_, err = reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: nn})
	if err != nil {
		t.Fatalf("Reconcile (3): %v", err)
	}

	var svc corev1.Service
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: "john-workspace-svc"}, &svc); err != nil {
		t.Fatalf("Get Service: %v", err)
	}
	if svc.Spec.ClusterIP != corev1.ClusterIPNone {
		t.Errorf("Service expected headless, ClusterIP=%s", svc.Spec.ClusterIP)
	}
	if len(svc.OwnerReferences) != 1 || svc.OwnerReferences[0].Kind != "Workspace" {
		t.Errorf("Service owner ref: %v", svc.OwnerReferences)
	}

	// Patch Pod to Running and Ready
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{
		{Type: corev1.PodReady, Status: corev1.ConditionTrue},
	}
	if err := k8sClient.Status().Update(ctx, &pod); err != nil {
		t.Fatalf("Patch Pod status: %v", err)
	}

	// Fifth reconcile: should update Workspace status to Running
	_, err = reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: nn})
	if err != nil {
		t.Fatalf("Reconcile (4): %v", err)
	}

	if err := k8sClient.Get(ctx, nn, ws); err != nil {
		t.Fatalf("Get Workspace: %v", err)
	}
	if ws.Status.Phase != workspacev1alpha1.WorkspacePhaseRunning {
		t.Errorf("Workspace status phase = %q, want Running", ws.Status.Phase)
	}
	if ws.Status.PodName != "john-workspace-pod" {
		t.Errorf("Workspace status podName = %q", ws.Status.PodName)
	}
	if ws.Status.ServiceEndpoint == "" {
		t.Error("Workspace status serviceEndpoint empty")
	}
}

func TestReconcile_InvalidSpec_SetsFailedStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("Failed to start envtest: %v", err)
	}
	defer func() {
		if err := env.Stop(); err != nil {
			t.Errorf("Failed to stop envtest: %v", err)
		}
	}()

	k8sClient, err := client.New(cfg, client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	ws := &workspacev1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "invalid-ws", Namespace: "default"},
		Spec: workspacev1alpha1.WorkspaceSpec{
			User:      workspacev1alpha1.UserInfo{ID: "", Email: "j@example.com"},
			Resources: workspacev1alpha1.ResourceRequirements{CPU: "1", Memory: "1Gi", Storage: "1Gi"},
			AIConfig: workspacev1alpha1.AIConfiguration{
				Providers: []workspacev1alpha1.AIProvider{
					{Name: "local", Endpoint: "http://x", Models: []string{"m"}},
				},
			},
			Persistence: workspacev1alpha1.PersistenceConfig{},
		},
	}
	if err := k8sClient.Create(ctx, ws); err != nil {
		t.Fatalf("Failed to create Workspace: %v", err)
	}

	reconciler := &WorkspaceReconciler{
		Client:         k8sClient,
		Scheme:         testScheme,
		WorkspaceImage: "workspace:test",
	}

	_, err = reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: ws.Name, Namespace: ws.Namespace}})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if err := k8sClient.Get(ctx, types.NamespacedName{Name: ws.Name, Namespace: ws.Namespace}, ws); err != nil {
		t.Fatalf("Get Workspace: %v", err)
	}
	if ws.Status.Phase != workspacev1alpha1.WorkspacePhaseFailed {
		t.Errorf("status.phase = %q, want Failed", ws.Status.Phase)
	}
	if ws.Status.Message == "" {
		t.Error("status.message expected non-empty for invalid spec")
	}
}
