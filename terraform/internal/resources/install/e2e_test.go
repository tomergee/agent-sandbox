package install

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"

	"sigs.k8s.io/agent-sandbox/terraform/internal/client"
)

// TestE2E is an end-to-end test against a real cluster covering the full
// resource lifecycle:
//
//	(optional) controller install -> Sandbox Ready -> SandboxTemplate ->
//	SandboxWarmPool ready -> SandboxClaim Ready -> teardown
//
// Environment:
//
//	AGENTSANDBOX_E2E=1           run the test (skipped otherwise)
//	AGENTSANDBOX_CONTEXT         kubeconfig context (default: current)
//	AGENTSANDBOX_E2E_INSTALL=1   run the controller install phase first
//	AGENTSANDBOX_VERSION         release to install (default v0.5.4)
//	AGENTSANDBOX_E2E_NAMESPACE   namespace for test CRs (default "tf-e2e";
//	                             created and deleted unless it is "default")
//	AGENTSANDBOX_E2E_IMAGE       sandbox container image (default pause)
func TestE2E(t *testing.T) {
	if os.Getenv("AGENTSANDBOX_E2E") != "1" {
		t.Skip("set AGENTSANDBOX_E2E=1 to run against the current kubeconfig context")
	}

	ctx := context.Background()
	c, err := client.New(client.Config{Context: os.Getenv("AGENTSANDBOX_CONTEXT")})
	if err != nil {
		t.Fatalf("building client: %v", err)
	}

	ns := envOr("AGENTSANDBOX_E2E_NAMESPACE", "tf-e2e")
	image := envOr("AGENTSANDBOX_E2E_IMAGE", "registry.k8s.io/pause:3.10")

	if os.Getenv("AGENTSANDBOX_E2E_INSTALL") == "1" {
		installController(ctx, t, c)
	}

	if ns != "default" {
		_, err := c.Typed.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: ns},
		}, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatalf("creating namespace %s: %v", ns, err)
		}
		t.Cleanup(func() {
			_ = c.Typed.CoreV1().Namespaces().Delete(ctx, ns, metav1.DeleteOptions{})
		})
	}

	podSpec := map[string]interface{}{
		"containers": []interface{}{
			map[string]interface{}{"name": "runtime", "image": image},
		},
	}

	t.Run("Sandbox", func(t *testing.T) {
		name := "tf-e2e-sandbox"
		obj := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": client.SandboxAPIVersion,
			"kind":       "Sandbox",
			"metadata":   map[string]interface{}{"name": name, "namespace": ns},
			"spec": map[string]interface{}{
				"podTemplate": map[string]interface{}{"spec": podSpec},
			},
		}}
		if _, err := c.Apply(ctx, client.SandboxGVR, obj); err != nil {
			t.Fatalf("applying sandbox: %v", err)
		}
		t.Cleanup(func() { _ = c.Delete(ctx, client.SandboxGVR, ns, name) })

		if err := c.WaitForCondition(ctx, client.SandboxGVR, ns, name, "Ready", 8*time.Minute); err != nil {
			t.Fatalf("sandbox not ready: %v", err)
		}
		live, err := c.Get(ctx, client.SandboxGVR, ns, name)
		if err != nil {
			t.Fatal(err)
		}
		ips, _, _ := unstructured.NestedStringSlice(live.Object, "status", "podIPs")
		t.Logf("sandbox ready, podIPs=%v", ips)
	})

	t.Run("TemplateWarmPoolClaim", func(t *testing.T) {
		tmplName, poolName, claimName := "tf-e2e-template", "tf-e2e-pool", "tf-e2e-claim"

		tmpl := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": client.ExtensionsAPIVersion,
			"kind":       "SandboxTemplate",
			"metadata":   map[string]interface{}{"name": tmplName, "namespace": ns},
			"spec": map[string]interface{}{
				"podTemplate":            map[string]interface{}{"spec": podSpec},
				"envVarsInjectionPolicy": "Allowed",
			},
		}}
		if _, err := c.Apply(ctx, client.SandboxTemplateGVR, tmpl); err != nil {
			t.Fatalf("applying template: %v", err)
		}
		t.Cleanup(func() { _ = c.Delete(ctx, client.SandboxTemplateGVR, ns, tmplName) })

		pool := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": client.ExtensionsAPIVersion,
			"kind":       "SandboxWarmPool",
			"metadata":   map[string]interface{}{"name": poolName, "namespace": ns},
			"spec": map[string]interface{}{
				"replicas":           int64(1),
				"sandboxTemplateRef": map[string]interface{}{"name": tmplName},
			},
		}}
		if _, err := c.Apply(ctx, client.SandboxWarmPoolGVR, pool); err != nil {
			t.Fatalf("applying warmpool: %v", err)
		}
		t.Cleanup(func() { _ = c.Delete(ctx, client.SandboxWarmPoolGVR, ns, poolName) })

		if err := c.WaitForWarmPoolReady(ctx, ns, poolName, 1, 8*time.Minute); err != nil {
			t.Fatalf("warmpool not ready: %v", err)
		}
		t.Log("warmpool ready")

		claimObj := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": client.ExtensionsAPIVersion,
			"kind":       "SandboxClaim",
			"metadata":   map[string]interface{}{"name": claimName, "namespace": ns},
			"spec": map[string]interface{}{
				"warmPoolRef": map[string]interface{}{"name": poolName},
				"env": []interface{}{
					map[string]interface{}{"name": "SESSION_ID", "value": "e2e"},
				},
			},
		}}
		if _, err := c.Apply(ctx, client.SandboxClaimGVR, claimObj); err != nil {
			t.Fatalf("applying claim: %v", err)
		}
		t.Cleanup(func() { _ = c.Delete(ctx, client.SandboxClaimGVR, ns, claimName) })

		if err := c.WaitForCondition(ctx, client.SandboxClaimGVR, ns, claimName, "Ready", 8*time.Minute); err != nil {
			t.Fatalf("claim not ready: %v", err)
		}
		live, err := c.Get(ctx, client.SandboxClaimGVR, ns, claimName)
		if err != nil {
			t.Fatal(err)
		}
		fqdn, _, _ := unstructured.NestedString(live.Object, "status", "sandbox", "serviceFQDN")
		ips, _, _ := unstructured.NestedStringSlice(live.Object, "status", "sandbox", "podIPs")
		t.Logf("claim ready, serviceFQDN=%q podIPs=%v", fqdn, ips)
	})

	t.Run("Teardown", func(t *testing.T) {
		// Deletions are registered as cleanups above; here we just verify the
		// sandbox actually disappears (cascade through claim/pool is async).
		if err := c.Delete(ctx, client.SandboxGVR, ns, "tf-e2e-sandbox"); err != nil {
			t.Fatalf("deleting sandbox: %v", err)
		}
		if err := c.WaitForDeleted(ctx, client.SandboxGVR, ns, "tf-e2e-sandbox", 4*time.Minute); err != nil {
			t.Fatalf("sandbox not deleted: %v", err)
		}
		t.Log("sandbox deleted")
	})
}

func installController(ctx context.Context, t *testing.T, c *client.Client) {
	t.Helper()
	r := &Resource{client: c}
	plan := &model{
		Version:        types.StringValue(envOr("AGENTSANDBOX_VERSION", "v0.5.4")),
		Manifest:       types.StringValue("sandbox-with-extensions"),
		ManifestURL:    types.StringNull(),
		FieldManager:   types.StringValue(client.FieldManager),
		ForceConflicts: types.BoolValue(true),
		Wait:           types.BoolValue(true),
	}
	var diags diag.Diagnostics
	applied := r.installManifest(ctx, plan, 10*time.Minute, &diags)
	if diags.HasError() {
		t.Fatalf("install failed: %v", diags.Errors())
	}
	t.Logf("installed controller: %d objects applied", len(applied))

	// The conversion webhook needs a moment after the deployment reports
	// available before it serves; retry a trivial list until it responds.
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		_, err := c.Dynamic.Resource(client.SandboxGVR).Namespace("default").List(ctx, metav1.ListOptions{Limit: 1})
		return err == nil, nil
	})
	if err != nil {
		t.Fatalf("sandbox API not serving after install: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
