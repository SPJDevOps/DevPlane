package gateway

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	workspacev1alpha1 "workspace-operator/api/v1alpha1"
)

const (
	workspaceReadyTimeout = 60 * time.Second
	workspaceReadyPoll    = 2 * time.Second
)

// LifecycleConfig holds defaults used when creating new Workspace CRs.
type LifecycleConfig struct {
	Providers      []workspacev1alpha1.AIProvider
	DefaultCPU     string
	DefaultMemory  string
	DefaultStorage string
}

// LifecycleManager creates and retrieves Workspace custom resources on behalf
// of authenticated users.
type LifecycleManager struct {
	client client.Client
	log    logr.Logger
	cfg    LifecycleConfig
}

// NewLifecycleManager returns a LifecycleManager using the provided K8s client.
func NewLifecycleManager(c client.Client, log logr.Logger, cfg LifecycleConfig) *LifecycleManager {
	return &LifecycleManager{client: c, log: log, cfg: cfg}
}

// EnsureWorkspace gets or creates a Workspace CR for claims.UserID in namespace,
// then waits up to workspaceReadyTimeout for it to reach the Running phase.
// It also stamps LastAccessed so the idle-timeout controller can track activity.
func (m *LifecycleManager) EnsureWorkspace(ctx context.Context, namespace string, claims *Claims) (*workspacev1alpha1.Workspace, error) {
	key := types.NamespacedName{Name: claims.UserID, Namespace: namespace}

	ws := &workspacev1alpha1.Workspace{}
	err := m.client.Get(ctx, key, ws)
	if err != nil && !errors.IsNotFound(err) {
		return nil, fmt.Errorf("get workspace %q: %w", claims.UserID, err)
	}

	if errors.IsNotFound(err) {
		ws = &workspacev1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name:      claims.UserID,
				Namespace: namespace,
			},
			Spec: workspacev1alpha1.WorkspaceSpec{
				User: workspacev1alpha1.UserInfo{
					ID:    claims.UserID,
					Email: claims.Email,
				},
				Resources: workspacev1alpha1.ResourceRequirements{
					CPU:     m.cfg.DefaultCPU,
					Memory:  m.cfg.DefaultMemory,
					Storage: m.cfg.DefaultStorage,
				},
				AIConfig: workspacev1alpha1.AIConfiguration{
					Providers: m.cfg.Providers,
				},
			},
		}
		m.log.Info("Creating Workspace CR", "user", claims.UserID, "namespace", namespace)
		if err := m.client.Create(ctx, ws); err != nil {
			return nil, fmt.Errorf("create workspace %q: %w", claims.UserID, err)
		}
	}

	ws, err = m.waitForRunning(ctx, key)
	if err != nil {
		return nil, err
	}

	// Stamp LastAccessed so the idle-timeout controller can track inactivity.
	// This is best-effort; a failure here does not prevent the user from connecting.
	patchBase := ws.DeepCopy()
	ws.Status.LastAccessed = metav1.Now()
	if patchErr := m.client.Status().Patch(ctx, ws, client.MergeFrom(patchBase)); patchErr != nil {
		m.log.Error(patchErr, "Failed to update LastAccessed", "workspace", ws.Name)
	}

	return ws, nil
}

// EnsureExists gets or creates the Workspace CR for claims.UserID in namespace
// and returns it immediately without waiting for it to reach Running.
// If the workspace is Stopped it patches the phase to "" to re-trigger operator
// reconciliation, then returns the patched workspace.
// Callers must inspect ws.Status.Phase and ws.Status.ServiceEndpoint.
func (m *LifecycleManager) EnsureExists(ctx context.Context, namespace string, claims *Claims) (*workspacev1alpha1.Workspace, error) {
	key := types.NamespacedName{Name: claims.UserID, Namespace: namespace}

	ws := &workspacev1alpha1.Workspace{}
	err := m.client.Get(ctx, key, ws)
	if err != nil && !errors.IsNotFound(err) {
		return nil, fmt.Errorf("get workspace %q: %w", claims.UserID, err)
	}

	if errors.IsNotFound(err) {
		ws = &workspacev1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name:      claims.UserID,
				Namespace: namespace,
			},
			Spec: workspacev1alpha1.WorkspaceSpec{
				User: workspacev1alpha1.UserInfo{
					ID:    claims.UserID,
					Email: claims.Email,
				},
				Resources: workspacev1alpha1.ResourceRequirements{
					CPU:     m.cfg.DefaultCPU,
					Memory:  m.cfg.DefaultMemory,
					Storage: m.cfg.DefaultStorage,
				},
				AIConfig: workspacev1alpha1.AIConfiguration{
					Providers: m.cfg.Providers,
				},
			},
		}
		m.log.Info("Creating Workspace CR", "user", claims.UserID, "namespace", namespace)
		if err := m.client.Create(ctx, ws); err != nil {
			return nil, fmt.Errorf("create workspace %q: %w", claims.UserID, err)
		}
		return ws, nil
	}

	// If Stopped, clear the phase so the operator reconcile loop recreates the pod.
	if ws.Status.Phase == workspacev1alpha1.WorkspacePhaseStopped {
		m.log.Info("Restarting stopped workspace", "workspace", key.Name)
		patchBase := ws.DeepCopy()
		ws.Status.Phase = ""
		ws.Status.Message = ""
		ws.Status.PodName = ""
		if patchErr := m.client.Status().Patch(ctx, ws, client.MergeFrom(patchBase)); patchErr != nil {
			return nil, fmt.Errorf("restart stopped workspace %q: %w", key.Name, patchErr)
		}
	}

	return ws, nil
}

// waitForRunning polls until the Workspace reaches Running or the deadline passes.
// When the workspace is Stopped it patches the status to clear the phase, allowing
// the operator to recreate the pod, then continues polling.
func (m *LifecycleManager) waitForRunning(ctx context.Context, key types.NamespacedName) (*workspacev1alpha1.Workspace, error) {
	deadline := time.Now().Add(workspaceReadyTimeout)
	for time.Now().Before(deadline) {
		ws := &workspacev1alpha1.Workspace{}
		if err := m.client.Get(ctx, key, ws); err != nil {
			return nil, fmt.Errorf("get workspace %q: %w", key.Name, err)
		}
		switch ws.Status.Phase {
		case workspacev1alpha1.WorkspacePhaseRunning:
			return ws, nil
		case workspacev1alpha1.WorkspacePhaseFailed:
			return nil, fmt.Errorf("workspace %q failed: %s", key.Name, ws.Status.Message)
		case workspacev1alpha1.WorkspacePhaseStopped:
			// Clear the Stopped phase so the operator reconcile loop recreates the pod.
			m.log.Info("Restarting stopped workspace", "workspace", key.Name)
			patchBase := ws.DeepCopy()
			ws.Status.Phase = ""
			ws.Status.Message = ""
			ws.Status.PodName = ""
			if patchErr := m.client.Status().Patch(ctx, ws, client.MergeFrom(patchBase)); patchErr != nil {
				return nil, fmt.Errorf("restart stopped workspace %q: %w", key.Name, patchErr)
			}
		}
		m.log.Info("Waiting for workspace", "workspace", key.Name, "phase", ws.Status.Phase)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(workspaceReadyPoll):
		}
	}
	return nil, fmt.Errorf("workspace %q not ready after %s", key.Name, workspaceReadyTimeout)
}

// TouchLastAccessed stamps the workspace's LastAccessed to now.
// Called on each proxied WebSocket message to keep idle-timeout tracking accurate.
// Updates are best-effort; errors are logged but do not interrupt the session.
func (m *LifecycleManager) TouchLastAccessed(ctx context.Context, ws *workspacev1alpha1.Workspace) {
	patchBase := ws.DeepCopy()
	ws.Status.LastAccessed = metav1.Now()
	if err := m.client.Status().Patch(ctx, ws, client.MergeFrom(patchBase)); err != nil {
		m.log.Error(err, "Failed to update LastAccessed", "workspace", ws.Name)
	}
}
