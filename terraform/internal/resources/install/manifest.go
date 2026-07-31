package install

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	sigsyaml "sigs.k8s.io/yaml"
)

// ManifestURL builds the release-asset URL for a given version and manifest
// flavor, unless an explicit override URL is supplied.
func ManifestURL(version, manifest, override string) string {
	if override != "" {
		return override
	}
	return fmt.Sprintf("https://github.com/kubernetes-sigs/agent-sandbox/releases/download/%s/%s.yaml", version, manifest)
}

// FetchManifest downloads the manifest with a bounded timeout.
func FetchManifest(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching %s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// ParseManifest splits a multi-document YAML manifest into unstructured
// objects, skipping empty documents and rejecting documents that are not
// Kubernetes objects.
func ParseManifest(data []byte) ([]*unstructured.Unstructured, error) {
	var objs []*unstructured.Unstructured
	reader := k8syaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
	for i := 0; ; i++ {
		doc, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading document %d: %w", i, err)
		}
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}
		var obj map[string]interface{}
		if err := sigsyaml.Unmarshal(doc, &obj); err != nil {
			return nil, fmt.Errorf("parsing document %d: %w", i, err)
		}
		if len(obj) == 0 {
			continue
		}
		u := &unstructured.Unstructured{Object: obj}
		if u.GetAPIVersion() == "" || u.GetKind() == "" {
			return nil, fmt.Errorf("document %d is missing apiVersion or kind", i)
		}
		objs = append(objs, u)
	}
	return objs, nil
}

// kindWeight orders objects so dependencies apply before dependents; delete
// runs in reverse.
func kindWeight(kind string) int {
	switch kind {
	case "Namespace":
		return 0
	case "CustomResourceDefinition":
		return 1
	case "ServiceAccount", "ClusterRole", "Role":
		return 2
	case "ClusterRoleBinding", "RoleBinding":
		return 3
	case "ConfigMap", "Secret", "Service":
		return 4
	case "Deployment", "StatefulSet", "DaemonSet":
		return 6
	case "MutatingWebhookConfiguration", "ValidatingWebhookConfiguration":
		return 7
	default:
		return 5
	}
}

// SortForApply orders objects for creation (stable within equal weights).
func SortForApply(objs []*unstructured.Unstructured) {
	sort.SliceStable(objs, func(i, j int) bool {
		return kindWeight(objs[i].GetKind()) < kindWeight(objs[j].GetKind())
	})
}

// ObjectKey renders the stable identity used in the applied_objects state:
// "apiVersion|Kind|namespace|name" (namespace empty for cluster-scoped).
// "|" cannot appear in any of the segments, so parsing is unambiguous even
// though apiVersion itself may contain "/".
func ObjectKey(u *unstructured.Unstructured) string {
	return fmt.Sprintf("%s|%s|%s|%s", u.GetAPIVersion(), u.GetKind(), u.GetNamespace(), u.GetName())
}

// ParseObjectKey is the inverse of ObjectKey.
func ParseObjectKey(key string) (apiVersion, kind, namespace, name string, err error) {
	parts := strings.Split(key, "|")
	if len(parts) != 4 {
		return "", "", "", "", fmt.Errorf("invalid object key %q", key)
	}
	return parts[0], parts[1], parts[2], parts[3], nil
}
