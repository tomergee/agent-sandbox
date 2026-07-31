package install

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sschema "k8s.io/apimachinery/pkg/runtime/schema"

	"sigs.k8s.io/agent-sandbox/terraform/internal/client"
)

// TestE2EUpgrade upgrades a live agent-sandbox installation the way a
// `version` change on agentsandbox_install would: SSA-apply the new release
// manifest, then prune objects the new manifest no longer contains. It
// verifies that pre-existing workloads (sandboxes, warm pools) survive the
// upgrade and that the upgraded controller still reconciles new sandboxes.
//
// Environment:
//
//	AGENTSANDBOX_E2E_UPGRADE=1    run the test (skipped otherwise)
//	AGENTSANDBOX_CONTEXT          kubeconfig context (default: current)
//	AGENTSANDBOX_UPGRADE_FROM     version currently installed / to install first (default v0.5.1)
//	AGENTSANDBOX_UPGRADE_TO       target version (default v0.5.4)
//	AGENTSANDBOX_E2E_INSTALL=1    install FROM first (fresh clusters, e.g. kind)
func TestE2EUpgrade(t *testing.T) {
	if os.Getenv("AGENTSANDBOX_E2E_UPGRADE") != "1" {
		t.Skip("set AGENTSANDBOX_E2E_UPGRADE=1 to run against the current kubeconfig context")
	}

	ctx := context.Background()
	c, err := client.New(client.Config{Context: os.Getenv("AGENTSANDBOX_CONTEXT")})
	if err != nil {
		t.Fatalf("building client: %v", err)
	}
	r := &Resource{client: c}

	from := envOr("AGENTSANDBOX_UPGRADE_FROM", "v0.5.1")
	to := envOr("AGENTSANDBOX_UPGRADE_TO", "v0.5.4")

	// Prior applied set = what a Terraform state would hold after installing
	// FROM: parse the FROM release manifests without applying them.
	var prior []string
	for _, asset := range assetNamesFor(from) {
		url := fmt.Sprintf("https://github.com/kubernetes-sigs/agent-sandbox/releases/download/%s/%s", from, asset)
		data, err := FetchManifest(ctx, url)
		if err != nil {
			t.Fatalf("fetching %s: %v", url, err)
		}
		objs, err := ParseManifest(data)
		if err != nil {
			t.Fatalf("parsing %s: %v", url, err)
		}
		for _, obj := range objs {
			prior = append(prior, ObjectKey(obj))
		}
	}

	if os.Getenv("AGENTSANDBOX_E2E_INSTALL") == "1" {
		for _, asset := range assetNamesFor(from) {
			plan := &model{
				Version:        types.StringValue(from),
				Manifest:       types.StringValue(strings.TrimSuffix(asset, ".yaml")),
				ManifestURL:    types.StringValue(fmt.Sprintf("https://github.com/kubernetes-sigs/agent-sandbox/releases/download/%s/%s", from, asset)),
				FieldManager:   types.StringValue(client.FieldManager),
				ForceConflicts: types.BoolValue(true),
				Wait:           types.BoolValue(true),
			}
			var diags diag.Diagnostics
			r.installManifest(ctx, plan, 10*time.Minute, &diags)
			if diags.HasError() {
				t.Fatalf("installing %s %s: %v", from, asset, diags.Errors())
			}
		}
		t.Logf("installed %s", from)
	}

	// On a fresh cluster (kind path), create a workload under the FROM
	// version so the preservation checks below actually exercise something.
	if os.Getenv("AGENTSANDBOX_E2E_INSTALL") == "1" {
		image := envOr("AGENTSANDBOX_E2E_IMAGE", "registry.k8s.io/pause:3.10")
		podSpec := map[string]interface{}{
			"containers": []interface{}{
				map[string]interface{}{"name": "runtime", "image": image},
			},
		}
		tmpl := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": client.ExtensionsAPIVersion,
			"kind":       "SandboxTemplate",
			"metadata":   map[string]interface{}{"name": "tf-e2e-upgrade-tmpl", "namespace": "default"},
			"spec":       map[string]interface{}{"podTemplate": map[string]interface{}{"spec": podSpec}},
		}}
		if _, err := c.Apply(ctx, client.SandboxTemplateGVR, tmpl); err != nil {
			t.Fatalf("applying pre-upgrade template: %v", err)
		}
		t.Cleanup(func() { _ = c.Delete(ctx, client.SandboxTemplateGVR, "default", "tf-e2e-upgrade-tmpl") })

		pool := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": client.ExtensionsAPIVersion,
			"kind":       "SandboxWarmPool",
			"metadata":   map[string]interface{}{"name": "tf-e2e-upgrade-pool", "namespace": "default"},
			"spec": map[string]interface{}{
				"replicas":           int64(1),
				"sandboxTemplateRef": map[string]interface{}{"name": "tf-e2e-upgrade-tmpl"},
			},
		}}
		if _, err := c.Apply(ctx, client.SandboxWarmPoolGVR, pool); err != nil {
			t.Fatalf("applying pre-upgrade warmpool: %v", err)
		}
		t.Cleanup(func() { _ = c.Delete(ctx, client.SandboxWarmPoolGVR, "default", "tf-e2e-upgrade-pool") })
		if err := c.WaitForWarmPoolReady(ctx, "default", "tf-e2e-upgrade-pool", 1, 8*time.Minute); err != nil {
			t.Fatalf("pre-upgrade warmpool not ready: %v", err)
		}
		t.Log("pre-upgrade workload ready")
	}

	// Snapshot every pre-existing sandbox and warm pool so we can prove the
	// upgrade did not disturb them.
	preSandboxes := listNames(ctx, t, c, client.SandboxGVR)
	preWarmPools := listNames(ctx, t, c, client.SandboxWarmPoolGVR)
	t.Logf("pre-upgrade: %d sandboxes, %d warmpools", len(preSandboxes), len(preWarmPools))

	// Upgrade: exactly what Update() does — apply TO, then prune.
	plan := &model{
		Version:        types.StringValue(to),
		Manifest:       types.StringValue("sandbox-with-extensions"),
		ManifestURL:    types.StringNull(),
		FieldManager:   types.StringValue(client.FieldManager),
		ForceConflicts: types.BoolValue(true),
		Wait:           types.BoolValue(true),
	}
	var diags diag.Diagnostics
	applied := r.installManifest(ctx, plan, 10*time.Minute, &diags)
	if diags.HasError() {
		t.Fatalf("upgrade to %s failed: %v", to, diags.Errors())
	}
	pruned := DiffAppliedSets(prior, applied)
	for _, key := range pruned {
		if err := r.deleteByKey(ctx, key); err != nil {
			t.Errorf("pruning %s: %v", key, err)
		}
	}
	t.Logf("upgraded to %s: %d objects applied, %d pruned (%v)", to, len(applied), len(pruned), pruned)

	// Controller deployments must be running the target version.
	deps, err := c.Typed.AppsV1().Deployments(installNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(deps.Items) == 0 {
		t.Fatal("no controller deployments found after upgrade")
	}
	for _, dep := range deps.Items {
		for _, container := range dep.Spec.Template.Spec.Containers {
			if !strings.HasSuffix(container.Image, ":"+to) {
				t.Errorf("deployment %s container %s image %s does not match %s", dep.Name, container.Name, container.Image, to)
			}
		}
	}

	// Every pre-existing sandbox and warm pool must still exist.
	for key := range preSandboxes {
		ns, name, _ := strings.Cut(key, "/")
		if _, err := c.Get(ctx, client.SandboxGVR, ns, name); err != nil {
			t.Errorf("pre-existing sandbox %s lost after upgrade: %v", key, err)
		}
	}
	for key, want := range preWarmPools {
		ns, name, _ := strings.Cut(key, "/")
		live, err := c.Get(ctx, client.SandboxWarmPoolGVR, ns, name)
		if err != nil {
			t.Errorf("pre-existing warmpool %s lost after upgrade: %v", key, err)
			continue
		}
		replicas, _, _ := unstructured.NestedInt64(live.Object, "spec", "replicas")
		if replicas != want {
			t.Errorf("warmpool %s replicas changed: %d -> %d", key, want, replicas)
		}
		if err := c.WaitForWarmPoolReady(ctx, ns, name, replicas, 8*time.Minute); err != nil {
			t.Errorf("warmpool %s not ready after upgrade: %v", key, err)
		}
	}

	// The upgraded controller must still reconcile new work.
	name := "tf-e2e-upgrade-probe"
	probe := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": client.SandboxAPIVersion,
		"kind":       "Sandbox",
		"metadata":   map[string]interface{}{"name": name, "namespace": "default"},
		"spec": map[string]interface{}{
			"podTemplate": map[string]interface{}{
				"spec": map[string]interface{}{
					"containers": []interface{}{
						map[string]interface{}{"name": "runtime", "image": envOr("AGENTSANDBOX_E2E_IMAGE", "registry.k8s.io/pause:3.10")},
					},
				},
			},
		},
	}}
	if _, err := c.Apply(ctx, client.SandboxGVR, probe); err != nil {
		t.Fatalf("applying probe sandbox: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(ctx, client.SandboxGVR, "default", name) })
	if err := c.WaitForCondition(ctx, client.SandboxGVR, "default", name, "Ready", 8*time.Minute); err != nil {
		t.Fatalf("probe sandbox not ready on upgraded controller: %v", err)
	}
	t.Log("probe sandbox ready on upgraded controller")
}

// assetNamesFor returns the release asset names for a version: releases
// before v0.5.2 shipped manifest.yaml + extensions.yaml, later ones ship the
// combined sandbox-with-extensions.yaml.
func assetNamesFor(version string) []string {
	if olderThan(version, 0, 5, 2) {
		return []string{"manifest.yaml", "extensions.yaml"}
	}
	return []string{"sandbox-with-extensions.yaml"}
}

func olderThan(version string, major, minor, patch int) bool {
	parts := strings.SplitN(strings.TrimPrefix(version, "v"), "-", 2)
	nums := strings.Split(parts[0], ".")
	if len(nums) != 3 {
		return false
	}
	v := make([]int, 3)
	for i, s := range nums {
		n, err := strconv.Atoi(s)
		if err != nil {
			return false
		}
		v[i] = n
	}
	want := []int{major, minor, patch}
	for i := range v {
		if v[i] != want[i] {
			return v[i] < want[i]
		}
	}
	return false
}

// listNames returns "namespace/name" -> spec.replicas (0 for kinds without
// replicas) for all objects of the given resource across the cluster.
func listNames(ctx context.Context, t *testing.T, c *client.Client, gvr k8sschema.GroupVersionResource) map[string]int64 {
	t.Helper()
	list, err := c.Dynamic.Resource(gvr).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing %s: %v", gvr.Resource, err)
	}
	out := map[string]int64{}
	for _, item := range list.Items {
		replicas, _, _ := unstructured.NestedInt64(item.Object, "spec", "replicas")
		out[item.GetNamespace()+"/"+item.GetName()] = replicas
	}
	return out
}
