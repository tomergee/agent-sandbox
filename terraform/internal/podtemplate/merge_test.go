package podtemplate

import (
	"reflect"
	"testing"
)

func basePodSpec() map[string]interface{} {
	return map[string]interface{}{
		"containers": []interface{}{
			map[string]interface{}{
				"name":    "main",
				"image":   "python:3.12-slim",
				"command": []interface{}{"sleep", "infinity"},
				"env": []interface{}{
					map[string]interface{}{"name": "FOO", "value": "bar"},
				},
			},
		},
	}
}

func TestMergeOverridesEmpty(t *testing.T) {
	base := basePodSpec()
	got, err := MergeOverrides(base, "   ")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, base) {
		t.Errorf("expected base unchanged, got %v", got)
	}
}

func TestMergeOverridesAddScalarField(t *testing.T) {
	got, err := MergeOverrides(basePodSpec(), "runtimeClassName: gvisor\n")
	if err != nil {
		t.Fatal(err)
	}
	if got["runtimeClassName"] != "gvisor" {
		t.Errorf("runtimeClassName = %v, want gvisor", got["runtimeClassName"])
	}
	// Typed containers must survive.
	containers := got["containers"].([]interface{})
	if len(containers) != 1 {
		t.Fatalf("containers = %v", containers)
	}
}

func TestMergeOverridesContainerMergeByName(t *testing.T) {
	override := `
containers:
  - name: main
    resources:
      limits:
        cpu: "1"
`
	got, err := MergeOverrides(basePodSpec(), override)
	if err != nil {
		t.Fatal(err)
	}
	containers := got["containers"].([]interface{})
	if len(containers) != 1 {
		t.Fatalf("expected container merge by name, got %d containers", len(containers))
	}
	c := containers[0].(map[string]interface{})
	if c["image"] != "python:3.12-slim" {
		t.Errorf("image lost in merge: %v", c["image"])
	}
	limits := c["resources"].(map[string]interface{})["limits"].(map[string]interface{})
	if limits["cpu"] != "1" {
		t.Errorf("cpu limit = %v", limits["cpu"])
	}
}

func TestMergeOverridesEnvMergeByName(t *testing.T) {
	override := `
containers:
  - name: main
    env:
      - name: BAZ
        value: qux
`
	got, err := MergeOverrides(basePodSpec(), override)
	if err != nil {
		t.Fatal(err)
	}
	c := got["containers"].([]interface{})[0].(map[string]interface{})
	env := c["env"].([]interface{})
	if len(env) != 2 {
		t.Fatalf("expected env merged by name (FOO + BAZ), got %v", env)
	}
}

func TestMergeOverridesUnkeyedListReplaced(t *testing.T) {
	override := `
containers:
  - name: main
    command: ["python", "-m", "http.server"]
`
	got, err := MergeOverrides(basePodSpec(), override)
	if err != nil {
		t.Fatal(err)
	}
	c := got["containers"].([]interface{})[0].(map[string]interface{})
	cmd := c["command"].([]interface{})
	if len(cmd) != 3 || cmd[0] != "python" {
		t.Errorf("command should be replaced wholesale, got %v", cmd)
	}
}

func TestMergeOverridesTolerationsAndNodeSelector(t *testing.T) {
	override := `
nodeSelector:
  cloud.google.com/gke-sandbox: "true"
tolerations:
  - key: sandbox.gke.io/runtime
    operator: Equal
    value: gvisor
    effect: NoSchedule
`
	got, err := MergeOverrides(basePodSpec(), override)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["nodeSelector"]; !ok {
		t.Error("nodeSelector missing after merge")
	}
	if _, ok := got["tolerations"]; !ok {
		t.Error("tolerations missing after merge")
	}
}

func TestMergeOverridesNullDeletes(t *testing.T) {
	base := basePodSpec()
	base["serviceAccountName"] = "special"
	got, err := MergeOverrides(base, "serviceAccountName: null\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["serviceAccountName"]; ok {
		t.Errorf("null override should delete serviceAccountName, got %v", got["serviceAccountName"])
	}
}

func TestMergeOverridesInvalidYAML(t *testing.T) {
	_, err := MergeOverrides(basePodSpec(), "{invalid: [yaml")
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}
