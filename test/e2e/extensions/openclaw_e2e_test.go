// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package extensions

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
	extensionsv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
	"sigs.k8s.io/agent-sandbox/test/e2e/framework"
	"sigs.k8s.io/agent-sandbox/test/e2e/framework/predicates"
)

func runKubectlExec(ctx context.Context, namespace, podName, containerName string, cmd ...string) (string, error) {
	args := append([]string{"exec", "-n", namespace, podName, "-c", containerName, "--"}, cmd...)
	execCmd := exec.CommandContext(ctx, "kubectl", args...)
	var stdout, stderr bytes.Buffer
	execCmd.Stdout = &stdout
	execCmd.Stderr = &stderr
	if err := execCmd.Run(); err != nil {
		return "", fmt.Errorf("kubectl exec failed: %w, stderr: %s", err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

func TestOpenClawSandboxClaimCorrelationAndPersistence(t *testing.T) {
	tc := framework.NewTestContext(t)

	ns := &corev1.Namespace{}
	ns.Name = fmt.Sprintf("openclaw-e2e-test-%d", time.Now().UnixNano())
	require.NoError(t, tc.CreateWithCleanup(t.Context(), ns))

	// 1. Create SandboxTemplate with workspaces PVC configuration
	template := &extensionsv1beta1.SandboxTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "openclaw-e2e-template",
			Namespace: ns.Name,
		},
		Spec: extensionsv1beta1.SandboxTemplateSpec{
			PodTemplate: sandboxv1beta1.PodTemplate{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:    "busybox",
							Image:   "docker.io/library/busybox:1.36",
							Command: []string{"sh", "-c", "sleep 3600"},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "workspaces-pvc",
									MountPath: "/root/.openclaw",
								},
							},
						},
					},
				},
			},
			VolumeClaimTemplates: []sandboxv1beta1.PersistentVolumeClaimTemplate{
				{
					EmbeddedObjectMetadata: sandboxv1beta1.EmbeddedObjectMetadata{
						Name: "workspaces-pvc",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse("1Gi"),
							},
						},
					},
				},
			},
		},
	}
	require.NoError(t, tc.CreateWithCleanup(t.Context(), template))

	// 2. Create SandboxWarmPool (replicas = 1)
	warmPool := &extensionsv1beta1.SandboxWarmPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "openclaw-e2e-warmpool",
			Namespace: ns.Name,
		},
		Spec: extensionsv1beta1.SandboxWarmPoolSpec{
			Replicas:    1,
			TemplateRef: extensionsv1beta1.SandboxTemplateRef{Name: "openclaw-e2e-template"},
		},
	}
	require.NoError(t, tc.CreateWithCleanup(t.Context(), warmPool))

	// Wait for the WarmPool to become ready with 1 replica
	tc.MustWaitForObject(warmPool, predicates.ReadyReplicasConditionIsTrue)

	// 3. Create SandboxClaim representing session key correlation
	claim := &extensionsv1beta1.SandboxClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "openclaw-sandbox-claim-agent-main-main",
			Namespace: ns.Name,
		},
		Spec: extensionsv1beta1.SandboxClaimSpec{
			WarmPoolRef: extensionsv1beta1.SandboxWarmPoolRef{Name: "openclaw-e2e-warmpool"},
		},
	}
	require.NoError(t, tc.CreateWithCleanup(t.Context(), claim))

	// Wait for claim to become ready and bound to the warm pod
	tc.MustWaitForObject(claim, predicates.ReadyConditionIsTrue)

	// Fetch claim status to identify the assigned pod
	updatedClaim := &extensionsv1beta1.SandboxClaim{}
	require.NoError(t, tc.Get(t.Context(), types.NamespacedName{Name: claim.Name, Namespace: ns.Name}, updatedClaim))
	assignedSandboxName := updatedClaim.Status.SandboxStatus.Name
	require.NotEmpty(t, assignedSandboxName)

	// 4. E2E State Writing: write a test file into the workspace PVC
	testFilePath := "/root/.openclaw/workspace/pvc_test.txt"
	_, err := runKubectlExec(t.Context(), ns.Name, assignedSandboxName, "busybox", "mkdir", "-p", "/root/.openclaw/workspace")
	require.NoError(t, err)
	_, err = runKubectlExec(t.Context(), ns.Name, assignedSandboxName, "busybox", "sh", "-c", "echo 'E2E PVC State Preserved!' > "+testFilePath)
	require.NoError(t, err)

	// Verify file is correctly written
	content, err := runKubectlExec(t.Context(), ns.Name, assignedSandboxName, "busybox", "cat", testFilePath)
	require.NoError(t, err)
	require.Equal(t, "E2E PVC State Preserved!", content)

	// 5. Suspend Sandbox Claim (operatingMode = Suspended)
	sandboxObj := &sandboxv1beta1.Sandbox{}
	require.NoError(t, tc.Get(t.Context(), types.NamespacedName{Name: assignedSandboxName, Namespace: ns.Name}, sandboxObj))

	framework.MustUpdateObject(tc.ClusterClient, sandboxObj, func(obj *sandboxv1beta1.Sandbox) {
		obj.Spec.OperatingMode = sandboxv1beta1.SandboxOperatingModeSuspended
	})

	// Assert the Pod gets terminated and deleted
	require.NoError(t, tc.WaitForObject(t.Context(), sandboxObj, &predicates.StatusPredicate{
		MatchType:   "Ready",
		MatchStatus: metav1.ConditionFalse,
	}))

	// Verify pod is gone
	pod := &corev1.Pod{}
	err = tc.Get(t.Context(), types.NamespacedName{Name: assignedSandboxName, Namespace: ns.Name}, pod)
	require.True(t, apierrors.IsNotFound(err) || pod.DeletionTimestamp != nil)

	// 6. Resume Sandbox Claim (operatingMode = Running)
	framework.MustUpdateObject(tc.ClusterClient, sandboxObj, func(obj *sandboxv1beta1.Sandbox) {
		obj.Spec.OperatingMode = sandboxv1beta1.SandboxOperatingModeRunning
	})

	// Wait for the new pod to become ready
	tc.MustWaitForObject(sandboxObj, predicates.ReadyConditionIsTrue)

	// 7. Verify E2E State Retention: PVC file must still be present and correct
	contentAfterResume, err := runKubectlExec(t.Context(), ns.Name, assignedSandboxName, "busybox", "cat", testFilePath)
	require.NoError(t, err)
	require.Equal(t, "E2E PVC State Preserved!", contentAfterResume)
}
