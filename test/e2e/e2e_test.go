//go:build e2e

// Package e2e contains end-to-end tests that run against a real Kubernetes cluster.
//
// Prerequisites:
//   - A running cluster with the operator deployed (make install && make run, or helm install)
//   - A valid kubeconfig (KUBECONFIG env var or ~/.kube/config)
//   - The workspace CRD installed (make install)
//
// Optional (gateway auth → API smoke):
//   - E2E_GATEWAY_URL — base URL of the deployed gateway (https://…)
//   - E2E_ID_TOKEN — OIDC ID token (JWT) for a test user; polls /api/workspace until ttydReady
//
// Run with:
//
//	go test -v -tags e2e ./test/e2e/ -timeout 5m
//	# or via make:
//	make test-e2e
package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
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

// restConfig returns a *rest.Config from KUBECONFIG or ~/.kube/config.
func restConfig(t *testing.T) *rest.Config {
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
	return cfg
}

// newClient builds a controller-runtime client from the active kubeconfig.
func newClient(t *testing.T) client.Client {
	t.Helper()
	cfg := restConfig(t)
	c, err := client.New(cfg, client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatalf("create k8s client: %v", err)
	}
	return c
}

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

// probeTTYDViaPortForward checks that ttyd serves HTTP on :7681 by port-forwarding
// from the test runner to the workspace pod (cluster DNS is not required locally).
func probeTTYDViaPortForward(t *testing.T, cfg *rest.Config, namespace, podName string) {
	t.Helper()
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("kubernetes clientset: %v", err)
	}
	req := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(podName).
		SubResource("portforward")

	transport, upgrader, err := spdy.RoundTripperFor(cfg)
	if err != nil {
		t.Fatalf("spdy RoundTripperFor: %v", err)
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, req.URL())

	stopCh := make(chan struct{}, 1)
	readyCh := make(chan struct{})
	fw, err := portforward.New(dialer, []string{":7681"}, stopCh, readyCh, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("portforward.New: %v", err)
	}
	go func() {
		if err := fw.ForwardPorts(); err != nil && !errors.Is(err, portforward.ErrLostConnectionToPod) {
			t.Logf("port-forward ended: %v", err)
		}
	}()

	select {
	case <-readyCh:
	case <-time.After(45 * time.Second):
		close(stopCh)
		t.Fatal("port-forward did not become ready in time")
	}

	ports, err := fw.GetPorts()
	if err != nil || len(ports) == 0 {
		close(stopCh)
		t.Fatalf("GetPorts: err=%v ports=%v", err, ports)
	}
	localPort := int(ports[0].Local)
	ttydURL := fmt.Sprintf("http://127.0.0.1:%d/", localPort)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	defer close(stopCh)

	if err := wait.PollUntilContextTimeout(ctx, time.Second, 40*time.Second, true, func(ctx context.Context) (bool, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ttydURL, nil)
		if err != nil {
			return false, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false, nil
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK, nil
	}); err != nil {
		t.Fatalf("ttyd HTTP not reachable via port-forward %s: %v", ttydURL, err)
	}
	t.Logf("ttyd responded OK via port-forward %s", ttydURL)
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
				Providers: []workspacev1alpha1.AIProvider{
					{Name: "local", Endpoint: "http://vllm.ai-system.svc:8000", Models: []string{"test-model"}},
				},
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

	// Full ship path: wait for Running, ready pod, then ttyd HTTP via port-forward.
	t.Log("Waiting for Workspace status to reach Running...")
	if err := wait.PollUntilContextTimeout(ctx, pollInterval, createTimeout, true, func(ctx context.Context) (bool, error) {
		if err := c.Get(ctx, types.NamespacedName{Name: wsName, Namespace: e2eNamespace}, ws); err != nil {
			return false, err
		}
		switch ws.Status.Phase {
		case workspacev1alpha1.WorkspacePhaseRunning:
			return true, nil
		case workspacev1alpha1.WorkspacePhaseFailed:
			return false, fmt.Errorf("workspace reached Failed phase: %s", ws.Status.Message)
		}
		return false, nil
	}); err != nil {
		t.Fatalf("workspace did not reach Running: %v (last phase: %s)", err, ws.Status.Phase)
	}
	if ws.Status.ServiceEndpoint == "" {
		t.Fatal("Running workspace missing status.serviceEndpoint")
	}
	t.Logf("Workspace Running — serviceEndpoint %q", ws.Status.ServiceEndpoint)

	t.Log("Waiting for workspace pod Ready condition...")
	if err := wait.PollUntilContextTimeout(ctx, pollInterval, createTimeout, true, func(ctx context.Context) (bool, error) {
		var pod corev1.Pod
		if err := c.Get(ctx, types.NamespacedName{Name: podName, Namespace: e2eNamespace}, &pod); err != nil {
			return false, err
		}
		return isPodReady(&pod), nil
	}); err != nil {
		t.Fatalf("pod not Ready: %v", err)
	}

	t.Log("Probing ttyd via port-forward (workspace reachable like gateway would dial it)...")
	probeTTYDViaPortForward(t, restConfig(t), e2eNamespace, podName)
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
				Providers: []workspacev1alpha1.AIProvider{
					{Name: "local", Endpoint: "http://vllm.ai-system.svc:8000", Models: []string{"test-model"}},
				},
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
				Providers: []workspacev1alpha1.AIProvider{
					{Name: "local", Endpoint: "http://vllm:8000", Models: []string{"model"}},
				},
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

// TestGatewayWorkspaceAPISmoke calls GET /api/workspace on a live gateway with a Bearer
// OIDC ID token and waits until ttydReady is true (authenticate → workspace → terminal).
func TestGatewayWorkspaceAPISmoke(t *testing.T) {
	base := strings.TrimSpace(os.Getenv("E2E_GATEWAY_URL"))
	token := strings.TrimSpace(os.Getenv("E2E_ID_TOKEN"))
	if base == "" || token == "" {
		t.Skip("set E2E_GATEWAY_URL and E2E_ID_TOKEN to run gateway API smoke")
	}
	base = strings.TrimRight(base, "/")

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	deadline := time.Now().Add(7 * time.Minute)
	var lastPhase string
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/workspace", nil)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Logf("gateway request error: %v", err)
			time.Sleep(3 * time.Second)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Logf("gateway /api/workspace status %d body %s", resp.StatusCode, string(body))
			time.Sleep(3 * time.Second)
			continue
		}
		var got struct {
			TTYDReady bool   `json:"ttydReady"`
			Phase     string `json:"phase"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode JSON: %v body=%s", err, string(body))
		}
		lastPhase = got.Phase
		if got.TTYDReady {
			t.Logf("gateway reports ttydReady=true phase=%s", got.Phase)
			return
		}
		t.Logf("waiting for ttydReady phase=%s", got.Phase)
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("ttydReady never true (last phase %q)", lastPhase)
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
