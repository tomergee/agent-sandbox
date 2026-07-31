package client

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
)

const pollInterval = 2 * time.Second

// WaitForCondition polls until status.conditions contains condType with
// status True and the observed generation (when reported) has caught up with
// metadata.generation. NotFound during the wait keeps polling — creation may
// still be propagating. On timeout the latest conditions are embedded in the
// error to make the diagnostic actionable.
func (c *Client) WaitForCondition(ctx context.Context, gvr schema.GroupVersionResource, namespace, name, condType string, timeout time.Duration) error {
	var lastConditions []interface{}
	err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		u, err := c.Get(ctx, gvr, namespace, name)
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !generationObserved(u) {
			return false, nil
		}
		conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
		lastConditions = conds
		return conditionTrue(conds, condType), nil
	})
	if err != nil {
		detail, _ := json.Marshal(lastConditions)
		return fmt.Errorf("waiting for %s %s/%s condition %q: %w (last conditions: %s)",
			gvr.Resource, namespace, name, condType, err, string(detail))
	}
	return nil
}

// WaitForWarmPoolReady polls until status.readyReplicas and status.replicas
// both equal the desired replica count.
func (c *Client) WaitForWarmPoolReady(ctx context.Context, namespace, name string, want int64, timeout time.Duration) error {
	var lastReady, lastTotal int64
	err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		u, err := c.Get(ctx, SandboxWarmPoolGVR, namespace, name)
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !generationObserved(u) {
			return false, nil
		}
		lastReady, _, _ = unstructured.NestedInt64(u.Object, "status", "readyReplicas")
		lastTotal, _, _ = unstructured.NestedInt64(u.Object, "status", "replicas")
		return lastReady == want && lastTotal == want, nil
	})
	if err != nil {
		return fmt.Errorf("waiting for sandboxwarmpool %s/%s to reach %d ready replicas (ready=%d, total=%d): %w",
			namespace, name, want, lastReady, lastTotal, err)
	}
	return nil
}

// WaitForDeleted polls until the object is gone.
func (c *Client) WaitForDeleted(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string, timeout time.Duration) error {
	err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		_, err := c.Get(ctx, gvr, namespace, name)
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
	if err != nil {
		return fmt.Errorf("waiting for %s %s/%s to be deleted: %w", gvr.Resource, namespace, name, err)
	}
	return nil
}

// WaitForCRDEstablished polls the CRD until its Established condition is True.
func (c *Client) WaitForCRDEstablished(ctx context.Context, name string, timeout time.Duration) error {
	err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		u, err := c.Dynamic.Resource(CRDGVR).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
		return conditionTrue(conds, "Established"), nil
	})
	if err != nil {
		return fmt.Errorf("waiting for CRD %s to be established: %w", name, err)
	}
	return nil
}

// WaitForDeploymentAvailable polls until the deployment reports Available and
// all replicas are updated.
func (c *Client) WaitForDeploymentAvailable(ctx context.Context, namespace, name string, timeout time.Duration) error {
	err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		dep, err := c.Typed.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if dep.Generation != dep.Status.ObservedGeneration {
			return false, nil
		}
		want := int32(1)
		if dep.Spec.Replicas != nil {
			want = *dep.Spec.Replicas
		}
		if dep.Status.UpdatedReplicas != want || dep.Status.AvailableReplicas != want {
			return false, nil
		}
		for _, cond := range dep.Status.Conditions {
			if cond.Type == appsv1.DeploymentAvailable {
				return cond.Status == "True", nil
			}
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("waiting for deployment %s/%s to become available: %w", namespace, name, err)
	}
	return nil
}

func conditionTrue(conds []interface{}, condType string) bool {
	for _, raw := range conds {
		cond, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if cond["type"] == condType {
			return cond["status"] == "True"
		}
	}
	return false
}

// generationObserved reports whether status.observedGeneration (when the
// resource exposes it) has caught up with metadata.generation, guarding
// against acting on a stale Ready condition right after an update.
func generationObserved(u *unstructured.Unstructured) bool {
	observed, found, _ := unstructured.NestedInt64(u.Object, "status", "observedGeneration")
	if !found {
		return true
	}
	return observed >= u.GetGeneration()
}
