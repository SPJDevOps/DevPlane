package workspace

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	workspacev1alpha1 "workspace-operator/api/v1alpha1"
)

var scheme = func() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(workspacev1alpha1.AddToScheme(s))
	return s
}()

func TestPVCName(t *testing.T) {
	if got := PVCName("john"); got != "john-workspace-pvc" {
		t.Errorf("PVCName(%q) = %q, want john-workspace-pvc", "john", got)
	}
}

func TestPodName(t *testing.T) {
	if got := PodName("john"); got != "john-workspace-pod" {
		t.Errorf("PodName(%q) = %q, want john-workspace-pod", "john", got)
	}
}

func TestServiceName(t *testing.T) {
	if got := ServiceName("john"); got != "john-workspace-svc" {
		t.Errorf("ServiceName(%q) = %q, want john-workspace-svc", "john", got)
	}
}

func TestLabels(t *testing.T) {
	got := Labels("alice")
	want := map[string]string{
		"app":        "workspace",
		"user":       "alice",
		"managed-by": "devplane",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("Labels(%q)[%q] = %q, want %q", "alice", k, got[k], v)
		}
	}
}

func minimalWorkspace() *workspacev1alpha1.Workspace {
	return &workspacev1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "ws1", Namespace: "default"},
		Spec: workspacev1alpha1.WorkspaceSpec{
			User: workspacev1alpha1.UserInfo{ID: "john", Email: "john@example.com"},
			Resources: workspacev1alpha1.ResourceRequirements{
				CPU: "1", Memory: "2Gi", Storage: "20Gi",
			},
			AIConfig: workspacev1alpha1.AIConfiguration{
				VLLMEndpoint: "http://vllm:8000",
				VLLMModel:    "model",
			},
			Persistence: workspacev1alpha1.PersistenceConfig{StorageClass: "standard"},
		},
	}
}

func TestBuildPVC(t *testing.T) {
	ws := minimalWorkspace()
	pvc, err := BuildPVC(ws, scheme)
	if err != nil {
		t.Fatalf("BuildPVC: %v", err)
	}
	if pvc.Name != "john-workspace-pvc" {
		t.Errorf("pvc.Name = %q, want john-workspace-pvc", pvc.Name)
	}
	if pvc.Namespace != "default" {
		t.Errorf("pvc.Namespace = %q, want default", pvc.Namespace)
	}
	if len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Errorf("AccessModes = %v, want [ReadWriteOnce]", pvc.Spec.AccessModes)
	}
	storage := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if storage.String() != "20Gi" {
		t.Errorf("storage request = %s, want 20Gi", storage.String())
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "standard" {
		t.Errorf("StorageClassName = %v, want standard", pvc.Spec.StorageClassName)
	}
	if len(pvc.OwnerReferences) != 1 {
		t.Fatalf("expected one owner reference, got %d", len(pvc.OwnerReferences))
	}
	ref := pvc.OwnerReferences[0]
	if ref.Kind != "Workspace" || ref.Name != "ws1" {
		t.Errorf("owner ref: Kind=%s Name=%s, want Workspace ws1", ref.Kind, ref.Name)
	}
}

func TestBuildPVC_EmptyStorageClass(t *testing.T) {
	ws := minimalWorkspace()
	ws.Spec.Persistence.StorageClass = ""
	pvc, err := BuildPVC(ws, scheme)
	if err != nil {
		t.Fatalf("BuildPVC: %v", err)
	}
	if pvc.Spec.StorageClassName != nil {
		t.Errorf("StorageClassName should be nil when empty, got %v", pvc.Spec.StorageClassName)
	}
}

func TestBuildPod(t *testing.T) {
	ws := minimalWorkspace()
	pod, err := BuildPod(ws, "john-workspace-pvc", "workspace:0.0.1", scheme)
	if err != nil {
		t.Fatalf("BuildPod: %v", err)
	}
	if pod.Name != "john-workspace-pod" {
		t.Errorf("pod.Name = %q, want john-workspace-pod", pod.Name)
	}
	psc := pod.Spec.SecurityContext
	if psc == nil {
		t.Fatal("PodSecurityContext is nil")
	}
	if psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot {
		t.Error("PodSecurityContext.RunAsNonRoot expected true")
	}
	if psc.RunAsUser == nil || *psc.RunAsUser != 1000 {
		t.Error("PodSecurityContext.RunAsUser expected 1000")
	}
	if len(pod.Spec.Containers) != 1 {
		t.Fatalf("expected one container, got %d", len(pod.Spec.Containers))
	}
	c := &pod.Spec.Containers[0]
	if c.Image != "workspace:0.0.1" {
		t.Errorf("container image = %q, want workspace:0.0.1", c.Image)
	}
	if c.SecurityContext == nil {
		t.Fatal("container SecurityContext is nil")
	}
	if c.SecurityContext.ReadOnlyRootFilesystem == nil || !*c.SecurityContext.ReadOnlyRootFilesystem {
		t.Error("container SecurityContext.ReadOnlyRootFilesystem expected true")
	}
	if c.SecurityContext.AllowPrivilegeEscalation == nil || *c.SecurityContext.AllowPrivilegeEscalation {
		t.Error("container SecurityContext.AllowPrivilegeEscalation expected false")
	}
	foundWorkspaceMount := false
	for _, vm := range c.VolumeMounts {
		if vm.MountPath == workspaceMount {
			foundWorkspaceMount = true
		}
	}
	if !foundWorkspaceMount {
		t.Errorf("VolumeMounts = %v, want a mount at %s", c.VolumeMounts, workspaceMount)
	}
	envMap := make(map[string]string)
	for _, e := range c.Env {
		envMap[e.Name] = e.Value
	}
	for _, name := range []string{"VLLM_ENDPOINT", "VLLM_MODEL", "USER_EMAIL", "USER_ID"} {
		if _, ok := envMap[name]; !ok {
			t.Errorf("missing env %q", name)
		}
	}
	if envMap["USER_ID"] != "john" || envMap["USER_EMAIL"] != "john@example.com" {
		t.Errorf("USER_ID=%q USER_EMAIL=%q", envMap["USER_ID"], envMap["USER_EMAIL"])
	}
	if len(pod.OwnerReferences) != 1 || pod.OwnerReferences[0].Kind != "Workspace" {
		t.Errorf("expected Workspace owner reference, got %v", pod.OwnerReferences)
	}
}

func TestBuildHeadlessService(t *testing.T) {
	ws := minimalWorkspace()
	svc, err := BuildHeadlessService(ws, scheme)
	if err != nil {
		t.Fatalf("BuildHeadlessService: %v", err)
	}
	if svc.Name != "john-workspace-svc" {
		t.Errorf("svc.Name = %q, want john-workspace-svc", svc.Name)
	}
	if svc.Spec.ClusterIP != corev1.ClusterIPNone {
		t.Errorf("ClusterIP = %q, want %q", svc.Spec.ClusterIP, corev1.ClusterIPNone)
	}
	if len(svc.Spec.Ports) != 1 {
		t.Fatalf("Ports len = %d, want 1", len(svc.Spec.Ports))
	}
	p := svc.Spec.Ports[0]
	if p.Port != ttydPort {
		t.Errorf("Port = %d, want %d", p.Port, ttydPort)
	}
	if p.Protocol != corev1.ProtocolTCP {
		t.Errorf("Protocol = %q, want TCP", p.Protocol)
	}
	if svc.Spec.Selector["user"] != "john" || svc.Spec.Selector["app"] != "workspace" {
		t.Errorf("Selector = %v", svc.Spec.Selector)
	}
	if len(svc.OwnerReferences) != 1 || svc.OwnerReferences[0].Kind != "Workspace" {
		t.Errorf("expected Workspace owner reference, got %v", svc.OwnerReferences)
	}
}

func TestValidateSpec(t *testing.T) {
	valid := minimalWorkspace()
	if err := ValidateSpec(valid); err != nil {
		t.Errorf("ValidateSpec(valid) = %v", err)
	}
}

func TestValidateSpec_MissingUserID(t *testing.T) {
	ws := minimalWorkspace()
	ws.Spec.User.ID = ""
	if err := ValidateSpec(ws); err == nil {
		t.Error("ValidateSpec: expected error for missing user.id")
	}
}

func TestValidateSpec_MissingUserEmail(t *testing.T) {
	ws := minimalWorkspace()
	ws.Spec.User.Email = ""
	if err := ValidateSpec(ws); err == nil {
		t.Error("ValidateSpec: expected error for missing user.email")
	}
}

func TestValidateSpec_MissingStorage(t *testing.T) {
	ws := minimalWorkspace()
	ws.Spec.Resources.Storage = ""
	if err := ValidateSpec(ws); err == nil {
		t.Error("ValidateSpec: expected error for missing resources.storage")
	}
}

func TestValidateSpec_MissingVLLMEndpoint(t *testing.T) {
	ws := minimalWorkspace()
	ws.Spec.AIConfig.VLLMEndpoint = ""
	if err := ValidateSpec(ws); err == nil {
		t.Error("ValidateSpec: expected error for missing aiConfig.vllmEndpoint")
	}
}

func TestValidateSpec_NilWorkspace(t *testing.T) {
	if err := ValidateSpec(nil); err == nil {
		t.Error("ValidateSpec(nil): expected error")
	}
}

func TestBuildPod_SecurityContext(t *testing.T) {
	ws := minimalWorkspace()
	pod, err := BuildPod(ws, "john-workspace-pvc", "workspace:0.0.1", scheme)
	if err != nil {
		t.Fatalf("BuildPod: %v", err)
	}
	if len(pod.Spec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(pod.Spec.Containers))
	}
	psc := pod.Spec.SecurityContext
	c := &pod.Spec.Containers[0]
	csc := c.SecurityContext

	t.Run("RunAsNonRoot", func(t *testing.T) {
		if psc == nil || psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot {
			t.Error("PodSecurityContext.RunAsNonRoot must be true")
		}
	})

	t.Run("RunAsUser1000", func(t *testing.T) {
		if psc == nil || psc.RunAsUser == nil || *psc.RunAsUser != 1000 {
			t.Errorf("PodSecurityContext.RunAsUser = %v, want 1000", psc.RunAsUser)
		}
	})

	t.Run("SeccompRuntimeDefault", func(t *testing.T) {
		if psc == nil || psc.SeccompProfile == nil {
			t.Fatal("PodSecurityContext.SeccompProfile is nil")
		}
		if psc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
			t.Errorf("SeccompProfile.Type = %q, want RuntimeDefault", psc.SeccompProfile.Type)
		}
	})

	t.Run("ReadOnlyRootFilesystem", func(t *testing.T) {
		if csc == nil || csc.ReadOnlyRootFilesystem == nil || !*csc.ReadOnlyRootFilesystem {
			t.Error("container SecurityContext.ReadOnlyRootFilesystem must be true")
		}
	})

	t.Run("NoPrivilegeEscalation", func(t *testing.T) {
		if csc == nil || csc.AllowPrivilegeEscalation == nil || *csc.AllowPrivilegeEscalation {
			t.Error("container SecurityContext.AllowPrivilegeEscalation must be false")
		}
	})

	t.Run("CapabilitiesDropAll", func(t *testing.T) {
		if csc == nil || csc.Capabilities == nil {
			t.Fatal("container SecurityContext.Capabilities is nil")
		}
		if len(csc.Capabilities.Drop) != 1 || csc.Capabilities.Drop[0] != "ALL" {
			t.Errorf("Capabilities.Drop = %v, want [ALL]", csc.Capabilities.Drop)
		}
	})

	t.Run("TmpVolumeMount", func(t *testing.T) {
		for _, vm := range c.VolumeMounts {
			if vm.MountPath == "/tmp" {
				return
			}
		}
		t.Error("container must have a /tmp VolumeMount for readOnlyRootFilesystem")
	})

	t.Run("TmpEmptyDirVolume", func(t *testing.T) {
		for _, v := range pod.Spec.Volumes {
			if v.Name == "tmp" && v.EmptyDir != nil {
				return
			}
		}
		t.Error("pod must have a 'tmp' emptyDir Volume backing the /tmp mount")
	})

	t.Run("TtydContainerPort", func(t *testing.T) {
		for _, p := range c.Ports {
			if p.Name == "ttyd" && p.ContainerPort == ttydPort && p.Protocol == corev1.ProtocolTCP {
				return
			}
		}
		t.Errorf("container must declare port name=ttyd containerPort=%d protocol=TCP", ttydPort)
	})
}
