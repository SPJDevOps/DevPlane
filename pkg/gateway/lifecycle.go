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
	LLMEndpoint    string
	LLMModel       string
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
					Endpoint: m.cfg.LLMEndpoint,
					Model:    m.cfg.LLMModel,
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

// waitForRunning polls until the Workspace reaches Running or the deadline passes.
// It also handles the Stopped phase by returning an error immediately rather than
// waiting out the full timeout.
func (m *LifecycleManager) waitForRunning(ctx context.Context, key types.NamespacedName) (*workspacev1alpha1.Workspace, error) {
	deadline := time.Now().Add(workspaceReadyTimeout)
	for time.Now().Before(deadline) {
		ws := &workspacev1alpha1.Workspace{}
		if err := m.client.Get(ctx, key, ws); err != nil {
			return nil, fmt.Errorf("get workspace %q: %w", key.Name, err)
		}
		switch ws.Status.Phase {
		case "Running":
			return ws, nil
		case "Failed":
			return nil, fmt.Errorf("workspace %q failed: %s", key.Name, ws.Status.Message)
		case "Stopped":
			return nil, fmt.Errorf("workspace %q is stopped", key.Name)
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
