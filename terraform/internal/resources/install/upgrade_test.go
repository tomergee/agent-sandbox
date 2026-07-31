package install

import (
	"os"
	"testing"
)

// TestUpgradeDiff_v051_to_v054 pins the prune behavior of a v0.5.1 -> v0.5.4
// upgrade: the object sets are compared exactly the way Update does it, so
// this test documents precisely what an upgrade deletes.
func TestUpgradeDiff_v051_to_v054(t *testing.T) {
	prior := manifestKeys(t, "testdata/manifest-v0.5.1.yaml", "testdata/extensions-v0.5.1.yaml")
	current := manifestKeys(t, "testdata/sandbox-with-extensions.yaml")

	removed := DiffAppliedSets(prior, current)
	added := DiffAppliedSets(current, prior)

	t.Logf("pruned on upgrade: %v", removed)
	t.Logf("added on upgrade: %v", added)

	// The upgrade must never prune CRDs, the namespace, or the service —
	// those carry state or serve traffic for existing workloads.
	for _, key := range removed {
		_, kind, _, _, err := ParseObjectKey(key)
		if err != nil {
			t.Fatal(err)
		}
		switch kind {
		case "CustomResourceDefinition", "Namespace", "Service":
			t.Errorf("upgrade would prune %s — this breaks existing workloads", key)
		}
	}
}

func manifestKeys(t *testing.T, paths ...string) []string {
	t.Helper()
	var keys []string
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		objs, err := ParseManifest(data)
		if err != nil {
			t.Fatal(err)
		}
		for _, obj := range objs {
			keys = append(keys, ObjectKey(obj))
		}
	}
	return keys
}
