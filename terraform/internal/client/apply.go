package client

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
)

// Apply server-side-applies obj under our field manager. Create and Update
// share this path. If the CRD is not yet registered (install ordering race),
// the call is retried for up to crdRetryWindow before giving up.
func (c *Client) Apply(ctx context.Context, gvr schema.GroupVersionResource, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	const crdRetryWindow = 30 * time.Second

	var out *unstructured.Unstructured
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, crdRetryWindow, true, func(ctx context.Context) (bool, error) {
		var applyErr error
		out, applyErr = c.Dynamic.Resource(gvr).Namespace(obj.GetNamespace()).Apply(
			ctx, obj.GetName(), obj, metav1.ApplyOptions{FieldManager: FieldManager, Force: true},
		)
		if applyErr == nil {
			return true, nil
		}
		// The CRD may not be established yet right after an install.
		if meta := apierrors.IsNotFound(applyErr) || isNoKindMatch(applyErr); meta {
			return false, nil
		}
		return false, applyErr
	})
	if err != nil {
		return nil, fmt.Errorf("applying %s %s/%s: %w", gvr.Resource, obj.GetNamespace(), obj.GetName(), err)
	}
	return out, nil
}

func isNoKindMatch(err error) bool {
	if err == nil {
		return false
	}
	statusErr, ok := err.(*apierrors.StatusError)
	if !ok {
		return false
	}
	return statusErr.ErrStatus.Reason == metav1.StatusReasonNotFound ||
		statusErr.ErrStatus.Code == 404
}

// Get fetches a namespaced object; callers should check apierrors.IsNotFound.
func (c *Client) Get(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	return c.Dynamic.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
}

// Delete removes the object with foreground propagation and tolerates
// objects that are already gone.
func (c *Client) Delete(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error {
	policy := metav1.DeletePropagationForeground
	err := c.Dynamic.Resource(gvr).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &policy})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
