package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func fullWorkspace() *Workspace {
	return &Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "ws1", Namespace: "default"},
		Spec: WorkspaceSpec{
			User: UserInfo{ID: "alice", Email: "alice@example.com"},
			Resources: ResourceRequirements{
				CPU: "2", Memory: "4Gi", Storage: "20Gi",
			},
			AIConfig: AIConfiguration{
				Providers: []AIProvider{
					{Name: "local", Endpoint: "http://vllm:8000", Models: []string{"model-a", "model-b"}},
					{Name: "cloud", Endpoint: "http://openai:8080", Models: []string{"gpt-4"}},
				},
				EgressNamespaces: []string{"ai-system", "ollama-ns"},
				EgressPorts:      []int32{22, 443, 8000},
			},
			Persistence: PersistenceConfig{StorageClass: "fast-ssd"},
			TLS: TLSConfig{
				CustomCABundle: &CABundleRef{Name: "my-ca-bundle"},
			},
		},
		Status: WorkspaceStatus{
			Phase:           WorkspacePhaseRunning,
			PodName:         "alice-workspace-pod",
			ServiceEndpoint: "alice-workspace-svc.default.svc.cluster.local",
			Message:         "running",
		},
	}
}

func TestDeepCopy_Workspace(t *testing.T) {
	orig := fullWorkspace()
	copy := orig.DeepCopy()

	if copy == nil {
		t.Fatal("DeepCopy returned nil")
	}
	if copy == orig {
		t.Fatal("DeepCopy returned same pointer")
	}
	if copy.Name != orig.Name {
		t.Errorf("Name = %q, want %q", copy.Name, orig.Name)
	}
	if copy.Spec.User.ID != orig.Spec.User.ID {
		t.Errorf("Spec.User.ID = %q, want %q", copy.Spec.User.ID, orig.Spec.User.ID)
	}
	if len(copy.Spec.AIConfig.Providers) != len(orig.Spec.AIConfig.Providers) {
		t.Fatalf("Providers len = %d, want %d", len(copy.Spec.AIConfig.Providers), len(orig.Spec.AIConfig.Providers))
	}
	if copy.Spec.AIConfig.Providers[0].Name != orig.Spec.AIConfig.Providers[0].Name {
		t.Errorf("Providers[0].Name = %q, want %q", copy.Spec.AIConfig.Providers[0].Name, orig.Spec.AIConfig.Providers[0].Name)
	}
	if len(copy.Spec.AIConfig.EgressNamespaces) != len(orig.Spec.AIConfig.EgressNamespaces) {
		t.Errorf("EgressNamespaces len = %d, want %d", len(copy.Spec.AIConfig.EgressNamespaces), len(orig.Spec.AIConfig.EgressNamespaces))
	}
	if len(copy.Spec.AIConfig.EgressPorts) != len(orig.Spec.AIConfig.EgressPorts) {
		t.Errorf("EgressPorts len = %d, want %d", len(copy.Spec.AIConfig.EgressPorts), len(orig.Spec.AIConfig.EgressPorts))
	}
	if copy.Spec.TLS.CustomCABundle == nil {
		t.Fatal("TLS.CustomCABundle is nil after DeepCopy")
	}
	if copy.Spec.TLS.CustomCABundle == orig.Spec.TLS.CustomCABundle {
		t.Error("TLS.CustomCABundle pointer not copied")
	}
	if copy.Spec.TLS.CustomCABundle.Name != orig.Spec.TLS.CustomCABundle.Name {
		t.Errorf("CABundle.Name = %q, want %q", copy.Spec.TLS.CustomCABundle.Name, orig.Spec.TLS.CustomCABundle.Name)
	}
	if copy.Status.Phase != orig.Status.Phase {
		t.Errorf("Status.Phase = %q, want %q", copy.Status.Phase, orig.Status.Phase)
	}

	// Mutations to the copy must not affect the original.
	copy.Spec.AIConfig.Providers[0].Name = "mutated"
	if orig.Spec.AIConfig.Providers[0].Name == "mutated" {
		t.Error("mutating copy.Providers[0] affected orig")
	}
	copy.Spec.AIConfig.EgressNamespaces[0] = "mutated"
	if orig.Spec.AIConfig.EgressNamespaces[0] == "mutated" {
		t.Error("mutating copy.EgressNamespaces[0] affected orig")
	}
}

func TestDeepCopy_Workspace_Nil(t *testing.T) {
	var ws *Workspace
	if ws.DeepCopy() != nil {
		t.Error("nil Workspace.DeepCopy() should return nil")
	}
}

func TestDeepCopyObject_Workspace(t *testing.T) {
	orig := fullWorkspace()
	obj := orig.DeepCopyObject()
	if obj == nil {
		t.Fatal("DeepCopyObject returned nil")
	}
	ws, ok := obj.(*Workspace)
	if !ok {
		t.Fatalf("DeepCopyObject returned %T, want *Workspace", obj)
	}
	if ws.Name != orig.Name {
		t.Errorf("Name = %q, want %q", ws.Name, orig.Name)
	}
}

func TestDeepCopyObject_Workspace_NilReturn(t *testing.T) {
	var ws *Workspace
	// DeepCopyObject on nil calls DeepCopy which returns nil, then returns nil.
	obj := ws.DeepCopyObject()
	if obj != nil {
		t.Errorf("nil Workspace.DeepCopyObject() should return nil, got %v", obj)
	}
}

func TestDeepCopy_WorkspaceList(t *testing.T) {
	orig := &WorkspaceList{
		Items: []Workspace{*fullWorkspace(), *fullWorkspace()},
	}
	copy := orig.DeepCopy()
	if copy == nil {
		t.Fatal("DeepCopy returned nil")
	}
	if len(copy.Items) != len(orig.Items) {
		t.Fatalf("Items len = %d, want %d", len(copy.Items), len(orig.Items))
	}
	copy.Items[0].Name = "mutated"
	if orig.Items[0].Name == "mutated" {
		t.Error("mutating copy.Items[0] affected orig")
	}
}

func TestDeepCopy_WorkspaceList_Nil(t *testing.T) {
	var l *WorkspaceList
	if l.DeepCopy() != nil {
		t.Error("nil WorkspaceList.DeepCopy() should return nil")
	}
}

func TestDeepCopyObject_WorkspaceList(t *testing.T) {
	orig := &WorkspaceList{Items: []Workspace{*fullWorkspace()}}
	obj := orig.DeepCopyObject()
	if obj == nil {
		t.Fatal("DeepCopyObject returned nil")
	}
	l, ok := obj.(*WorkspaceList)
	if !ok {
		t.Fatalf("DeepCopyObject returned %T, want *WorkspaceList", obj)
	}
	if len(l.Items) != 1 {
		t.Errorf("Items len = %d, want 1", len(l.Items))
	}
}

func TestDeepCopyObject_WorkspaceList_NilReturn(t *testing.T) {
	var l *WorkspaceList
	obj := l.DeepCopyObject()
	if obj != nil {
		t.Errorf("nil WorkspaceList.DeepCopyObject() should return nil, got %v", obj)
	}
}

func TestDeepCopy_AIConfiguration(t *testing.T) {
	orig := &AIConfiguration{
		Providers: []AIProvider{
			{Name: "local", Endpoint: "http://vllm:8000", Models: []string{"m1", "m2"}},
		},
		EgressNamespaces: []string{"ns1", "ns2"},
		EgressPorts:      []int32{80, 443},
	}
	copy := orig.DeepCopy()
	if copy == nil {
		t.Fatal("DeepCopy returned nil")
	}
	copy.Providers[0].Name = "mutated"
	if orig.Providers[0].Name == "mutated" {
		t.Error("mutating copy.Providers[0] affected orig")
	}
	copy.EgressNamespaces[0] = "mutated"
	if orig.EgressNamespaces[0] == "mutated" {
		t.Error("mutating copy.EgressNamespaces[0] affected orig")
	}
	copy.EgressPorts[0] = 9999
	if orig.EgressPorts[0] == 9999 {
		t.Error("mutating copy.EgressPorts[0] affected orig")
	}
}

func TestDeepCopy_AIConfiguration_Nil(t *testing.T) {
	var c *AIConfiguration
	if c.DeepCopy() != nil {
		t.Error("nil AIConfiguration.DeepCopy() should return nil")
	}
}

func TestDeepCopy_AIConfiguration_EmptySlices(t *testing.T) {
	orig := &AIConfiguration{} // nil slices
	copy := orig.DeepCopy()
	if copy == nil {
		t.Fatal("DeepCopy returned nil")
	}
	if copy.Providers != nil {
		t.Errorf("expected nil Providers, got %v", copy.Providers)
	}
}

func TestDeepCopy_AIProvider(t *testing.T) {
	orig := &AIProvider{
		Name:     "local",
		Endpoint: "http://vllm:8000",
		Models:   []string{"model-a", "model-b"},
	}
	copy := orig.DeepCopy()
	if copy == nil {
		t.Fatal("DeepCopy returned nil")
	}
	copy.Models[0] = "mutated"
	if orig.Models[0] == "mutated" {
		t.Error("mutating copy.Models[0] affected orig")
	}
}

func TestDeepCopy_AIProvider_Nil(t *testing.T) {
	var p *AIProvider
	if p.DeepCopy() != nil {
		t.Error("nil AIProvider.DeepCopy() should return nil")
	}
}

func TestDeepCopy_AIProvider_NilModels(t *testing.T) {
	orig := &AIProvider{Name: "local", Endpoint: "http://x"}
	copy := orig.DeepCopy()
	if copy == nil {
		t.Fatal("DeepCopy returned nil")
	}
	if copy.Models != nil {
		t.Errorf("expected nil Models, got %v", copy.Models)
	}
}

func TestDeepCopy_CABundleRef(t *testing.T) {
	orig := &CABundleRef{Name: "my-ca"}
	copy := orig.DeepCopy()
	if copy == nil {
		t.Fatal("DeepCopy returned nil")
	}
	if copy.Name != "my-ca" {
		t.Errorf("Name = %q, want my-ca", copy.Name)
	}
}

func TestDeepCopy_CABundleRef_Nil(t *testing.T) {
	var c *CABundleRef
	if c.DeepCopy() != nil {
		t.Error("nil CABundleRef.DeepCopy() should return nil")
	}
}

func TestDeepCopy_PersistenceConfig(t *testing.T) {
	orig := &PersistenceConfig{StorageClass: "fast-ssd"}
	copy := orig.DeepCopy()
	if copy == nil {
		t.Fatal("DeepCopy returned nil")
	}
	if copy.StorageClass != "fast-ssd" {
		t.Errorf("StorageClass = %q, want fast-ssd", copy.StorageClass)
	}
}

func TestDeepCopy_PersistenceConfig_Nil(t *testing.T) {
	var p *PersistenceConfig
	if p.DeepCopy() != nil {
		t.Error("nil PersistenceConfig.DeepCopy() should return nil")
	}
}

func TestDeepCopy_ResourceRequirements(t *testing.T) {
	orig := &ResourceRequirements{CPU: "2", Memory: "4Gi", Storage: "20Gi"}
	copy := orig.DeepCopy()
	if copy == nil {
		t.Fatal("DeepCopy returned nil")
	}
	if copy.CPU != "2" || copy.Memory != "4Gi" || copy.Storage != "20Gi" {
		t.Errorf("ResourceRequirements = %+v", *copy)
	}
}

func TestDeepCopy_ResourceRequirements_Nil(t *testing.T) {
	var r *ResourceRequirements
	if r.DeepCopy() != nil {
		t.Error("nil ResourceRequirements.DeepCopy() should return nil")
	}
}

func TestDeepCopy_TLSConfig(t *testing.T) {
	orig := &TLSConfig{CustomCABundle: &CABundleRef{Name: "ca"}}
	copy := orig.DeepCopy()
	if copy == nil {
		t.Fatal("DeepCopy returned nil")
	}
	if copy.CustomCABundle == nil {
		t.Fatal("CustomCABundle is nil after DeepCopy")
	}
	if copy.CustomCABundle == orig.CustomCABundle {
		t.Error("CustomCABundle pointer not deep-copied")
	}
	if copy.CustomCABundle.Name != "ca" {
		t.Errorf("Name = %q, want ca", copy.CustomCABundle.Name)
	}
}

func TestDeepCopy_TLSConfig_NilBundle(t *testing.T) {
	orig := &TLSConfig{} // nil CustomCABundle
	copy := orig.DeepCopy()
	if copy == nil {
		t.Fatal("DeepCopy returned nil")
	}
	if copy.CustomCABundle != nil {
		t.Error("expected nil CustomCABundle")
	}
}

func TestDeepCopy_TLSConfig_Nil(t *testing.T) {
	var t2 *TLSConfig
	if t2.DeepCopy() != nil {
		t.Error("nil TLSConfig.DeepCopy() should return nil")
	}
}

func TestDeepCopy_UserInfo(t *testing.T) {
	orig := &UserInfo{ID: "alice", Email: "alice@example.com"}
	copy := orig.DeepCopy()
	if copy == nil {
		t.Fatal("DeepCopy returned nil")
	}
	if copy.ID != "alice" || copy.Email != "alice@example.com" {
		t.Errorf("UserInfo = %+v", *copy)
	}
}

func TestDeepCopy_UserInfo_Nil(t *testing.T) {
	var u *UserInfo
	if u.DeepCopy() != nil {
		t.Error("nil UserInfo.DeepCopy() should return nil")
	}
}

func TestDeepCopy_WorkspaceSpec(t *testing.T) {
	orig := fullWorkspace().Spec.DeepCopy()
	if orig == nil {
		t.Fatal("DeepCopy returned nil")
	}
	if orig.User.ID != "alice" {
		t.Errorf("User.ID = %q, want alice", orig.User.ID)
	}
}

func TestDeepCopy_WorkspaceSpec_Nil(t *testing.T) {
	var s *WorkspaceSpec
	if s.DeepCopy() != nil {
		t.Error("nil WorkspaceSpec.DeepCopy() should return nil")
	}
}

func TestDeepCopy_WorkspaceStatus(t *testing.T) {
	orig := &WorkspaceStatus{
		Phase:   WorkspacePhaseRunning,
		PodName: "pod-1",
	}
	copy := orig.DeepCopy()
	if copy == nil {
		t.Fatal("DeepCopy returned nil")
	}
	if copy.Phase != WorkspacePhaseRunning {
		t.Errorf("Phase = %q, want Running", copy.Phase)
	}
}

func TestDeepCopy_WorkspaceStatus_Nil(t *testing.T) {
	var s *WorkspaceStatus
	if s.DeepCopy() != nil {
		t.Error("nil WorkspaceStatus.DeepCopy() should return nil")
	}
}
