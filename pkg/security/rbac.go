package security

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	workspacev1alpha1 "workspace-operator/api/v1alpha1"
)

// ServiceAccountName returns the ServiceAccount name for a user ID.
func ServiceAccountName(userID string) string {
	return fmt.Sprintf("%s-workspace", userID)
}

// BuildServiceAccount creates a ServiceAccount for the workspace pod.
// The pod spec should reference this account so the pod runs with minimal
// in-cluster credentials rather than the default ServiceAccount.
func BuildServiceAccount(workspace *workspacev1alpha1.Workspace, scheme *runtime.Scheme) (*corev1.ServiceAccount, error) {
	userID := workspace.Spec.User.ID
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ServiceAccountName(userID),
			Namespace: workspace.Namespace,
			Labels: map[string]string{
				"app":        "workspace",
				"user":       userID,
				"managed-by": "devplane",
			},
		},
	}
	if err := controllerutil.SetControllerReference(workspace, sa, scheme); err != nil {
		return nil, fmt.Errorf("set ServiceAccount owner reference: %w", err)
	}
	return sa, nil
}

// BuildRole creates a Role that grants the workspace pod read-only access to
// common resources in its namespace.  This is enough for kubectl/k9s to work
// with the pod's in-cluster credentials without exposing write operations or
// secrets.
func BuildRole(workspace *workspacev1alpha1.Workspace, scheme *runtime.Scheme) (*rbacv1.Role, error) {
	userID := workspace.Spec.User.ID
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ServiceAccountName(userID),
			Namespace: workspace.Namespace,
			Labels: map[string]string{
				"app":        "workspace",
				"user":       userID,
				"managed-by": "devplane",
			},
		},
		Rules: []rbacv1.PolicyRule{
			// Read common workload resources — useful for k9s / kubectl from
			// inside the terminal.  Secrets are intentionally excluded.
			{
				APIGroups: []string{""},
				Resources: []string{"pods", "services", "configmaps", "events", "endpoints"},
				Verbs:     []string{"get", "list", "watch"},
			},
			// Allow reading pod logs.
			{
				APIGroups: []string{""},
				Resources: []string{"pods/log"},
				Verbs:     []string{"get", "list"},
			},
			// Read-only view of apps resources.
			{
				APIGroups: []string{"apps"},
				Resources: []string{"deployments", "replicasets", "statefulsets", "daemonsets"},
				Verbs:     []string{"get", "list", "watch"},
			},
			// Read own Workspace CR so the terminal can inspect its own status.
			{
				APIGroups: []string{"workspace.devplane.io"},
				Resources: []string{"workspaces"},
				Verbs:     []string{"get"},
			},
		},
	}
	if err := controllerutil.SetControllerReference(workspace, role, scheme); err != nil {
		return nil, fmt.Errorf("set Role owner reference: %w", err)
	}
	return role, nil
}

// BuildRoleBinding binds the per-user Role to the per-user ServiceAccount.
func BuildRoleBinding(workspace *workspacev1alpha1.Workspace, scheme *runtime.Scheme) (*rbacv1.RoleBinding, error) {
	userID := workspace.Spec.User.ID
	saName := ServiceAccountName(userID)
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      saName,
			Namespace: workspace.Namespace,
			Labels: map[string]string{
				"app":        "workspace",
				"user":       userID,
				"managed-by": "devplane",
			},
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      saName,
				Namespace: workspace.Namespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     saName,
		},
	}
	if err := controllerutil.SetControllerReference(workspace, rb, scheme); err != nil {
		return nil, fmt.Errorf("set RoleBinding owner reference: %w", err)
	}
	return rb, nil
}
