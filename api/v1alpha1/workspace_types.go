// Package v1alpha1 contains API types for the Workspace API.
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WorkspaceSpec defines the desired state of a Workspace.
type WorkspaceSpec struct {
	// User identifies the workspace owner (from OIDC).
	User UserInfo `json:"user"`
	// Resources defines CPU, memory, and storage for the workspace pod.
	Resources ResourceRequirements `json:"resources"`
	// AIConfig configures the AI coding assistant (vLLM endpoint, model).
	AIConfig AIConfiguration `json:"aiConfig"`
	// Persistence configures storage class for the workspace PVC.
	Persistence PersistenceConfig `json:"persistence"`
}

// UserInfo holds the sanitized user identity from OIDC.
type UserInfo struct {
	// ID is the sanitized username (e.g., "john").
	ID string `json:"id"`
	// Email is the user's email from the OIDC token.
	Email string `json:"email"`
}

// ResourceRequirements defines CPU, memory, and storage requests/limits.
type ResourceRequirements struct {
	// CPU limit (e.g., "2").
	CPU string `json:"cpu"`
	// Memory limit (e.g., "4Gi").
	Memory string `json:"memory"`
	// Storage size for the workspace PVC (e.g., "20Gi").
	Storage string `json:"storage"`
}

// AIConfiguration configures the AI assistant backend.
type AIConfiguration struct {
	// DefaultProvider is the default AI provider name.
	DefaultProvider string `json:"defaultProvider,omitempty"`
	// VLLMEndpoint is the vLLM service URL (e.g., "http://vllm.ai-system.svc:8000").
	VLLMEndpoint string `json:"vllmEndpoint"`
	// VLLMModel is the model name (e.g., "deepseek-coder-33b-instruct").
	VLLMModel string `json:"vllmModel"`
}

// PersistenceConfig configures persistent storage for the workspace.
type PersistenceConfig struct {
	// StorageClass is the name of the StorageClass for the workspace PVC.
	StorageClass string `json:"storageClass,omitempty"`
}

// WorkspaceStatus defines the observed state of a Workspace.
type WorkspaceStatus struct {
	// Phase is the current lifecycle phase: Pending, Creating, Running, Failed, Stopped.
	Phase string `json:"phase,omitempty"`
	// PodName is the name of the workspace pod when running.
	PodName string `json:"podName,omitempty"`
	// ServiceEndpoint is the internal service DNS name for the workspace.
	ServiceEndpoint string `json:"serviceEndpoint,omitempty"`
	// Message is a human-readable error or info (e.g. validation failure, PVC not bound).
	Message string `json:"message,omitempty"`
	// LastAccessed is when the workspace was last accessed by the user.
	LastAccessed metav1.Time `json:"lastAccessed,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:path=workspaces,scope=Namespaced,shortName=ws

// Workspace is the Schema for the workspaces API.
type Workspace struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkspaceSpec   `json:"spec,omitempty"`
	Status WorkspaceStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// WorkspaceList contains a list of Workspace.
type WorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Workspace `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Workspace{}, &WorkspaceList{})
}
