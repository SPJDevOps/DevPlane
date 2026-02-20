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
	// AIConfig configures the AI coding assistant (OpenAI-compatible LLM endpoint).
	AIConfig AIConfiguration `json:"aiConfig"`
	// Persistence configures storage class for the workspace PVC.
	Persistence PersistenceConfig `json:"persistence"`
	// TLS configures custom TLS certificate trust for the workspace.
	// +optional
	TLS TLSConfig `json:"tls,omitempty"`
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

// AIProvider configures a single AI provider backend.
// The endpoint must be OpenAI API-compatible (vLLM, Ollama, OpenWebUI, etc.).
type AIProvider struct {
	// Name is the provider key used in the opencode configuration (e.g., "local", "cloud").
	// Must be a non-empty identifier unique within the providers list.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// Endpoint is the base URL of the OpenAI-compatible LLM service
	// (e.g., "http://vllm.ai-system.svc:8000", "http://ollama.ai-system.svc:11434").
	// +kubebuilder:validation:MinLength=1
	Endpoint string `json:"endpoint"`
	// Models lists one or more model identifiers served by this provider.
	// +kubebuilder:validation:MinItems=1
	Models []string `json:"models"`
}

// AIConfiguration configures the AI assistant backend.
type AIConfiguration struct {
	// Providers is the list of AI provider backends available to this workspace.
	// At least one provider must be specified.
	// +kubebuilder:validation:MinItems=1
	Providers []AIProvider `json:"providers"`
	// EgressNamespaces lists Kubernetes namespaces where LLM services run.
	// NetworkPolicy egress rules allow traffic to all pods in these namespaces.
	// +optional
	EgressNamespaces []string `json:"egressNamespaces,omitempty"`
	// EgressPorts lists TCP ports allowed for egress to external IPs (0.0.0.0/0).
	// Use this to allow git over SSH (22), package registries (5000, 8080, 8081),
	// bare-metal LLM endpoints (8000, 11434), and any other non-standard ports.
	// If empty, the operator default or built-in default list is used.
	// +optional
	EgressPorts []int32 `json:"egressPorts,omitempty"`
}

// TLSConfig configures custom TLS certificate trust for the workspace.
type TLSConfig struct {
	// CustomCABundle references a ConfigMap containing CA certificates.
	// All keys will be mounted into the pod's trust store.
	// +optional
	CustomCABundle *CABundleRef `json:"customCABundle,omitempty"`
}

// CABundleRef references a ConfigMap containing CA certificates.
type CABundleRef struct {
	// Name of the ConfigMap containing CA certificates.
	Name string `json:"name"`
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
