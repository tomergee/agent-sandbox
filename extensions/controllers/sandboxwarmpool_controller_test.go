// Copyright 2025 The Kubernetes Authors.
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

package controllers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	sandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	sandboxcontrollers "sigs.k8s.io/agent-sandbox/controllers"
	extensionsv1alpha1 "sigs.k8s.io/agent-sandbox/extensions/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// Create a test scheme with extensions types registered
func newTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(sandboxv1alpha1.AddToScheme(scheme))
	utilruntime.Must(extensionsv1alpha1.AddToScheme(scheme))
	return scheme
}

func createSandbox(name, namespace, poolNameHash string) *sandboxv1alpha1.Sandbox {
	return &sandboxv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{poolLabel: poolNameHash},
		},
		Spec: sandboxv1alpha1.SandboxSpec{
			PodTemplate: sandboxv1alpha1.PodTemplate{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "test-image",
						},
					},
				},
			},
		},
	}
}

func createPoolSandbox(poolName, namespace, poolNameHash, suffix string) *sandboxv1alpha1.Sandbox {
	name := poolName + suffix
	return createSandbox(name, namespace, poolNameHash)
}

func createTemplate(name, namespace string) *extensionsv1alpha1.SandboxTemplate {
	return &extensionsv1alpha1.SandboxTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: extensionsv1alpha1.SandboxTemplateSpec{
			PodTemplate: sandboxv1alpha1.PodTemplate{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "test-image",
						},
					},
				},
			},
		},
	}
}

func TestReconcilePool(t *testing.T) {
	poolName := "test-pool"
	poolNamespace := "default"
	templateName := "test-template"
	replicas := int32(3)

	// Create a SandboxTemplate
	template := createTemplate(templateName, poolNamespace)

	warmPool := &extensionsv1alpha1.SandboxWarmPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      poolName,
			Namespace: poolNamespace,
		},
		Spec: extensionsv1alpha1.SandboxWarmPoolSpec{
			Replicas: replicas,
			TemplateRef: extensionsv1alpha1.SandboxTemplateRef{
				Name: templateName,
			},
		},
	}

	// Compute the pool name hash
	poolNameHash := sandboxcontrollers.NameHash(poolName)

	testCases := []struct {
		name             string
		initialObjs      []runtime.Object
		expectedReplicas int32
	}{
		{
			name:             "creates sandboxes when pool is empty",
			initialObjs:      []runtime.Object{template},
			expectedReplicas: replicas,
		},
		{
			name: "creates additional sandboxes when under-provisioned",
			initialObjs: []runtime.Object{
				template,
				createPoolSandbox(poolName, poolNamespace, poolNameHash, "abc123"),
			},
			expectedReplicas: replicas,
		},
		{
			name: "deletes excess sandboxes when over-provisioned",
			initialObjs: []runtime.Object{
				template,
				createPoolSandbox(poolName, poolNamespace, poolNameHash, "abc123"),
				createPoolSandbox(poolName, poolNamespace, poolNameHash, "def456"),
				createPoolSandbox(poolName, poolNamespace, poolNameHash, "ghi789"),
				createPoolSandbox(poolName, poolNamespace, poolNameHash, "jkl012"),
			},
			expectedReplicas: replicas,
		},
		{
			name: "maintains correct replica count",
			initialObjs: []runtime.Object{
				template,
				createPoolSandbox(poolName, poolNamespace, poolNameHash, "abc123"),
				createPoolSandbox(poolName, poolNamespace, poolNameHash, "def456"),
				createPoolSandbox(poolName, poolNamespace, poolNameHash, "ghi789"),
			},
			expectedReplicas: replicas,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := SandboxWarmPoolReconciler{
				Client: fake.NewClientBuilder().
					WithScheme(newTestScheme()).
					WithRuntimeObjects(tc.initialObjs...).
					Build(),
			}

			ctx := context.Background()

			// Run reconcilePool twice: first to create/delete, second to update status
			err := r.reconcilePool(ctx, warmPool)
			require.NoError(t, err)

			err = r.reconcilePool(ctx, warmPool)
			require.NoError(t, err)

			// Verify final state
			list := &sandboxv1alpha1.SandboxList{}
			err = r.List(ctx, list, &client.ListOptions{Namespace: poolNamespace})
			require.NoError(t, err)

			// Count sandboxes with correct pool label
			count := int32(0)
			for _, sb := range list.Items {
				if sb.Labels[poolLabel] == poolNameHash {
					count++
				}
			}

			require.Equal(t, tc.expectedReplicas, count)
			require.Equal(t, tc.expectedReplicas, warmPool.Status.Replicas)
		})
	}
}

func TestReconcilePoolControllerRef(t *testing.T) {
	poolName := "test-pool"
	poolNamespace := "default"
	templateName := "test-template"
	replicas := int32(2)

	// Create a SandboxTemplate
	template := createTemplate(templateName, poolNamespace)

	warmPool := &extensionsv1alpha1.SandboxWarmPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      poolName,
			Namespace: poolNamespace,
			UID:       "warmpool-uid-123",
		},
		Spec: extensionsv1alpha1.SandboxWarmPoolSpec{
			Replicas: replicas,
			TemplateRef: extensionsv1alpha1.SandboxTemplateRef{
				Name: templateName,
			},
		},
	}

	// Compute the pool name hash
	poolNameHash := sandboxcontrollers.NameHash(poolName)

	createSandboxWithOwner := func(name string, ownerUID string) *sandboxv1alpha1.Sandbox {
		sb := createPoolSandbox(poolName, poolNamespace, poolNameHash, name)
		if ownerUID != "" {
			sb.OwnerReferences = []metav1.OwnerReference{
				{
					APIVersion: "extensions.agents.x-k8s.io/v1alpha1",
					Kind:       "SandboxWarmPool",
					Name:       poolName,
					UID:        types.UID(ownerUID),
					Controller: boolPtr(true),
				},
			}
		}
		return sb
	}

	createSandboxWithDifferentController := func(name string) *sandboxv1alpha1.Sandbox {
		sb := createPoolSandbox(poolName, poolNamespace, poolNameHash, name)
		sb.OwnerReferences = []metav1.OwnerReference{
			{
				APIVersion: "apps/v1",
				Kind:       "ReplicaSet",
				Name:       "other-controller",
				UID:        "other-uid-456",
				Controller: boolPtr(true),
			},
		}
		return sb
	}

	testCases := []struct {
		name             string
		initialObjs      []runtime.Object
		expectedReplicas int32
		expectedAdopted  int // number of sandboxes that should be adopted
	}{
		{
			name: "adopts orphaned sandboxes with no controller reference",
			initialObjs: []runtime.Object{
				template,
				createSandboxWithOwner("abc123", ""), // No owner reference
				createSandboxWithOwner("def456", ""), // No owner reference
			},
			expectedReplicas: replicas,
			expectedAdopted:  2,
		},
		{
			name: "includes sandboxes with correct controller reference",
			initialObjs: []runtime.Object{
				template,
				createSandboxWithOwner("abc123", "warmpool-uid-123"),
				createSandboxWithOwner("def456", "warmpool-uid-123"),
			},
			expectedReplicas: replicas,
			expectedAdopted:  0,
		},
		{
			name: "ignores sandboxes with different controller reference",
			initialObjs: []runtime.Object{
				template,
				createSandboxWithDifferentController("abc123"),
				createSandboxWithDifferentController("def456"),
			},
			expectedReplicas: replicas, // Should create 2 new sandboxes
			expectedAdopted:  0,
		},
		{
			name: "handles mix of owned, orphaned, and foreign sandboxes",
			initialObjs: []runtime.Object{
				template,
				createSandboxWithOwner("abc123", "warmpool-uid-123"), // Owned
				createSandboxWithOwner("def456", ""),                 // Orphaned - should adopt
				createSandboxWithDifferentController("ghi789"),       // Foreign - should ignore
			},
			expectedReplicas: replicas,
			expectedAdopted:  1,
		},
		{
			name: "adopts orphan and creates additional sandbox when under-provisioned",
			initialObjs: []runtime.Object{
				template,
				createSandboxWithOwner("abc123", ""), // Orphaned - should adopt
			},
			expectedReplicas: replicas, // 1 adopted + 1 created
			expectedAdopted:  1,
		},
		{
			name: "deletes excess owned sandboxes but ignores foreign sandboxes",
			initialObjs: []runtime.Object{
				template,
				createSandboxWithOwner("abc123", "warmpool-uid-123"),
				createSandboxWithOwner("def456", "warmpool-uid-123"),
				createSandboxWithOwner("ghi789", "warmpool-uid-123"),
				createSandboxWithDifferentController("jkl012"), // Should be ignored
			},
			expectedReplicas: replicas, // Should delete 1 owned sandbox
			expectedAdopted:  0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := SandboxWarmPoolReconciler{
				Client: fake.NewClientBuilder().
					WithScheme(newTestScheme()).
					WithRuntimeObjects(tc.initialObjs...).
					Build(),
			}

			ctx := context.Background()

			// Run reconcilePool
			err := r.reconcilePool(ctx, warmPool)
			require.NoError(t, err)

			// Run again to ensure idempotency
			err = r.reconcilePool(ctx, warmPool)
			require.NoError(t, err)

			// Verify final state
			list := &sandboxv1alpha1.SandboxList{}
			err = r.List(ctx, list, &client.ListOptions{Namespace: poolNamespace})
			require.NoError(t, err)

			// Count sandboxes with correct pool label and owned by warmpool
			ownedCount := int32(0)
			adoptedCount := 0
			for _, sb := range list.Items {
				if sb.Labels[poolLabel] == poolNameHash {
					controllerRef := metav1.GetControllerOf(&sb)
					if controllerRef != nil && controllerRef.UID == warmPool.UID {
						ownedCount++
						// Check if this was originally an orphan (adopted)
						for _, initialObj := range tc.initialObjs {
							if initialSb, ok := initialObj.(*sandboxv1alpha1.Sandbox); ok {
								if initialSb.Name == sb.Name && len(initialSb.OwnerReferences) == 0 {
									adoptedCount++
									break
								}
							}
						}
					}
				}
			}

			require.Equal(t, tc.expectedReplicas, ownedCount, "owned sandbox count mismatch")
			require.Equal(t, tc.expectedReplicas, warmPool.Status.Replicas, "status replicas mismatch")
		})
	}
}

func boolPtr(b bool) *bool {
	return &b
}

func TestPoolLabelValueInIntegration(t *testing.T) {
	poolName := "test-pool"
	poolNamespace := "default"
	templateName := "test-template"
	replicas := int32(3)

	ctx := context.Background()

	t.Run("all created pods have correct pool label and sandbox template ref label", func(t *testing.T) {
		// Create a SandboxTemplate with labels and annotations
		template := &extensionsv1alpha1.SandboxTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      templateName,
				Namespace: poolNamespace,
				Labels: map[string]string{
					"app":     "test-app",
					"version": "1.0",
				},
				Annotations: map[string]string{
					"description": "test pod",
				},
			},
			Spec: extensionsv1alpha1.SandboxTemplateSpec{
				PodTemplate: sandboxv1alpha1.PodTemplate{
					ObjectMeta: sandboxv1alpha1.PodMetadata{
						Labels: map[string]string{
							"pod-label": "from-podtemplate",
							"version":   "2.0",
						},
						Annotations: map[string]string{
							"pod-annotation": "from-podtemplate",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "test-container",
								Image: "test-image:latest",
							},
						},
					},
				},
			},
		}

		warmPool := &extensionsv1alpha1.SandboxWarmPool{
			ObjectMeta: metav1.ObjectMeta{
				Name:      poolName,
				Namespace: poolNamespace,
				UID:       "warmpool-uid-123",
			},
			Spec: extensionsv1alpha1.SandboxWarmPoolSpec{
				Replicas: replicas,
				TemplateRef: extensionsv1alpha1.SandboxTemplateRef{
					Name: templateName,
				},
			},
		}

		r := SandboxWarmPoolReconciler{
			Client: fake.NewClientBuilder().
				WithScheme(newTestScheme()).
				WithRuntimeObjects(template).
				Build(),
		}

		// Calculate expected pool name hash
		expectedPoolNameHash := sandboxcontrollers.NameHash(poolName)

		// Reconcile
		err := r.reconcilePool(ctx, warmPool)
		require.NoError(t, err)

		// List all pods
		list := &sandboxv1alpha1.SandboxList{}
		err = r.List(ctx, list, &client.ListOptions{Namespace: poolNamespace})
		require.NoError(t, err)
		require.Len(t, list.Items, int(replicas))

		// Verify each sandbox has the correct labels
		for _, sb := range list.Items {
			require.Equal(t, expectedPoolNameHash, sb.Labels[poolLabel],
				"sandbox %s should have correct pool label (pool name hash)", sb.Name)
			require.Equal(t, sandboxcontrollers.NameHash(templateName), sb.Labels[sandboxTemplateRefHash],
				"sandbox %s should have correct sandbox template ref label", sb.Name)

			// Verify labels from pod template are in Spec.PodTemplate
			require.Equal(t, "2.0", sb.Spec.PodTemplate.ObjectMeta.Labels["version"])
			require.Equal(t, "from-podtemplate", sb.Spec.PodTemplate.ObjectMeta.Labels["pod-label"])

			// Verify sandbox template labels are not propagated to Sandbox ObjectMeta
			require.NotContains(t, sb.Labels, "app")

			// Verify annotations from pod template are in Spec.PodTemplate
			require.Equal(t, "from-podtemplate", sb.Spec.PodTemplate.ObjectMeta.Annotations["pod-annotation"])

			// Verify sandbox template metadata annotations are not propagated to Sandbox ObjectMeta
			require.NotContains(t, sb.Annotations, "description")
		}
	})
}

func TestReconcilePoolReadyReplicas(t *testing.T) {
	poolName := "test-pool"
	poolNamespace := "default"
	templateName := "test-template"
	replicas := int32(3)

	// Create a SandboxTemplate
	template := createTemplate(templateName, poolNamespace)

	warmPool := &extensionsv1alpha1.SandboxWarmPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      poolName,
			Namespace: poolNamespace,
		},
		Spec: extensionsv1alpha1.SandboxWarmPoolSpec{
			Replicas: replicas,
			TemplateRef: extensionsv1alpha1.SandboxTemplateRef{
				Name: templateName,
			},
		},
	}

	// Compute the pool name hash
	poolNameHash := sandboxcontrollers.NameHash(poolName)

	createSandboxWithReadyCondition := func(suffix string, ready metav1.ConditionStatus) *sandboxv1alpha1.Sandbox {
		sb := createPoolSandbox(poolName, poolNamespace, poolNameHash, suffix)
		sb.Status.Conditions = []metav1.Condition{
			{
				Type:   string(sandboxv1alpha1.SandboxConditionReady),
				Status: ready,
			},
		}
		return sb
	}

	testCases := []struct {
		name                  string
		initialSandboxes      []runtime.Object
		expectedReadyReplicas int32
	}{
		{
			name: "no sandboxes ready",
			initialSandboxes: []runtime.Object{
				template,
				createSandboxWithReadyCondition("abc123", metav1.ConditionFalse),
				createSandboxWithReadyCondition("def456", metav1.ConditionUnknown),
				createSandboxWithReadyCondition("ghi789", metav1.ConditionFalse),
			},
			expectedReadyReplicas: 0,
		},
		{
			name: "some sandboxes ready",
			initialSandboxes: []runtime.Object{
				template,
				createSandboxWithReadyCondition("abc123", metav1.ConditionTrue),
				createSandboxWithReadyCondition("def456", metav1.ConditionFalse),
				createSandboxWithReadyCondition("ghi789", metav1.ConditionTrue),
			},
			expectedReadyReplicas: 2,
		},
		{
			name: "all sandboxes ready",
			initialSandboxes: []runtime.Object{
				template,
				createSandboxWithReadyCondition("abc123", metav1.ConditionTrue),
				createSandboxWithReadyCondition("def456", metav1.ConditionTrue),
				createSandboxWithReadyCondition("ghi789", metav1.ConditionTrue),
			},
			expectedReadyReplicas: 3,
		},
		{
			name: "sandboxes with no ready condition",
			initialSandboxes: []runtime.Object{
				template,
				createPoolSandbox(poolName, poolNamespace, poolNameHash, "abc123"),
				createPoolSandbox(poolName, poolNamespace, poolNameHash, "def456"),
				createSandboxWithReadyCondition("ghi789", metav1.ConditionTrue),
			},
			expectedReadyReplicas: 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := SandboxWarmPoolReconciler{
				Client: fake.NewClientBuilder().
					WithScheme(newTestScheme()).
					WithRuntimeObjects(tc.initialSandboxes...).
					Build(),
			}

			ctx := context.Background()

			// Run reconcilePool twice to update status
			err := r.reconcilePool(ctx, warmPool)
			require.NoError(t, err)
			err = r.reconcilePool(ctx, warmPool)
			require.NoError(t, err)

			// Verify the ReadyReplicas status
			require.Equal(t, tc.expectedReadyReplicas, warmPool.Status.ReadyReplicas)
		})
	}
}
