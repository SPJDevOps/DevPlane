// Package workspace provides resource builders for the workspace operator (PVC, Pod, Service).
package workspace

import (
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	workspacev1alpha1 "workspace-operator/api/v1alpha1"
)

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
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: workspace.Namespace,
			Labels:    Labels(userID),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(workspace.Spec.Resources.Storage),
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

	cpuQty := resource.MustParse(workspace.Spec.Resources.CPU)
	memQty := resource.MustParse(workspace.Spec.Resources.Memory)

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
					Env: []corev1.EnvVar{
						{Name: "VLLM_ENDPOINT", Value: workspace.Spec.AIConfig.VLLMEndpoint},
						{Name: "VLLM_MODEL", Value: workspace.Spec.AIConfig.VLLMModel},
						{Name: "USER_EMAIL", Value: workspace.Spec.User.Email},
						{Name: "USER_ID", Value: workspace.Spec.User.ID},
					},
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
func ValidateSpec(workspace *workspacev1alpha1.Workspace) error {
	if workspace == nil {
		return errors.New("workspace is nil")
	}
	s := &workspace.Spec
	if s.User.ID == "" {
		return errors.New("spec.user.id is required")
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
	if s.AIConfig.VLLMEndpoint == "" {
		return errors.New("spec.aiConfig.vllmEndpoint is required")
	}
	if s.AIConfig.VLLMModel == "" {
		return errors.New("spec.aiConfig.vllmModel is required")
	}
	return nil
}

func ptr[T any](v T) *T {
	return &v
}
