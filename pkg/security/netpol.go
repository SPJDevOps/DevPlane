package security

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	workspacev1alpha1 "workspace-operator/api/v1alpha1"
)

// NetworkPolicy naming and label conventions.
const (
	// labelGatewayApp is the label that must be present on gateway pods so the
	// ingress policy can select them.
	labelGatewayApp = "workspace-gateway"

	// dnsNamespace is selected via the stable kubernetes.io/metadata.name label
	// which Kubernetes sets automatically on every namespace since v1.21.
	dnsNamespace = "kube-system"
)

// netpolName returns a deterministic NetworkPolicy name for a user + suffix.
func netpolName(userID, suffix string) string {
	return fmt.Sprintf("%s-workspace-%s", userID, suffix)
}

// workspacePodSelector returns the label selector that matches workspace pods
// for a specific user.
func workspacePodSelector(userID string) metav1.LabelSelector {
	return metav1.LabelSelector{
		MatchLabels: map[string]string{
			"app":  "workspace",
			"user": userID,
		},
	}
}

// namespaceSelectorByName returns a LabelSelector that matches a namespace by
// its built-in kubernetes.io/metadata.name label (available since K8s 1.21).
func namespaceSelectorByName(name string) networkingv1.NetworkPolicyPeer {
	return networkingv1.NetworkPolicyPeer{
		NamespaceSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{
				"kubernetes.io/metadata.name": name,
			},
		},
	}
}

// port returns a *intstr.IntOrString from an integer port number.
func port(p int) *intstr.IntOrString {
	v := intstr.FromInt(p)
	return &v
}

func protoPtr(p corev1.Protocol) *corev1.Protocol { return &p }

// BuildDenyAllNetworkPolicy returns a NetworkPolicy that denies all ingress and
// egress for workspace pods of userID.  Other, more specific policies then
// selectively re-open the required traffic.
func BuildDenyAllNetworkPolicy(workspace *workspacev1alpha1.Workspace, scheme *runtime.Scheme) (*networkingv1.NetworkPolicy, error) {
	userID := workspace.Spec.User.ID
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      netpolName(userID, "deny-all"),
			Namespace: workspace.Namespace,
			Labels: map[string]string{
				"app":        "workspace",
				"user":       userID,
				"managed-by": "devplane",
			},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: workspacePodSelector(userID),
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeIngress,
				networkingv1.PolicyTypeEgress,
			},
			// Empty Ingress and Egress slices = deny all.
			Ingress: []networkingv1.NetworkPolicyIngressRule{},
			Egress:  []networkingv1.NetworkPolicyEgressRule{},
		},
	}
	if err := controllerutil.SetControllerReference(workspace, np, scheme); err != nil {
		return nil, fmt.Errorf("set NetworkPolicy owner reference: %w", err)
	}
	return np, nil
}

// BuildEgressNetworkPolicy returns a NetworkPolicy that allows workspace pods
// to reach:
//   - DNS (UDP+TCP 53) in kube-system
//   - All pods in vllmNamespace (typically "ai-system")
//   - The public internet on port 80 and 443
func BuildEgressNetworkPolicy(workspace *workspacev1alpha1.Workspace, vllmNamespace string, scheme *runtime.Scheme) (*networkingv1.NetworkPolicy, error) {
	userID := workspace.Spec.User.ID
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      netpolName(userID, "egress"),
			Namespace: workspace.Namespace,
			Labels: map[string]string{
				"app":        "workspace",
				"user":       userID,
				"managed-by": "devplane",
			},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: workspacePodSelector(userID),
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				// DNS — UDP and TCP both needed (TCP for large responses / zone transfers).
				{
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: protoPtr(corev1.ProtocolUDP), Port: port(53)},
						{Protocol: protoPtr(corev1.ProtocolTCP), Port: port(53)},
					},
					To: []networkingv1.NetworkPolicyPeer{namespaceSelectorByName(dnsNamespace)},
				},
				// vLLM — all pods in the ai-system namespace, any port.
				{
					To: []networkingv1.NetworkPolicyPeer{namespaceSelectorByName(vllmNamespace)},
				},
				// Internet — HTTP and HTTPS to any IP (git clone, package downloads, etc.).
				{
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: protoPtr(corev1.ProtocolTCP), Port: port(80)},
						{Protocol: protoPtr(corev1.ProtocolTCP), Port: port(443)},
					},
					To: []networkingv1.NetworkPolicyPeer{
						{IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0"}},
					},
				},
			},
		},
	}
	if err := controllerutil.SetControllerReference(workspace, np, scheme); err != nil {
		return nil, fmt.Errorf("set NetworkPolicy owner reference: %w", err)
	}
	return np, nil
}

// BuildIngressFromGatewayNetworkPolicy returns a NetworkPolicy that allows the
// gateway pods (selected by app=workspace-gateway) to reach the workspace pod
// on the ttyd port.
func BuildIngressFromGatewayNetworkPolicy(workspace *workspacev1alpha1.Workspace, scheme *runtime.Scheme) (*networkingv1.NetworkPolicy, error) {
	userID := workspace.Spec.User.ID
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      netpolName(userID, "ingress-gateway"),
			Namespace: workspace.Namespace,
			Labels: map[string]string{
				"app":        "workspace",
				"user":       userID,
				"managed-by": "devplane",
			},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: workspacePodSelector(userID),
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: protoPtr(corev1.ProtocolTCP), Port: port(7681)},
					},
					From: []networkingv1.NetworkPolicyPeer{
						{
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"app": labelGatewayApp},
							},
						},
					},
				},
			},
		},
	}
	if err := controllerutil.SetControllerReference(workspace, np, scheme); err != nil {
		return nil, fmt.Errorf("set NetworkPolicy owner reference: %w", err)
	}
	return np, nil
}
