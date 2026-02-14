package security

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
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

func minimalWorkspace() *workspacev1alpha1.Workspace {
	return &workspacev1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "ws1", Namespace: "dev"},
		Spec: workspacev1alpha1.WorkspaceSpec{
			User: workspacev1alpha1.UserInfo{ID: "alice", Email: "alice@example.com"},
			Resources: workspacev1alpha1.ResourceRequirements{
				CPU: "1", Memory: "2Gi", Storage: "20Gi",
			},
			AIConfig: workspacev1alpha1.AIConfiguration{
				VLLMEndpoint: "http://vllm:8000",
				VLLMModel:    "model",
			},
		},
	}
}

// ── NetworkPolicy tests ───────────────────────────────────────────────────────

func TestBuildDenyAllNetworkPolicy(t *testing.T) {
	ws := minimalWorkspace()
	np, err := BuildDenyAllNetworkPolicy(ws, scheme)
	if err != nil {
		t.Fatalf("BuildDenyAllNetworkPolicy: %v", err)
	}

	if np.Name != "alice-workspace-deny-all" {
		t.Errorf("Name = %q, want alice-workspace-deny-all", np.Name)
	}
	if np.Namespace != "dev" {
		t.Errorf("Namespace = %q, want dev", np.Namespace)
	}

	// Must select workspace pods for this user.
	if np.Spec.PodSelector.MatchLabels["app"] != "workspace" ||
		np.Spec.PodSelector.MatchLabels["user"] != "alice" {
		t.Errorf("PodSelector = %v, want app=workspace user=alice", np.Spec.PodSelector.MatchLabels)
	}

	// Must declare both policy types so Kubernetes enforces the deny-all.
	wantTypes := map[networkingv1.PolicyType]bool{
		networkingv1.PolicyTypeIngress: false,
		networkingv1.PolicyTypeEgress:  false,
	}
	for _, pt := range np.Spec.PolicyTypes {
		wantTypes[pt] = true
	}
	for pt, found := range wantTypes {
		if !found {
			t.Errorf("missing PolicyType %q", pt)
		}
	}

	// Empty rules = deny all.
	if len(np.Spec.Ingress) != 0 {
		t.Errorf("Ingress rules = %d, want 0 (deny all)", len(np.Spec.Ingress))
	}
	if len(np.Spec.Egress) != 0 {
		t.Errorf("Egress rules = %d, want 0 (deny all)", len(np.Spec.Egress))
	}

	if len(np.OwnerReferences) != 1 || np.OwnerReferences[0].Kind != "Workspace" {
		t.Errorf("expected Workspace owner reference, got %v", np.OwnerReferences)
	}
}

func TestBuildEgressNetworkPolicy(t *testing.T) {
	ws := minimalWorkspace()
	np, err := BuildEgressNetworkPolicy(ws, "ai-system", scheme)
	if err != nil {
		t.Fatalf("BuildEgressNetworkPolicy: %v", err)
	}

	if np.Name != "alice-workspace-egress" {
		t.Errorf("Name = %q, want alice-workspace-egress", np.Name)
	}

	// Must only declare Egress.
	if len(np.Spec.PolicyTypes) != 1 || np.Spec.PolicyTypes[0] != networkingv1.PolicyTypeEgress {
		t.Errorf("PolicyTypes = %v, want [Egress]", np.Spec.PolicyTypes)
	}

	if len(np.Spec.Egress) != 3 {
		t.Fatalf("Egress rules = %d, want 3 (DNS, vLLM, internet)", len(np.Spec.Egress))
	}

	t.Run("DNS rule", func(t *testing.T) {
		rule := np.Spec.Egress[0]
		if len(rule.To) == 0 {
			t.Fatal("DNS rule has no To peer")
		}
		ns := rule.To[0].NamespaceSelector
		if ns == nil || ns.MatchLabels["kubernetes.io/metadata.name"] != dnsNamespace {
			t.Errorf("DNS peer namespace = %v, want kube-system", ns)
		}
		hasTCP, hasUDP := false, false
		for _, p := range rule.Ports {
			if p.Port.IntVal == 53 {
				if p.Protocol != nil && *p.Protocol == corev1.ProtocolTCP {
					hasTCP = true
				}
				if p.Protocol != nil && *p.Protocol == corev1.ProtocolUDP {
					hasUDP = true
				}
			}
		}
		if !hasTCP || !hasUDP {
			t.Errorf("DNS rule must allow port 53 TCP and UDP (hasTCP=%v hasUDP=%v)", hasTCP, hasUDP)
		}
	})

	t.Run("vLLM rule", func(t *testing.T) {
		rule := np.Spec.Egress[1]
		if len(rule.To) == 0 {
			t.Fatal("vLLM rule has no To peer")
		}
		ns := rule.To[0].NamespaceSelector
		if ns == nil || ns.MatchLabels["kubernetes.io/metadata.name"] != "ai-system" {
			t.Errorf("vLLM peer namespace = %v, want ai-system", ns)
		}
		// No port restriction on vLLM egress.
		if len(rule.Ports) != 0 {
			t.Errorf("vLLM rule should have no port restriction, got %v", rule.Ports)
		}
	})

	t.Run("Internet rule", func(t *testing.T) {
		rule := np.Spec.Egress[2]
		if len(rule.To) == 0 {
			t.Fatal("internet rule has no To peer")
		}
		if rule.To[0].IPBlock == nil || rule.To[0].IPBlock.CIDR != "0.0.0.0/0" {
			t.Errorf("internet CIDR = %v, want 0.0.0.0/0", rule.To[0].IPBlock)
		}
		has80, has443 := false, false
		for _, p := range rule.Ports {
			if p.Protocol != nil && *p.Protocol != corev1.ProtocolTCP {
				t.Errorf("internet port %d has protocol %v, want TCP", p.Port.IntVal, *p.Protocol)
			}
			switch p.Port.IntVal {
			case 80:
				has80 = true
			case 443:
				has443 = true
			}
		}
		if !has80 || !has443 {
			t.Errorf("internet rule must allow ports 80 and 443 (has80=%v has443=%v)", has80, has443)
		}
	})
}

func TestBuildIngressFromGatewayNetworkPolicy(t *testing.T) {
	ws := minimalWorkspace()
	np, err := BuildIngressFromGatewayNetworkPolicy(ws, scheme)
	if err != nil {
		t.Fatalf("BuildIngressFromGatewayNetworkPolicy: %v", err)
	}

	if np.Name != "alice-workspace-ingress-gateway" {
		t.Errorf("Name = %q, want alice-workspace-ingress-gateway", np.Name)
	}

	// Must only declare Ingress.
	if len(np.Spec.PolicyTypes) != 1 || np.Spec.PolicyTypes[0] != networkingv1.PolicyTypeIngress {
		t.Errorf("PolicyTypes = %v, want [Ingress]", np.Spec.PolicyTypes)
	}

	if len(np.Spec.Ingress) != 1 {
		t.Fatalf("Ingress rules = %d, want 1", len(np.Spec.Ingress))
	}
	rule := np.Spec.Ingress[0]

	// Must restrict to ttyd port.
	if len(rule.Ports) != 1 || rule.Ports[0].Port.IntVal != 7681 {
		t.Errorf("Ingress port = %v, want 7681", rule.Ports)
	}

	// Must allow only from gateway pods.
	if len(rule.From) != 1 {
		t.Fatalf("From peers = %d, want 1", len(rule.From))
	}
	ps := rule.From[0].PodSelector
	if ps == nil || ps.MatchLabels["app"] != labelGatewayApp {
		t.Errorf("From PodSelector = %v, want app=%s", ps, labelGatewayApp)
	}
}

// ── RBAC tests ────────────────────────────────────────────────────────────────

func TestBuildServiceAccount(t *testing.T) {
	ws := minimalWorkspace()
	sa, err := BuildServiceAccount(ws, scheme)
	if err != nil {
		t.Fatalf("BuildServiceAccount: %v", err)
	}

	if sa.Name != "alice-workspace" {
		t.Errorf("Name = %q, want alice-workspace", sa.Name)
	}
	if sa.Namespace != "dev" {
		t.Errorf("Namespace = %q, want dev", sa.Namespace)
	}
	if len(sa.OwnerReferences) != 1 || sa.OwnerReferences[0].Kind != "Workspace" {
		t.Errorf("expected Workspace owner reference, got %v", sa.OwnerReferences)
	}
}

func TestBuildRole(t *testing.T) {
	ws := minimalWorkspace()
	role, err := BuildRole(ws, scheme)
	if err != nil {
		t.Fatalf("BuildRole: %v", err)
	}

	if role.Name != "alice-workspace" {
		t.Errorf("Name = %q, want alice-workspace", role.Name)
	}

	// Verify no write permissions exist in any rule.
	writeVerbs := map[string]bool{"create": true, "update": true, "patch": true, "delete": true, "deletecollection": true}
	for _, rule := range role.Rules {
		for _, v := range rule.Verbs {
			if writeVerbs[v] {
				t.Errorf("Role contains write verb %q in rule %+v", v, rule)
			}
		}
	}

	// Secrets must not be listed in any rule.
	for _, rule := range role.Rules {
		for _, res := range rule.Resources {
			if res == "secrets" {
				t.Errorf("Role must not grant access to secrets (found in rule %+v)", rule)
			}
		}
	}

	if len(role.OwnerReferences) != 1 || role.OwnerReferences[0].Kind != "Workspace" {
		t.Errorf("expected Workspace owner reference, got %v", role.OwnerReferences)
	}
}

func TestBuildRoleBinding(t *testing.T) {
	ws := minimalWorkspace()
	rb, err := BuildRoleBinding(ws, scheme)
	if err != nil {
		t.Fatalf("BuildRoleBinding: %v", err)
	}

	if rb.Name != "alice-workspace" {
		t.Errorf("Name = %q, want alice-workspace", rb.Name)
	}

	// Must bind the correct ServiceAccount.
	if len(rb.Subjects) != 1 {
		t.Fatalf("Subjects = %d, want 1", len(rb.Subjects))
	}
	subj := rb.Subjects[0]
	if subj.Kind != rbacv1.ServiceAccountKind || subj.Name != "alice-workspace" || subj.Namespace != "dev" {
		t.Errorf("Subject = %+v, want ServiceAccount alice-workspace in dev", subj)
	}

	// Must reference the correct Role (not ClusterRole).
	if rb.RoleRef.Kind != "Role" || rb.RoleRef.Name != "alice-workspace" {
		t.Errorf("RoleRef = %+v, want Role alice-workspace", rb.RoleRef)
	}

	if len(rb.OwnerReferences) != 1 || rb.OwnerReferences[0].Kind != "Workspace" {
		t.Errorf("expected Workspace owner reference, got %v", rb.OwnerReferences)
	}
}

func TestServiceAccountName(t *testing.T) {
	if got := ServiceAccountName("bob"); got != "bob-workspace" {
		t.Errorf("ServiceAccountName(%q) = %q, want bob-workspace", "bob", got)
	}
}
