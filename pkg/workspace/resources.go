// Package workspace provides resource builders for the workspace operator (PVC, Pod, Service).
package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	workspacev1alpha1 "workspace-operator/api/v1alpha1"
)

// dnsLabelRegex matches a valid Kubernetes DNS label: lowercase alphanumeric,
// may contain hyphens, must start and end with alphanumeric, max 63 chars.
var dnsLabelRegex = regexp.MustCompile(`^[a-z0-9]([a-z0-9\-]*[a-z0-9])?$`)

const (
	labelApp       = "workspace"
	labelManagedBy = "devplane"
	labelUser      = "user"
	ttydPort       = 7681
	workspaceMount = "/workspace"
)

// PVCName returns the PVC name for a user ID.
func PVCName(userID string) string {
	return fmt.Sprintf("%s-workspace-pvc", userID)
}

// PodName returns the Pod name for a user ID.
func PodName(userID string) string {
	return fmt.Sprintf("%s-workspace-pod", userID)
}

// ServiceName returns the headless Service name for a user ID.
func ServiceName(userID string) string {
	return fmt.Sprintf("%s-workspace-svc", userID)
}

// Labels returns the common labels for all workspace resources.
func Labels(userID string) map[string]string {
	return map[string]string{
		"app":        labelApp,
		labelUser:    userID,
		"managed-by": labelManagedBy,
	}
}

// BuildPVC creates a PersistentVolumeClaim for the workspace with an owner reference to the Workspace.
func BuildPVC(workspace *workspacev1alpha1.Workspace, scheme *runtime.Scheme) (*corev1.PersistentVolumeClaim, error) {
	userID := workspace.Spec.User.ID
	name := PVCName(userID)

	storageQty, err := resource.ParseQuantity(workspace.Spec.Resources.Storage)
	if err != nil {
		return nil, fmt.Errorf("parse storage quantity %q: %w", workspace.Spec.Resources.Storage, err)
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: workspace.Namespace,
			Labels:    Labels(userID),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: storageQty,
				},
			},
		},
	}
	if workspace.Spec.Persistence.StorageClass != "" {
		pvc.Spec.StorageClassName = &workspace.Spec.Persistence.StorageClass
	}
	if err := controllerutil.SetControllerReference(workspace, pvc, scheme); err != nil {
		return nil, fmt.Errorf("set PVC owner reference: %w", err)
	}
	return pvc, nil
}

// ServiceAccountName returns the per-user ServiceAccount name for a user ID.
func ServiceAccountName(userID string) string {
	return fmt.Sprintf("%s-workspace", userID)
}

// BuildPod creates a Pod for the workspace with security context, volume, env, and owner reference.
func BuildPod(workspace *workspacev1alpha1.Workspace, pvcName, workspaceImage string, scheme *runtime.Scheme) (*corev1.Pod, error) {
	userID := workspace.Spec.User.ID
	name := PodName(userID)
	labels := Labels(userID)

	cpuQty, err := resource.ParseQuantity(workspace.Spec.Resources.CPU)
	if err != nil {
		return nil, fmt.Errorf("parse CPU quantity %q: %w", workspace.Spec.Resources.CPU, err)
	}
	memQty, err := resource.ParseQuantity(workspace.Spec.Resources.Memory)
	if err != nil {
		return nil, fmt.Errorf("parse memory quantity %q: %w", workspace.Spec.Resources.Memory, err)
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: workspace.Namespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			ServiceAccountName: ServiceAccountName(userID),
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot: ptr(true),
				RunAsUser:    ptr(int64(1000)),
				SeccompProfile: &corev1.SeccompProfile{
					Type: corev1.SeccompProfileTypeRuntimeDefault,
				},
			},
			Containers: []corev1.Container{
				{
					Name:  "workspace",
					Image: workspaceImage,
					SecurityContext: &corev1.SecurityContext{
						ReadOnlyRootFilesystem:   ptr(true),
						AllowPrivilegeEscalation: ptr(false),
						Capabilities: &corev1.Capabilities{
							Drop: []corev1.Capability{"ALL"},
						},
					},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    cpuQty,
							corev1.ResourceMemory: memQty,
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    cpuQty,
							corev1.ResourceMemory: memQty,
						},
					},
					Ports: []corev1.ContainerPort{
						{Name: "ttyd", ContainerPort: ttydPort, Protocol: corev1.ProtocolTCP},
					},
					ReadinessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							TCPSocket: &corev1.TCPSocketAction{
								Port: intstr.FromInt(ttydPort),
							},
						},
						InitialDelaySeconds: 5,
						PeriodSeconds:       5,
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "workspace-data",
							MountPath: workspaceMount,
						},
						{
							Name:      "tmp",
							MountPath: "/tmp",
						},
					},
					Env: buildEnvVars(workspace),
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "workspace-data",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: pvcName,
						},
					},
				},
				{
					Name:         "tmp",
					VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
				},
			},
		},
	}
	if workspace.Spec.TLS.CustomCABundle != nil && workspace.Spec.TLS.CustomCABundle.Name != "" {
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: "custom-ca-certs",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: workspace.Spec.TLS.CustomCABundle.Name,
					},
				},
			},
		})
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      "custom-ca-certs",
			MountPath: "/etc/ssl/certs/custom",
			ReadOnly:  true,
		})
		pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env,
			corev1.EnvVar{Name: "CUSTOM_CA_MOUNTED", Value: "true"},
		)
	}

	if err := controllerutil.SetControllerReference(workspace, pod, scheme); err != nil {
		return nil, fmt.Errorf("set Pod owner reference: %w", err)
	}
	return pod, nil
}

// BuildHeadlessService creates a headless Service for the workspace Pod with an owner reference.
func BuildHeadlessService(workspace *workspacev1alpha1.Workspace, scheme *runtime.Scheme) (*corev1.Service, error) {
	userID := workspace.Spec.User.ID
	name := ServiceName(userID)
	labels := Labels(userID)

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: workspace.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: corev1.ClusterIPNone,
			Selector:  labels,
			Ports: []corev1.ServicePort{
				{
					Name:     "ttyd",
					Port:     ttydPort,
					Protocol: corev1.ProtocolTCP,
				},
			},
		},
	}
	if err := controllerutil.SetControllerReference(workspace, svc, scheme); err != nil {
		return nil, fmt.Errorf("set Service owner reference: %w", err)
	}
	return svc, nil
}

// ValidateSpec returns an error if the workspace spec is invalid.
// It validates required fields, user ID DNS-label format, and resource quantity syntax.
func ValidateSpec(workspace *workspacev1alpha1.Workspace) error {
	if workspace == nil {
		return errors.New("workspace is nil")
	}
	s := &workspace.Spec
	if s.User.ID == "" {
		return errors.New("spec.user.id is required")
	}
	if len(s.User.ID) > 63 {
		return fmt.Errorf("spec.user.id must be 63 characters or fewer (got %d)", len(s.User.ID))
	}
	// User ID is used as a prefix in Kubernetes resource names (DNS label format).
	if !dnsLabelRegex.MatchString(s.User.ID) {
		return errors.New("spec.user.id must be a valid DNS label: lowercase alphanumeric and hyphens only, must start and end with alphanumeric")
	}
	if s.User.Email == "" {
		return errors.New("spec.user.email is required")
	}
	if s.Resources.Storage == "" {
		return errors.New("spec.resources.storage is required")
	}
	if s.Resources.CPU == "" {
		return errors.New("spec.resources.cpu is required")
	}
	if s.Resources.Memory == "" {
		return errors.New("spec.resources.memory is required")
	}
	// Validate resource quantities eagerly to surface parse errors before
	// resource.MustParse panics in builder functions.
	if _, err := resource.ParseQuantity(s.Resources.CPU); err != nil {
		return fmt.Errorf("spec.resources.cpu invalid: %w", err)
	}
	if _, err := resource.ParseQuantity(s.Resources.Memory); err != nil {
		return fmt.Errorf("spec.resources.memory invalid: %w", err)
	}
	if _, err := resource.ParseQuantity(s.Resources.Storage); err != nil {
		return fmt.Errorf("spec.resources.storage invalid: %w", err)
	}
	if len(s.AIConfig.Providers) == 0 {
		return errors.New("spec.aiConfig.providers must have at least one entry")
	}
	for i, p := range s.AIConfig.Providers {
		if p.Name == "" {
			return fmt.Errorf("spec.aiConfig.providers[%d].name is required", i)
		}
		if p.Endpoint == "" {
			return fmt.Errorf("spec.aiConfig.providers[%d].endpoint is required", i)
		}
		if len(p.Models) == 0 {
			return fmt.Errorf("spec.aiConfig.providers[%d].models must have at least one entry", i)
		}
	}
	return nil
}

// buildEnvVars constructs the container environment variables for a workspace pod.
// AI provider configuration is serialised to JSON so the entrypoint script can
// iterate over providers without requiring a template engine.
func buildEnvVars(workspace *workspacev1alpha1.Workspace) []corev1.EnvVar {
	providersJSON, _ := json.Marshal(workspace.Spec.AIConfig.Providers)
	return []corev1.EnvVar{
		{Name: "AI_PROVIDERS_JSON", Value: string(providersJSON)},
		{Name: "USER_EMAIL", Value: workspace.Spec.User.Email},
		{Name: "USER_ID", Value: workspace.Spec.User.ID},
	}
}

func ptr[T any](v T) *T {
	return &v
}
