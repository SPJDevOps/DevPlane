package gateway

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	workspacev1alpha1 "workspace-operator/api/v1alpha1"
)

var testScheme = func() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(workspacev1alpha1.AddToScheme(s))
	return s
}()

func testConfig() LifecycleConfig {
	return LifecycleConfig{
		Providers: []workspacev1alpha1.AIProvider{
			{Name: "local", Endpoint: "http://vllm:8000", Models: []string{"model"}},
		},
		DefaultCPU:     "1",
		DefaultMemory:  "1Gi",
		DefaultStorage: "10Gi",
	}
}

func TestEnsureWorkspace_CreatesNew(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	fc := fake.NewClientBuilder().WithScheme(testScheme).
		WithStatusSubresource(&workspacev1alpha1.Workspace{}).
		Build()
	log := zap.New(zap.UseDevMode(true))

	lm := NewLifecycleManager(fc, log, testConfig())

	claims := &Claims{Sub: "user1", Email: "user1@test.com", UserID: "user1"}

	// Pre-create a workspace in Running state so EnsureWorkspace can find it
	ws := &workspacev1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "user1", Namespace: "default"},
		Spec: workspacev1alpha1.WorkspaceSpec{
			User:      workspacev1alpha1.UserInfo{ID: "user1", Email: "user1@test.com"},
			Resources: workspacev1alpha1.ResourceRequirements{CPU: "1", Memory: "1Gi", Storage: "10Gi"},
			AIConfig: workspacev1alpha1.AIConfiguration{
				Providers: []workspacev1alpha1.AIProvider{
					{Name: "local", Endpoint: "http://vllm:8000", Models: []string{"model"}},
				},
			},
		},
	}
	if err := fc.Create(ctx, ws); err != nil {
		t.Fatalf("Create workspace: %v", err)
	}
	// Set it to Running
	ws.Status.Phase = workspacev1alpha1.WorkspacePhaseRunning
	ws.Status.ServiceEndpoint = "user1-workspace-svc.default.svc.cluster.local"
	if err := fc.Status().Update(ctx, ws); err != nil {
		t.Fatalf("Update status: %v", err)
	}

	result, err := lm.EnsureWorkspace(ctx, "default", claims)
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	if result.Status.Phase != workspacev1alpha1.WorkspacePhaseRunning {
		t.Errorf("phase = %q, want Running", result.Status.Phase)
	}
}

func TestEnsureWorkspace_FailedWorkspace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	fc := fake.NewClientBuilder().WithScheme(testScheme).
		WithStatusSubresource(&workspacev1alpha1.Workspace{}).
		Build()
	log := zap.New(zap.UseDevMode(true))

	lm := NewLifecycleManager(fc, log, testConfig())

	// Pre-create a workspace in Failed state
	ws := &workspacev1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "user2", Namespace: "default"},
		Spec: workspacev1alpha1.WorkspaceSpec{
			User:      workspacev1alpha1.UserInfo{ID: "user2", Email: "user2@test.com"},
			Resources: workspacev1alpha1.ResourceRequirements{CPU: "1", Memory: "1Gi", Storage: "10Gi"},
			AIConfig: workspacev1alpha1.AIConfiguration{
				Providers: []workspacev1alpha1.AIProvider{
					{Name: "local", Endpoint: "http://vllm:8000", Models: []string{"model"}},
				},
			},
		},
	}
	if err := fc.Create(ctx, ws); err != nil {
		t.Fatalf("Create workspace: %v", err)
	}
	ws.Status.Phase = workspacev1alpha1.WorkspacePhaseFailed
	ws.Status.Message = "pod crash"
	if err := fc.Status().Update(ctx, ws); err != nil {
		t.Fatalf("Update status: %v", err)
	}

	claims := &Claims{Sub: "user2", Email: "user2@test.com", UserID: "user2"}
	_, err := lm.EnsureWorkspace(ctx, "default", claims)
	if err == nil {
		t.Fatal("EnsureWorkspace should fail for Failed workspace")
	}
}

func TestEnsureWorkspace_CreatesNewCR(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	fc := fake.NewClientBuilder().WithScheme(testScheme).
		WithStatusSubresource(&workspacev1alpha1.Workspace{}).
		Build()
	log := zap.New(zap.UseDevMode(true))

	lm := NewLifecycleManager(fc, log, testConfig())
	claims := &Claims{Sub: "newuser", Email: "new@test.com", UserID: "newuser"}

	// Run in a goroutine since EnsureWorkspace will poll and timeout
	done := make(chan error, 1)
	go func() {
		_, err := lm.EnsureWorkspace(ctx, "default", claims)
		done <- err
	}()

	// Give it a moment to create the CR, then verify it was created
	time.Sleep(100 * time.Millisecond)
	ws := &workspacev1alpha1.Workspace{}
	err := fc.Get(ctx, types.NamespacedName{Name: "newuser", Namespace: "default"}, ws)
	if err != nil {
		t.Fatalf("Workspace CR should have been created: %v", err)
	}
	if ws.Spec.User.ID != "newuser" {
		t.Errorf("user.id = %q, want newuser", ws.Spec.User.ID)
	}
	if ws.Spec.Resources.CPU != "1" {
		t.Errorf("resources.cpu = %q, want 1", ws.Spec.Resources.CPU)
	}

	// Set it to Running to unblock
	ws.Status.Phase = workspacev1alpha1.WorkspacePhaseRunning
	ws.Status.ServiceEndpoint = "newuser-workspace-svc.default.svc.cluster.local"
	if err := fc.Status().Update(ctx, ws); err != nil {
		t.Fatalf("Update status: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("EnsureWorkspace: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("EnsureWorkspace timed out")
	}
}

func TestWaitForRunning_StoppedThenRunning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	fc := fake.NewClientBuilder().WithScheme(testScheme).
		WithStatusSubresource(&workspacev1alpha1.Workspace{}).
		Build()
	log := zap.New(zap.UseDevMode(true))

	ws := &workspacev1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "stopws", Namespace: "default"},
		Spec: workspacev1alpha1.WorkspaceSpec{
			User:      workspacev1alpha1.UserInfo{ID: "stopws", Email: "stop@test.com"},
			Resources: workspacev1alpha1.ResourceRequirements{CPU: "1", Memory: "1Gi", Storage: "10Gi"},
			AIConfig: workspacev1alpha1.AIConfiguration{
				Providers: []workspacev1alpha1.AIProvider{
					{Name: "local", Endpoint: "http://vllm:8000", Models: []string{"model"}},
				},
			},
		},
	}
	if err := fc.Create(ctx, ws); err != nil {
		t.Fatalf("Create workspace: %v", err)
	}
	ws.Status.Phase = workspacev1alpha1.WorkspacePhaseStopped
	if err := fc.Status().Update(ctx, ws); err != nil {
		t.Fatalf("Set Stopped status: %v", err)
	}

	lm := NewLifecycleManager(fc, log, testConfig())
	claims := &Claims{Sub: "stopws", Email: "stop@test.com", UserID: "stopws"}

	// After waitForRunning patches the Stopped phase clear, update to Running.
	// workspaceReadyPoll = 2s, so do this within that window.
	go func() {
		time.Sleep(500 * time.Millisecond)
		var updated workspacev1alpha1.Workspace
		if err := fc.Get(ctx, types.NamespacedName{Name: "stopws", Namespace: "default"}, &updated); err != nil {
			return
		}
		updated.Status.Phase = workspacev1alpha1.WorkspacePhaseRunning
		updated.Status.ServiceEndpoint = "stopws-workspace-svc.default.svc.cluster.local"
		_ = fc.Status().Update(ctx, &updated)
	}()

	result, err := lm.EnsureWorkspace(ctx, "default", claims)
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	if result.Status.Phase != workspacev1alpha1.WorkspacePhaseRunning {
		t.Errorf("phase = %q, want Running", result.Status.Phase)
	}
}

func TestWaitForRunning_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately so waitForRunning returns via ctx.Done().
	cancel()

	fc := fake.NewClientBuilder().WithScheme(testScheme).
		WithStatusSubresource(&workspacev1alpha1.Workspace{}).
		Build()
	log := zap.New(zap.UseDevMode(true))

	// Create a workspace with no phase — waitForRunning won't short-circuit on phase.
	ws := &workspacev1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "ctxws", Namespace: "default"},
		Spec: workspacev1alpha1.WorkspaceSpec{
			User:      workspacev1alpha1.UserInfo{ID: "ctxws", Email: "ctx@test.com"},
			Resources: workspacev1alpha1.ResourceRequirements{CPU: "1", Memory: "1Gi", Storage: "10Gi"},
			AIConfig: workspacev1alpha1.AIConfiguration{
				Providers: []workspacev1alpha1.AIProvider{
					{Name: "local", Endpoint: "http://vllm:8000", Models: []string{"model"}},
				},
			},
		},
	}
	if err := fc.Create(context.Background(), ws); err != nil {
		t.Fatalf("Create workspace: %v", err)
	}

	lm := NewLifecycleManager(fc, log, testConfig())
	claims := &Claims{Sub: "ctxws", Email: "ctx@test.com", UserID: "ctxws"}
	_, err := lm.EnsureWorkspace(ctx, "default", claims)
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

func TestTouchLastAccessed(t *testing.T) {
	ctx := context.Background()
	fc := fake.NewClientBuilder().WithScheme(testScheme).
		WithStatusSubresource(&workspacev1alpha1.Workspace{}).
		Build()
	log := zap.New(zap.UseDevMode(true))

	ws := &workspacev1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "touch-ws", Namespace: "default"},
		Spec: workspacev1alpha1.WorkspaceSpec{
			User:      workspacev1alpha1.UserInfo{ID: "touch", Email: "touch@test.com"},
			Resources: workspacev1alpha1.ResourceRequirements{CPU: "1", Memory: "1Gi", Storage: "10Gi"},
			AIConfig: workspacev1alpha1.AIConfiguration{
				Providers: []workspacev1alpha1.AIProvider{
					{Name: "local", Endpoint: "http://vllm:8000", Models: []string{"model"}},
				},
			},
		},
	}
	if err := fc.Create(ctx, ws); err != nil {
		t.Fatalf("Create workspace: %v", err)
	}

	lm := NewLifecycleManager(fc, log, testConfig())
	lm.TouchLastAccessed(ctx, ws)

	// Verify LastAccessed was stamped (best-effort, no error expected).
	var updated workspacev1alpha1.Workspace
	if err := fc.Get(ctx, types.NamespacedName{Name: "touch-ws", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("Get workspace: %v", err)
	}
	// LastAccessed should now be set.
	if updated.Status.LastAccessed.IsZero() {
		t.Error("LastAccessed should be non-zero after TouchLastAccessed")
	}
}

func TestLifecycleManager_GetExisting(t *testing.T) {
	ctx := context.Background()
	fc := fake.NewClientBuilder().WithScheme(testScheme).
		WithStatusSubresource(&workspacev1alpha1.Workspace{}).
		Build()
	log := zap.New(zap.UseDevMode(true))

	// Create an existing Running workspace
	ws := &workspacev1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "existing", Namespace: "ns1"},
		Spec: workspacev1alpha1.WorkspaceSpec{
			User:      workspacev1alpha1.UserInfo{ID: "existing", Email: "e@test.com"},
			Resources: workspacev1alpha1.ResourceRequirements{CPU: "1", Memory: "1Gi", Storage: "5Gi"},
			AIConfig: workspacev1alpha1.AIConfiguration{
				Providers: []workspacev1alpha1.AIProvider{
					{Name: "local", Endpoint: "http://vllm:8000", Models: []string{"m"}},
				},
			},
		},
	}
	if err := fc.Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	ws.Status.Phase = workspacev1alpha1.WorkspacePhaseRunning
	ws.Status.PodName = "existing-workspace-pod"
	ws.Status.ServiceEndpoint = "existing-workspace-svc.ns1.svc.cluster.local"
	if err := fc.Status().Update(ctx, ws); err != nil {
		t.Fatal(err)
	}

	lm := NewLifecycleManager(fc, log, testConfig())
	claims := &Claims{Sub: "existing", Email: "e@test.com", UserID: "existing"}
	result, err := lm.EnsureWorkspace(ctx, "ns1", claims)
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	if result.Status.PodName != "existing-workspace-pod" {
		t.Errorf("podName = %q, want existing-workspace-pod", result.Status.PodName)
	}

	// Verify no duplicate was created
	var list workspacev1alpha1.WorkspaceList
	if err := fc.List(ctx, &list, client.InNamespace("ns1")); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Errorf("expected 1 workspace, got %d", len(list.Items))
	}
}

// --- EnsureExists tests ---

func TestEnsureExists_CreatesNewCR(t *testing.T) {
	ctx := context.Background()
	fc := fake.NewClientBuilder().WithScheme(testScheme).
		WithStatusSubresource(&workspacev1alpha1.Workspace{}).
		Build()
	log := zap.New(zap.UseDevMode(true))

	lm := NewLifecycleManager(fc, log, testConfig())
	claims := &Claims{Sub: "newex", Email: "newex@test.com", UserID: "newex"}

	ws, err := lm.EnsureExists(ctx, "default", claims)
	if err != nil {
		t.Fatalf("EnsureExists: %v", err)
	}
	if ws == nil {
		t.Fatal("EnsureExists returned nil workspace")
	}
	if ws.Spec.User.ID != "newex" {
		t.Errorf("user.id = %q, want newex", ws.Spec.User.ID)
	}
	if ws.Spec.Resources.CPU != "1" {
		t.Errorf("resources.cpu = %q, want 1", ws.Spec.Resources.CPU)
	}
	// Brand-new CR has no phase set.
	if ws.Status.Phase != "" {
		t.Errorf("phase = %q, want empty for new CR", ws.Status.Phase)
	}
}

func TestEnsureExists_ExistingRunningReturnsImmediately(t *testing.T) {
	ctx := context.Background()
	fc := fake.NewClientBuilder().WithScheme(testScheme).
		WithStatusSubresource(&workspacev1alpha1.Workspace{}).
		Build()
	log := zap.New(zap.UseDevMode(true))

	ws := &workspacev1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "runex", Namespace: "default"},
		Spec: workspacev1alpha1.WorkspaceSpec{
			User:      workspacev1alpha1.UserInfo{ID: "runex", Email: "run@test.com"},
			Resources: workspacev1alpha1.ResourceRequirements{CPU: "1", Memory: "1Gi", Storage: "10Gi"},
			AIConfig: workspacev1alpha1.AIConfiguration{
				Providers: []workspacev1alpha1.AIProvider{
					{Name: "local", Endpoint: "http://vllm:8000", Models: []string{"model"}},
				},
			},
		},
	}
	if err := fc.Create(ctx, ws); err != nil {
		t.Fatalf("Create: %v", err)
	}
	ws.Status.Phase = workspacev1alpha1.WorkspacePhaseRunning
	ws.Status.ServiceEndpoint = "runex-workspace-svc.default.svc.cluster.local"
	if err := fc.Status().Update(ctx, ws); err != nil {
		t.Fatalf("Update status: %v", err)
	}

	lm := NewLifecycleManager(fc, log, testConfig())
	claims := &Claims{Sub: "runex", Email: "run@test.com", UserID: "runex"}

	result, err := lm.EnsureExists(ctx, "default", claims)
	if err != nil {
		t.Fatalf("EnsureExists: %v", err)
	}
	if result.Status.Phase != workspacev1alpha1.WorkspacePhaseRunning {
		t.Errorf("phase = %q, want Running", result.Status.Phase)
	}
	if result.Status.ServiceEndpoint == "" {
		t.Error("ServiceEndpoint should be set for Running workspace")
	}
}

func TestEnsureExists_StoppedResetsPhase(t *testing.T) {
	ctx := context.Background()
	fc := fake.NewClientBuilder().WithScheme(testScheme).
		WithStatusSubresource(&workspacev1alpha1.Workspace{}).
		Build()
	log := zap.New(zap.UseDevMode(true))

	ws := &workspacev1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "stopex", Namespace: "default"},
		Spec: workspacev1alpha1.WorkspaceSpec{
			User:      workspacev1alpha1.UserInfo{ID: "stopex", Email: "stop@test.com"},
			Resources: workspacev1alpha1.ResourceRequirements{CPU: "1", Memory: "1Gi", Storage: "10Gi"},
			AIConfig: workspacev1alpha1.AIConfiguration{
				Providers: []workspacev1alpha1.AIProvider{
					{Name: "local", Endpoint: "http://vllm:8000", Models: []string{"model"}},
				},
			},
		},
	}
	if err := fc.Create(ctx, ws); err != nil {
		t.Fatalf("Create: %v", err)
	}
	ws.Status.Phase = workspacev1alpha1.WorkspacePhaseStopped
	ws.Status.PodName = "old-pod"
	ws.Status.Message = "idle timeout"
	if err := fc.Status().Update(ctx, ws); err != nil {
		t.Fatalf("Update status: %v", err)
	}

	lm := NewLifecycleManager(fc, log, testConfig())
	claims := &Claims{Sub: "stopex", Email: "stop@test.com", UserID: "stopex"}

	result, err := lm.EnsureExists(ctx, "default", claims)
	if err != nil {
		t.Fatalf("EnsureExists: %v", err)
	}
	// Phase, PodName, and Message should be cleared to re-trigger reconciliation.
	if result.Status.Phase != "" {
		t.Errorf("phase = %q, want empty after Stopped recovery", result.Status.Phase)
	}
	if result.Status.PodName != "" {
		t.Errorf("podName = %q, want empty after Stopped recovery", result.Status.PodName)
	}
	if result.Status.Message != "" {
		t.Errorf("message = %q, want empty after Stopped recovery", result.Status.Message)
	}
}

func TestEnsureExists_PendingReturnsWithoutBlocking(t *testing.T) {
	ctx := context.Background()
	fc := fake.NewClientBuilder().WithScheme(testScheme).
		WithStatusSubresource(&workspacev1alpha1.Workspace{}).
		Build()
	log := zap.New(zap.UseDevMode(true))

	ws := &workspacev1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "pendex", Namespace: "default"},
		Spec: workspacev1alpha1.WorkspaceSpec{
			User:      workspacev1alpha1.UserInfo{ID: "pendex", Email: "pend@test.com"},
			Resources: workspacev1alpha1.ResourceRequirements{CPU: "1", Memory: "1Gi", Storage: "10Gi"},
			AIConfig: workspacev1alpha1.AIConfiguration{
				Providers: []workspacev1alpha1.AIProvider{
					{Name: "local", Endpoint: "http://vllm:8000", Models: []string{"model"}},
				},
			},
		},
	}
	if err := fc.Create(ctx, ws); err != nil {
		t.Fatalf("Create: %v", err)
	}
	ws.Status.Phase = workspacev1alpha1.WorkspacePhasePending
	if err := fc.Status().Update(ctx, ws); err != nil {
		t.Fatalf("Update status: %v", err)
	}

	lm := NewLifecycleManager(fc, log, testConfig())
	claims := &Claims{Sub: "pendex", Email: "pend@test.com", UserID: "pendex"}

	// EnsureExists must return immediately — no blocking poll.
	result, err := lm.EnsureExists(ctx, "default", claims)
	if err != nil {
		t.Fatalf("EnsureExists: %v", err)
	}
	if result.Status.Phase != workspacev1alpha1.WorkspacePhasePending {
		t.Errorf("phase = %q, want Pending", result.Status.Phase)
	}
}
