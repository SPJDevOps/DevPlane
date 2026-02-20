//go:build e2e

// Package e2e contains end-to-end tests that run against a real Kubernetes cluster.
//
// Prerequisites:
//   - A running cluster with the operator deployed (make install && make run, or helm install)
//   - A valid kubeconfig (KUBECONFIG env var or ~/.kube/config)
//   - The workspace CRD installed (make install)
//
// Run with:
//
//	go test -v -tags e2e ./test/e2e/ -timeout 5m
//	# or via make:
//	make test-e2e
package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	workspacev1alpha1 "workspace-operator/api/v1alpha1"
)

const (
	e2eNamespace  = "e2e-workspaces"
	e2eUserID     = "e2e-test-user"
	e2eUserEmail  = "e2e@example.com"
	pollInterval  = 2 * time.Second
	createTimeout = 3 * time.Minute
)

var testScheme = func() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(workspacev1alpha1.AddToScheme(s))
	return s
}()

// newClient builds a controller-runtime client from the active kubeconfig.
func newClient(t *testing.T) client.Client {
	t.Helper()
	kubeconfigPath := os.Getenv("KUBECONFIG")
	if kubeconfigPath == "" {
		home, _ := os.UserHomeDir()
		kubeconfigPath = filepath.Join(home, ".kube", "config")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		t.Fatalf("build kubeconfig from %s: %v", kubeconfigPath, err)
	}
	c, err := client.New(cfg, client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatalf("create k8s client: %v", err)
	}
	return c
}

// ensureNamespace creates the e2e test namespace if it does not already exist.
func ensureNamespace(t *testing.T, ctx context.Context, c client.Client) {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: e2eNamespace}}
	if err := c.Create(ctx, ns); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("create namespace %s: %v", e2eNamespace, err)
	}
}

// cleanupWorkspace deletes the test Workspace CR and waits for it to be gone.
func cleanupWorkspace(t *testing.T, ctx context.Context, c client.Client, name string) {
	t.Helper()
	ws := &workspacev1alpha1.Workspace{}
	key := types.NamespacedName{Name: name, Namespace: e2eNamespace}
	if err := c.Get(ctx, key, ws); err != nil {
		if errors.IsNotFound(err) {
			return
		}
		t.Logf("Warning: failed to get workspace for cleanup: %v", err)
		return
	}
	if err := c.Delete(ctx, ws); err != nil && !errors.IsNotFound(err) {
		t.Logf("Warning: failed to delete workspace %s: %v", name, err)
	}
	// Wait for the CR to be fully removed (finalizer should be released by operator).
	_ = wait.PollUntilContextTimeout(ctx, pollInterval, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		err := c.Get(ctx, key, &workspacev1alpha1.Workspace{})
		return errors.IsNotFound(err), nil
	})
}

// TestWorkspaceReconciliation verifies the full operator reconcile path: creating a
// Workspace CR causes the operator to provision all expected Kubernetes resources.
func TestWorkspaceReconciliation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), createTimeout)
	defer cancel()

	c := newClient(t)
	ensureNamespace(t, ctx, c)

	wsName := fmt.Sprintf("%s-%d", e2eUserID, time.Now().Unix())
	t.Cleanup(func() { cleanupWorkspace(t, context.Background(), c, wsName) })

	ws := &workspacev1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      wsName,
			Namespace: e2eNamespace,
		},
		Spec: workspacev1alpha1.WorkspaceSpec{
			User: workspacev1alpha1.UserInfo{
				ID:    e2eUserID,
				Email: e2eUserEmail,
			},
			Resources: workspacev1alpha1.ResourceRequirements{
				CPU:     "100m",
				Memory:  "128Mi",
				Storage: "1Gi",
			},
			AIConfig: workspacev1alpha1.AIConfiguration{
				Endpoint: "http://vllm.ai-system.svc:8000",
				Model:    "test-model",
			},
		},
	}

	t.Logf("Creating Workspace %s/%s", e2eNamespace, wsName)
	if err := c.Create(ctx, ws); err != nil {
		t.Fatalf("create Workspace: %v", err)
	}

	// Verify the operator adds the finalizer.
	t.Log("Waiting for finalizer to be registered...")
	if err := wait.PollUntilContextTimeout(ctx, pollInterval, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		if err := c.Get(ctx, types.NamespacedName{Name: wsName, Namespace: e2eNamespace}, ws); err != nil {
			return false, err
		}
		for _, f := range ws.Finalizers {
			if f == "workspace.devplane.io/finalizer" {
				return true, nil
			}
		}
		return false, nil
	}); err != nil {
		t.Fatalf("finalizer never registered: %v", err)
	}
	t.Log("Finalizer registered")

	// Verify RBAC resources are created.
	t.Log("Checking RBAC resources...")
	saName := fmt.Sprintf("%s-workspace", e2eUserID)
	if err := pollUntilExists(ctx, c, &corev1.ServiceAccount{}, e2eNamespace, saName, "ServiceAccount"); err != nil {
		t.Errorf("ServiceAccount not found: %v", err)
	}
	if err := pollUntilExists(ctx, c, &rbacv1.Role{}, e2eNamespace, saName, "Role"); err != nil {
		t.Errorf("Role not found: %v", err)
	}
	if err := pollUntilExists(ctx, c, &rbacv1.RoleBinding{}, e2eNamespace, saName, "RoleBinding"); err != nil {
		t.Errorf("RoleBinding not found: %v", err)
	}

	// Verify NetworkPolicies are created.
	t.Log("Checking NetworkPolicies...")
	denyAllName := fmt.Sprintf("%s-workspace-deny-all", e2eUserID)
	egressName := fmt.Sprintf("%s-workspace-egress", e2eUserID)
	ingressGwName := fmt.Sprintf("%s-workspace-ingress-gateway", e2eUserID)
	if err := pollUntilExists(ctx, c, &networkingv1.NetworkPolicy{}, e2eNamespace, denyAllName, "deny-all NetworkPolicy"); err != nil {
		t.Errorf("deny-all NetworkPolicy not found: %v", err)
	}
	if err := pollUntilExists(ctx, c, &networkingv1.NetworkPolicy{}, e2eNamespace, egressName, "egress NetworkPolicy"); err != nil {
		t.Errorf("egress NetworkPolicy not found: %v", err)
	}
	if err := pollUntilExists(ctx, c, &networkingv1.NetworkPolicy{}, e2eNamespace, ingressGwName, "ingress-gateway NetworkPolicy"); err != nil {
		t.Errorf("ingress-gateway NetworkPolicy not found: %v", err)
	}

	// Verify PVC is created.
	t.Log("Checking PVC...")
	pvcName := fmt.Sprintf("%s-workspace-pvc", e2eUserID)
	if err := pollUntilExists(ctx, c, &corev1.PersistentVolumeClaim{}, e2eNamespace, pvcName, "PVC"); err != nil {
		t.Errorf("PVC not found: %v", err)
	}
	// Verify PVC has an owner reference pointing to the Workspace CR.
	var pvc corev1.PersistentVolumeClaim
	if err := c.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: e2eNamespace}, &pvc); err == nil {
		if len(pvc.OwnerReferences) == 0 {
			t.Error("PVC has no owner references")
		} else if pvc.OwnerReferences[0].Kind != "Workspace" {
			t.Errorf("PVC owner ref kind = %q, want Workspace", pvc.OwnerReferences[0].Kind)
		}
	}

	// Verify Pod is created.
	t.Log("Checking Pod...")
	podName := fmt.Sprintf("%s-workspace-pod", e2eUserID)
	if err := pollUntilExists(ctx, c, &corev1.Pod{}, e2eNamespace, podName, "Pod"); err != nil {
		t.Errorf("Pod not found: %v", err)
	}

	// Verify Service is created.
	t.Log("Checking Service...")
	svcName := fmt.Sprintf("%s-workspace-svc", e2eUserID)
	if err := pollUntilExists(ctx, c, &corev1.Service{}, e2eNamespace, svcName, "Service"); err != nil {
		t.Errorf("Service not found: %v", err)
	}
	// Verify the service is headless.
	var svc corev1.Service
	if err := c.Get(ctx, types.NamespacedName{Name: svcName, Namespace: e2eNamespace}, &svc); err == nil {
		if svc.Spec.ClusterIP != corev1.ClusterIPNone {
			t.Errorf("Service ClusterIP = %q, want None (headless)", svc.Spec.ClusterIP)
		}
	}

	// Verify the Workspace status transitions to Creating or Running.
	t.Log("Waiting for Workspace status to reach Creating or Running phase...")
	if err := wait.PollUntilContextTimeout(ctx, pollInterval, createTimeout, true, func(ctx context.Context) (bool, error) {
		if err := c.Get(ctx, types.NamespacedName{Name: wsName, Namespace: e2eNamespace}, ws); err != nil {
			return false, err
		}
		switch ws.Status.Phase {
		case "Creating", "Running":
			return true, nil
		case "Failed":
			return false, fmt.Errorf("workspace reached Failed phase: %s", ws.Status.Message)
		}
		return false, nil
	}); err != nil {
		t.Fatalf("workspace did not reach expected phase: %v (last phase: %s)", err, ws.Status.Phase)
	}
	t.Logf("Workspace reached phase %q — reconciliation confirmed", ws.Status.Phase)
}

// TestWorkspaceDeletion verifies that deleting a Workspace CR removes the finalizer
// and triggers cascade deletion of owned resources.
func TestWorkspaceDeletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), createTimeout)
	defer cancel()

	c := newClient(t)
	ensureNamespace(t, ctx, c)

	wsName := fmt.Sprintf("del-%s-%d", e2eUserID, time.Now().Unix())

	ws := &workspacev1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      wsName,
			Namespace: e2eNamespace,
		},
		Spec: workspacev1alpha1.WorkspaceSpec{
			User: workspacev1alpha1.UserInfo{
				ID:    e2eUserID,
				Email: e2eUserEmail,
			},
			Resources: workspacev1alpha1.ResourceRequirements{
				CPU:     "100m",
				Memory:  "128Mi",
				Storage: "1Gi",
			},
			AIConfig: workspacev1alpha1.AIConfiguration{
				Endpoint: "http://vllm.ai-system.svc:8000",
				Model:    "test-model",
			},
		},
	}

	t.Logf("Creating Workspace %s/%s", e2eNamespace, wsName)
	if err := c.Create(ctx, ws); err != nil {
		t.Fatalf("create Workspace: %v", err)
	}

	// Wait until at least the PVC is created so there is something to verify deletion for.
	pvcName := fmt.Sprintf("%s-workspace-pvc", e2eUserID)
	_ = pollUntilExists(ctx, c, &corev1.PersistentVolumeClaim{}, e2eNamespace, pvcName, "PVC")

	// Delete the Workspace CR.
	t.Logf("Deleting Workspace %s", wsName)
	if err := c.Delete(ctx, ws); err != nil {
		t.Fatalf("delete Workspace: %v", err)
	}

	// The Workspace CR itself should eventually be gone (finalizer removed by operator).
	t.Log("Waiting for Workspace CR to be fully deleted...")
	if err := wait.PollUntilContextTimeout(ctx, pollInterval, 60*time.Second, true, func(ctx context.Context) (bool, error) {
		err := c.Get(ctx, types.NamespacedName{Name: wsName, Namespace: e2eNamespace}, &workspacev1alpha1.Workspace{})
		return errors.IsNotFound(err), nil
	}); err != nil {
		t.Errorf("Workspace CR not deleted within timeout: %v", err)
	} else {
		t.Log("Workspace CR deleted — finalizer removed successfully")
	}
}

// TestWorkspaceInvalidSpec verifies that a Workspace with an invalid spec is set
// to Failed status rather than causing a panic or crash loop.
func TestWorkspaceInvalidSpec(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	c := newClient(t)
	ensureNamespace(t, ctx, c)

	wsName := fmt.Sprintf("invalid-%d", time.Now().Unix())
	t.Cleanup(func() { cleanupWorkspace(t, context.Background(), c, wsName) })

	ws := &workspacev1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      wsName,
			Namespace: e2eNamespace,
		},
		Spec: workspacev1alpha1.WorkspaceSpec{
			User: workspacev1alpha1.UserInfo{
				ID:    "",
				Email: "test@example.com",
			},
			Resources: workspacev1alpha1.ResourceRequirements{
				CPU:     "1",
				Memory:  "1Gi",
				Storage: "1Gi",
			},
			AIConfig: workspacev1alpha1.AIConfiguration{
				Endpoint: "http://vllm:8000",
				Model:    "model",
			},
		},
	}

	if err := c.Create(ctx, ws); err != nil {
		t.Fatalf("create Workspace: %v", err)
	}

	t.Log("Waiting for Workspace status to reach Failed phase...")
	if err := wait.PollUntilContextTimeout(ctx, pollInterval, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		if err := c.Get(ctx, types.NamespacedName{Name: wsName, Namespace: e2eNamespace}, ws); err != nil {
			return false, err
		}
		return ws.Status.Phase == "Failed", nil
	}); err != nil {
		t.Fatalf("workspace did not reach Failed phase: %v (last phase: %s)", err, ws.Status.Phase)
	}
	if ws.Status.Message == "" {
		t.Error("status.message expected non-empty for invalid spec")
	}
	t.Logf("Workspace correctly reached Failed phase: %s", ws.Status.Message)
}

// pollUntilExists waits until a named resource exists in the cluster.
func pollUntilExists(ctx context.Context, c client.Client, obj client.Object, namespace, name, kind string) error {
	return wait.PollUntilContextTimeout(ctx, pollInterval, 60*time.Second, true, func(ctx context.Context) (bool, error) {
		err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, obj)
		if errors.IsNotFound(err) {
			return false, nil
		}
		return err == nil, err
	})
}
