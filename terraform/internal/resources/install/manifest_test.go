package install

import (
	"os"
	"testing"
)

func loadFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/sandbox-with-extensions.yaml")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	return data
}

func TestParseManifestFixture(t *testing.T) {
	objs, err := ParseManifest(loadFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) == 0 {
		t.Fatal("no objects parsed")
	}

	kinds := map[string]int{}
	for _, obj := range objs {
		if obj.GetKind() == "" || obj.GetAPIVersion() == "" {
			t.Errorf("object with empty kind/apiVersion: %v", obj)
		}
		kinds[obj.GetKind()]++
	}
	// The release manifest must at least carry the namespace, the four CRDs,
	// and a controller deployment.
	if kinds["Namespace"] < 1 {
		t.Errorf("expected a Namespace, got kinds %v", kinds)
	}
	if kinds["CustomResourceDefinition"] < 4 {
		t.Errorf("expected >=4 CRDs, got %d", kinds["CustomResourceDefinition"])
	}
	if kinds["Deployment"] < 1 {
		t.Errorf("expected >=1 Deployment, got kinds %v", kinds)
	}
}

func TestSortForApplyOrdering(t *testing.T) {
	objs, err := ParseManifest(loadFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	SortForApply(objs)

	lastWeight := -1
	for _, obj := range objs {
		w := kindWeight(obj.GetKind())
		if w < lastWeight {
			t.Fatalf("object %s %s out of order (weight %d after %d)", obj.GetKind(), obj.GetName(), w, lastWeight)
		}
		lastWeight = w
	}
	if objs[0].GetKind() != "Namespace" {
		t.Errorf("first object should be Namespace, got %s", objs[0].GetKind())
	}
}

func TestObjectKeyRoundTrip(t *testing.T) {
	objs, err := ParseManifest(loadFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range objs {
		key := ObjectKey(obj)
		apiVersion, kind, ns, name, err := ParseObjectKey(key)
		if err != nil {
			t.Fatalf("round-tripping %q: %v", key, err)
		}
		if apiVersion != obj.GetAPIVersion() || kind != obj.GetKind() || ns != obj.GetNamespace() || name != obj.GetName() {
			t.Errorf("round-trip mismatch for %q", key)
		}
	}
}

func TestParseManifestRejectsNonObjects(t *testing.T) {
	if _, err := ParseManifest([]byte("foo: bar\n")); err == nil {
		t.Error("expected error for document without kind/apiVersion")
	}
}

func TestManifestURL(t *testing.T) {
	got := ManifestURL("v0.5.4", "sandbox-with-extensions", "")
	want := "https://github.com/kubernetes-sigs/agent-sandbox/releases/download/v0.5.4/sandbox-with-extensions.yaml"
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if got := ManifestURL("v0.5.4", "sandbox", "https://mirror.example/x.yaml"); got != "https://mirror.example/x.yaml" {
		t.Errorf("override ignored: %s", got)
	}
}

func TestDiffAppliedSets(t *testing.T) {
	prior := []string{"a", "b", "c"}
	current := []string{"b", "c", "d"}
	removed := DiffAppliedSets(prior, current)
	if len(removed) != 1 || removed[0] != "a" {
		t.Errorf("removed = %v, want [a]", removed)
	}
	if removed := DiffAppliedSets(nil, current); removed != nil {
		t.Errorf("removed from nil prior = %v", removed)
	}
}
