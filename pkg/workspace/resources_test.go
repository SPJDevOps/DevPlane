package workspace

import (
	"encoding/json"
	"strings"
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

func TestServiceAccountName(t *testing.T) {
	if got := ServiceAccountName("john"); got != "john-workspace" {
		t.Errorf("ServiceAccountName(%q) = %q, want john-workspace", "john", got)
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
				Providers: []workspacev1alpha1.AIProvider{
					{Name: "local", Endpoint: "http://vllm:8000", Models: []string{"model"}},
				},
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
	for _, name := range []string{"AI_PROVIDERS_JSON", "USER_EMAIL", "USER_ID"} {
		if _, ok := envMap[name]; !ok {
			t.Errorf("missing env %q", name)
		}
	}
	if envMap["USER_ID"] != "john" || envMap["USER_EMAIL"] != "john@example.com" {
		t.Errorf("USER_ID=%q USER_EMAIL=%q", envMap["USER_ID"], envMap["USER_EMAIL"])
	}
	// Verify AI_PROVIDERS_JSON round-trips correctly.
	var providers []workspacev1alpha1.AIProvider
	if err := json.Unmarshal([]byte(envMap["AI_PROVIDERS_JSON"]), &providers); err != nil {
		t.Fatalf("AI_PROVIDERS_JSON is not valid JSON: %v", err)
	}
	if len(providers) != 1 || providers[0].Name != "local" || providers[0].Endpoint != "http://vllm:8000" {
		t.Errorf("AI_PROVIDERS_JSON providers = %+v", providers)
	}
	if len(providers[0].Models) != 1 || providers[0].Models[0] != "model" {
		t.Errorf("AI_PROVIDERS_JSON models = %v", providers[0].Models)
	}
	if len(pod.OwnerReferences) != 1 || pod.OwnerReferences[0].Kind != "Workspace" {
		t.Errorf("expected Workspace owner reference, got %v", pod.OwnerReferences)
	}
	if pod.Spec.ServiceAccountName != "john-workspace" {
		t.Errorf("pod.Spec.ServiceAccountName = %q, want john-workspace", pod.Spec.ServiceAccountName)
	}
}

func TestBuildPod_WithCABundle(t *testing.T) {
	ws := minimalWorkspace()
	ws.Spec.TLS.CustomCABundle = &workspacev1alpha1.CABundleRef{Name: "my-ca-bundle"}
	pod, err := BuildPod(ws, "john-workspace-pvc", "workspace:0.0.1", scheme)
	if err != nil {
		t.Fatalf("BuildPod: %v", err)
	}

	// Check volume exists
	foundVolume := false
	for _, v := range pod.Spec.Volumes {
		if v.Name == "custom-ca-certs" {
			foundVolume = true
			if v.ConfigMap == nil || v.ConfigMap.Name != "my-ca-bundle" {
				t.Errorf("custom-ca-certs volume ConfigMap = %v, want my-ca-bundle", v.ConfigMap)
			}
		}
	}
	if !foundVolume {
		t.Error("expected custom-ca-certs volume")
	}

	// Check volume mount exists
	c := &pod.Spec.Containers[0]
	foundMount := false
	for _, vm := range c.VolumeMounts {
		if vm.Name == "custom-ca-certs" {
			foundMount = true
			if vm.MountPath != "/etc/ssl/certs/custom" {
				t.Errorf("mountPath = %q, want /etc/ssl/certs/custom", vm.MountPath)
			}
			if !vm.ReadOnly {
				t.Error("custom-ca-certs mount should be read-only")
			}
		}
	}
	if !foundMount {
		t.Error("expected custom-ca-certs volume mount")
	}

	// Check CUSTOM_CA_MOUNTED env var
	envMap := make(map[string]string)
	for _, e := range c.Env {
		envMap[e.Name] = e.Value
	}
	if envMap["CUSTOM_CA_MOUNTED"] != "true" {
		t.Errorf("CUSTOM_CA_MOUNTED = %q, want true", envMap["CUSTOM_CA_MOUNTED"])
	}
}

func TestBuildPod_WithoutCABundle(t *testing.T) {
	ws := minimalWorkspace()
	pod, err := BuildPod(ws, "john-workspace-pvc", "workspace:0.0.1", scheme)
	if err != nil {
		t.Fatalf("BuildPod: %v", err)
	}

	for _, v := range pod.Spec.Volumes {
		if v.Name == "custom-ca-certs" {
			t.Error("custom-ca-certs volume should not exist without TLS config")
		}
	}

	c := &pod.Spec.Containers[0]
	for _, vm := range c.VolumeMounts {
		if vm.Name == "custom-ca-certs" {
			t.Error("custom-ca-certs mount should not exist without TLS config")
		}
	}
	for _, e := range c.Env {
		if e.Name == "CUSTOM_CA_MOUNTED" {
			t.Error("CUSTOM_CA_MOUNTED env should not exist without TLS config")
		}
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

func TestValidateSpec_EmptyProviders(t *testing.T) {
	ws := minimalWorkspace()
	ws.Spec.AIConfig.Providers = nil
	if err := ValidateSpec(ws); err == nil {
		t.Error("ValidateSpec: expected error for empty aiConfig.providers")
	}
}

func TestValidateSpec_ProviderMissingName(t *testing.T) {
	ws := minimalWorkspace()
	ws.Spec.AIConfig.Providers = []workspacev1alpha1.AIProvider{
		{Name: "", Endpoint: "http://vllm:8000", Models: []string{"model"}},
	}
	if err := ValidateSpec(ws); err == nil {
		t.Error("ValidateSpec: expected error for provider with empty name")
	}
}

func TestValidateSpec_ProviderMissingEndpoint(t *testing.T) {
	ws := minimalWorkspace()
	ws.Spec.AIConfig.Providers = []workspacev1alpha1.AIProvider{
		{Name: "local", Endpoint: "", Models: []string{"model"}},
	}
	if err := ValidateSpec(ws); err == nil {
		t.Error("ValidateSpec: expected error for provider with empty endpoint")
	}
}

func TestValidateSpec_ProviderMissingModels(t *testing.T) {
	ws := minimalWorkspace()
	ws.Spec.AIConfig.Providers = []workspacev1alpha1.AIProvider{
		{Name: "local", Endpoint: "http://vllm:8000", Models: nil},
	}
	if err := ValidateSpec(ws); err == nil {
		t.Error("ValidateSpec: expected error for provider with no models")
	}
}

func TestValidateSpec_NilWorkspace(t *testing.T) {
	if err := ValidateSpec(nil); err == nil {
		t.Error("ValidateSpec(nil): expected error")
	}
}

func TestValidateSpec_UserIDTooLong(t *testing.T) {
	ws := minimalWorkspace()
	ws.Spec.User.ID = strings.Repeat("a", 50) // 50 > 49-char limit
	if err := ValidateSpec(ws); err == nil {
		t.Error("ValidateSpec: expected error for user.id > 49 chars")
	}
}

func TestValidateSpec_UserIDAtMaxLength(t *testing.T) {
	ws := minimalWorkspace()
	ws.Spec.User.ID = strings.Repeat("a", 49) // exactly at the 49-char limit
	if err := ValidateSpec(ws); err != nil {
		t.Errorf("ValidateSpec: unexpected error for 49-char user.id: %v", err)
	}
}

func TestValidateSpec_InvalidDNSLabel(t *testing.T) {
	ws := minimalWorkspace()
	// Capital letters are not valid in a DNS label.
	ws.Spec.User.ID = "John"
	if err := ValidateSpec(ws); err == nil {
		t.Error("ValidateSpec: expected error for user.id with capital letters")
	}
}

func TestValidateSpec_InvalidDNSLabel_Hyphen(t *testing.T) {
	ws := minimalWorkspace()
	// Labels must not start with a hyphen.
	ws.Spec.User.ID = "-john"
	if err := ValidateSpec(ws); err == nil {
		t.Error("ValidateSpec: expected error for user.id starting with hyphen")
	}
}

func TestValidateSpec_InvalidDNSLabel_DigitFirst(t *testing.T) {
	ws := minimalWorkspace()
	// RFC 1035: Service names must start with a letter; a digit-first ID must be
	// rejected so the caller (gateway) can apply the "u-" prefix before creating
	// the Workspace CR.
	ws.Spec.User.ID = "12345678-abcd-efef-1234-abcdefabcdef"
	if err := ValidateSpec(ws); err == nil {
		t.Error("ValidateSpec: expected error for user.id starting with a digit")
	}
}

func TestValidateSpec_MissingCPU(t *testing.T) {
	ws := minimalWorkspace()
	ws.Spec.Resources.CPU = ""
	if err := ValidateSpec(ws); err == nil {
		t.Error("ValidateSpec: expected error for missing resources.cpu")
	}
}

func TestValidateSpec_MissingMemory(t *testing.T) {
	ws := minimalWorkspace()
	ws.Spec.Resources.Memory = ""
	if err := ValidateSpec(ws); err == nil {
		t.Error("ValidateSpec: expected error for missing resources.memory")
	}
}

func TestValidateSpec_InvalidCPUQuantity(t *testing.T) {
	ws := minimalWorkspace()
	ws.Spec.Resources.CPU = "not-a-quantity"
	if err := ValidateSpec(ws); err == nil {
		t.Error("ValidateSpec: expected error for invalid CPU quantity")
	}
}

func TestValidateSpec_InvalidMemoryQuantity(t *testing.T) {
	ws := minimalWorkspace()
	ws.Spec.Resources.Memory = "not-a-quantity"
	if err := ValidateSpec(ws); err == nil {
		t.Error("ValidateSpec: expected error for invalid memory quantity")
	}
}

func TestValidateSpec_InvalidStorageQuantity(t *testing.T) {
	ws := minimalWorkspace()
	ws.Spec.Resources.Storage = "not-a-quantity"
	if err := ValidateSpec(ws); err == nil {
		t.Error("ValidateSpec: expected error for invalid storage quantity")
	}
}

func TestBuildPVC_InvalidStorageQuantity(t *testing.T) {
	ws := minimalWorkspace()
	ws.Spec.Resources.Storage = "not-a-quantity"
	if _, err := BuildPVC(ws, scheme); err == nil {
		t.Error("BuildPVC: expected error for invalid storage quantity")
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

	t.Run("FSGroup1000", func(t *testing.T) {
		if psc == nil || psc.FSGroup == nil || *psc.FSGroup != 1000 {
			t.Errorf("PodSecurityContext.FSGroup = %v, want 1000", psc.FSGroup)
		}
	})

	t.Run("FSGroupChangeOnRootMismatch", func(t *testing.T) {
		if psc == nil || psc.FSGroupChangePolicy == nil || *psc.FSGroupChangePolicy != corev1.FSGroupChangeOnRootMismatch {
			t.Errorf("PodSecurityContext.FSGroupChangePolicy = %v, want OnRootMismatch", psc.FSGroupChangePolicy)
		}
	})
}
