package controllers

import (
	"context"
	"path/filepath"
	"testing"
	"time"

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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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

func TestSetupWithManager_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

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

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: testScheme})
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	r := &WorkspaceReconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		WorkspaceImage: "workspace:test",
	}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatalf("SetupWithManager: %v", err)
	}
}

// ── Fake-client unit tests (no envtest / etcd required) ──────────────────────
//
// These tests cover controller branches that the envtest integration tests do
// not reach (stopped phase, deletion, PVC lost, pod failure states, etc.).

// wsWithFinalizer creates a minimal valid Workspace that already carries the
// workspaceFinalizer so a reconcile call skips the "register finalizer" step.
func wsWithFinalizer(name, userID string) *workspacev1alpha1.Workspace {
	return &workspacev1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  "default",
			UID:        types.UID("uid-" + userID),
			Finalizers: []string{workspaceFinalizer},
		},
		Spec: workspacev1alpha1.WorkspaceSpec{
			User: workspacev1alpha1.UserInfo{ID: userID, Email: userID + "@test.com"},
			Resources: workspacev1alpha1.ResourceRequirements{
				CPU: "100m", Memory: "128Mi", Storage: "1Gi",
			},
			AIConfig: workspacev1alpha1.AIConfiguration{
				Providers: []workspacev1alpha1.AIProvider{
					{Name: "local", Endpoint: "http://vllm:8000", Models: []string{"model"}},
				},
			},
		},
	}
}

// newFakeReconciler returns a WorkspaceReconciler backed by a fake client.
// Objects in objs are pre-seeded (including any status already set on them).
func newFakeReconciler(t *testing.T, objs ...client.Object) (*WorkspaceReconciler, client.Client) {
	t.Helper()
	fc := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithStatusSubresource(&workspacev1alpha1.Workspace{}).
		WithObjects(objs...).
		Build()
	return &WorkspaceReconciler{
		Client:         fc,
		Scheme:         testScheme,
		WorkspaceImage: "workspace:test",
	}, fc
}

// reconcileNN is a convenience that issues one Reconcile call for the named object.
func reconcileNN(t *testing.T, r *WorkspaceReconciler, nn types.NamespacedName) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: nn}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

// getWS fetches the workspace or fatals the test.
func getWS(t *testing.T, fc client.Client, nn types.NamespacedName) workspacev1alpha1.Workspace {
	t.Helper()
	var ws workspacev1alpha1.Workspace
	if err := fc.Get(context.Background(), nn, &ws); err != nil {
		t.Fatalf("Get Workspace: %v", err)
	}
	return ws
}

func TestReconcile_StoppedPhase(t *testing.T) {
	ws := wsWithFinalizer("stopped-ws", "alice")
	ws.Status.Phase = workspacev1alpha1.WorkspacePhaseStopped
	r, fc := newFakeReconciler(t, ws)

	nn := types.NamespacedName{Name: ws.Name, Namespace: ws.Namespace}
	reconcileNN(t, r, nn)

	// No PVC should have been created (reconcile returns early for Stopped).
	var pvcList corev1.PersistentVolumeClaimList
	if err := fc.List(context.Background(), &pvcList, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	if len(pvcList.Items) != 0 {
		t.Errorf("expected no PVCs for stopped workspace, got %d", len(pvcList.Items))
	}
}

func TestReconcile_Delete(t *testing.T) {
	ctx := context.Background()
	ws := wsWithFinalizer("del-ws", "bob")
	r, fc := newFakeReconciler(t, ws)

	nn := types.NamespacedName{Name: ws.Name, Namespace: ws.Namespace}

	// Delete workspace — fake client sets DeletionTimestamp because finalizers are present.
	var stored workspacev1alpha1.Workspace
	if err := fc.Get(ctx, nn, &stored); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := fc.Delete(ctx, &stored); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Reconcile should call reconcileDelete, removing the finalizer.
	reconcileNN(t, r, nn)

	// After removing the last finalizer the fake client deletes the object.
	var ws2 workspacev1alpha1.Workspace
	if err := fc.Get(ctx, nn, &ws2); err == nil {
		// If found (unlikely), at least the finalizer must be gone.
		if len(ws2.Finalizers) != 0 {
			t.Errorf("expected no finalizers after delete, got %v", ws2.Finalizers)
		}
	}
}

func TestReconcile_PVCLost(t *testing.T) {
	ws := wsWithFinalizer("pvc-lost-ws", "charlie")

	// Pre-create a PVC with Lost status (not a status subresource → status stored directly).
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "charlie-workspace-pvc",
			Namespace: "default",
		},
		Status: corev1.PersistentVolumeClaimStatus{
			Phase: corev1.ClaimLost,
		},
	}
	r, fc := newFakeReconciler(t, ws, pvc)

	nn := types.NamespacedName{Name: ws.Name, Namespace: ws.Namespace}
	reconcileNN(t, r, nn)

	stored := getWS(t, fc, nn)
	if stored.Status.Phase != workspacev1alpha1.WorkspacePhaseFailed {
		t.Errorf("status.phase = %q, want Failed", stored.Status.Phase)
	}
}

func TestReconcile_PodFailed(t *testing.T) {
	ws := wsWithFinalizer("pod-failed-ws", "dave")

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "dave-workspace-pvc", Namespace: "default"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "dave-workspace-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "workspace", Image: "workspace:test"}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "OOMKilled"},
	}
	r, fc := newFakeReconciler(t, ws, pvc, pod)

	nn := types.NamespacedName{Name: ws.Name, Namespace: ws.Namespace}
	reconcileNN(t, r, nn)

	stored := getWS(t, fc, nn)
	if stored.Status.Phase != workspacev1alpha1.WorkspacePhaseFailed {
		t.Errorf("status.phase = %q, want Failed", stored.Status.Phase)
	}
}

func TestReconcile_CrashLoopBackOff(t *testing.T) {
	ws := wsWithFinalizer("crash-ws", "eve")

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "eve-workspace-pvc", Namespace: "default"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "eve-workspace-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "workspace", Image: "workspace:test"}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "workspace",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason:  "CrashLoopBackOff",
							Message: "back-off restarting failed container",
						},
					},
				},
			},
		},
	}
	r, fc := newFakeReconciler(t, ws, pvc, pod)

	nn := types.NamespacedName{Name: ws.Name, Namespace: ws.Namespace}
	reconcileNN(t, r, nn)

	stored := getWS(t, fc, nn)
	if stored.Status.Phase != workspacev1alpha1.WorkspacePhaseFailed {
		t.Errorf("status.phase = %q, want Failed", stored.Status.Phase)
	}
}

func TestReconcile_ImagePullBackOff(t *testing.T) {
	ws := wsWithFinalizer("imgpull-ws", "frank")

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "frank-workspace-pvc", Namespace: "default"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "frank-workspace-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "workspace", Image: "workspace:test"}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "workspace",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"},
					},
				},
			},
		},
	}
	r, fc := newFakeReconciler(t, ws, pvc, pod)

	nn := types.NamespacedName{Name: ws.Name, Namespace: ws.Namespace}
	reconcileNN(t, r, nn)

	stored := getWS(t, fc, nn)
	if stored.Status.Phase != workspacev1alpha1.WorkspacePhaseFailed {
		t.Errorf("status.phase = %q, want Failed (ImagePullBackOff)", stored.Status.Phase)
	}
}

func TestReconcile_PodCreatingPhase(t *testing.T) {
	ws := wsWithFinalizer("creating-ws", "grace")

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "grace-workspace-pvc", Namespace: "default"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "grace-workspace-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "workspace", Image: "workspace:test"}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodPending},
	}
	r, fc := newFakeReconciler(t, ws, pvc, pod)

	nn := types.NamespacedName{Name: ws.Name, Namespace: ws.Namespace}
	reconcileNN(t, r, nn)

	stored := getWS(t, fc, nn)
	if stored.Status.Phase != workspacev1alpha1.WorkspacePhaseCreating {
		t.Errorf("status.phase = %q, want Creating", stored.Status.Phase)
	}
}

func TestReconcile_PodStartingNoPhase(t *testing.T) {
	ws := wsWithFinalizer("noPhase-ws", "heidi")

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "heidi-workspace-pvc", Namespace: "default"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	}
	// Pod with no phase set at all → triggers "Pod starting" message.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "heidi-workspace-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "workspace", Image: "workspace:test"}},
		},
	}
	r, fc := newFakeReconciler(t, ws, pvc, pod)

	nn := types.NamespacedName{Name: ws.Name, Namespace: ws.Namespace}
	reconcileNN(t, r, nn)

	stored := getWS(t, fc, nn)
	if stored.Status.Phase != workspacev1alpha1.WorkspacePhaseCreating {
		t.Errorf("status.phase = %q, want Creating", stored.Status.Phase)
	}
}

func TestReconcile_IdleTimeout(t *testing.T) {
	ws := wsWithFinalizer("idle-ws", "ivan")
	// LastAccessed was 2 hours ago.
	ws.Status.LastAccessed = metav1.NewTime(time.Now().Add(-2 * time.Hour))

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "ivan-workspace-pvc", Namespace: "default"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "ivan-workspace-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "workspace", Image: "workspace:test"}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}
	r, fc := newFakeReconciler(t, ws, pvc, pod)
	// IdleTimeout of 1 hour → workspace that was last accessed 2 hours ago is idle.
	r.IdleTimeout = time.Hour

	nn := types.NamespacedName{Name: ws.Name, Namespace: ws.Namespace}
	reconcileNN(t, r, nn)

	// Pod should be deleted.
	var p corev1.Pod
	if err := fc.Get(context.Background(), types.NamespacedName{Name: "ivan-workspace-pod", Namespace: "default"}, &p); err == nil {
		t.Error("expected pod to be deleted after idle timeout")
	}

	stored := getWS(t, fc, nn)
	if stored.Status.Phase != workspacev1alpha1.WorkspacePhaseStopped {
		t.Errorf("status.phase = %q, want Stopped", stored.Status.Phase)
	}
}

func TestReconcile_PodImageChanged(t *testing.T) {
	ctx := context.Background()
	ws := wsWithFinalizer("imgchange-ws", "judy")

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "judy-workspace-pvc", Namespace: "default"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	}
	// Pod was created with an old image.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "judy-workspace-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "workspace", Image: "workspace:old"}},
		},
	}
	r, fc := newFakeReconciler(t, ws, pvc, pod)
	// New desired image differs from the pod's current image.
	r.WorkspaceImage = "workspace:new"

	nn := types.NamespacedName{Name: ws.Name, Namespace: ws.Namespace}
	reconcileNN(t, r, nn)

	// Pod should be deleted so the next reconcile recreates it with the new image.
	var p corev1.Pod
	if err := fc.Get(ctx, types.NamespacedName{Name: "judy-workspace-pod", Namespace: "default"}, &p); err == nil {
		t.Error("expected outdated pod to be deleted after image change")
	}
}

func TestReconcile_DefaultWorkspaceImage(t *testing.T) {
	ws := wsWithFinalizer("default-img-ws", "kim")
	r, _ := newFakeReconciler(t, ws)
	r.WorkspaceImage = "" // empty → should use "workspace:latest"

	nn := types.NamespacedName{Name: ws.Name, Namespace: ws.Namespace}
	// Run enough reconciles to create the Pod.
	for i := 0; i < 3; i++ {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: nn}); err != nil {
			t.Fatalf("Reconcile %d: %v", i, err)
		}
	}
	// Just verify reconcile didn't error — the image path is exercised.
}
