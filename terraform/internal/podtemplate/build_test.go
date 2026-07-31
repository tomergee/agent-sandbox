package podtemplate

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// buildModel constructs a minimal typed model: one container with image,
// command, env, and resources, plus an override adding runtimeClassName.
func buildModel(t *testing.T, overrides string) Model {
	t.Helper()

	env, d := types.ListValue(types.ObjectType{AttrTypes: EnvVarAttrTypes}, []attr.Value{
		mustObject(t, EnvVarAttrTypes, map[string]attr.Value{
			"name":               types.StringValue("FOO"),
			"value":              types.StringValue("bar"),
			"config_map_key_ref": types.ObjectNull(KeyRefAttrTypes),
			"secret_key_ref":     types.ObjectNull(KeyRefAttrTypes),
			"field_ref":          types.ObjectNull(FieldRefAttrTypes),
		}),
	})
	if d.HasError() {
		t.Fatal(d)
	}

	command, _ := types.ListValue(types.StringType, []attr.Value{
		types.StringValue("sleep"), types.StringValue("infinity"),
	})

	container := mustObject(t, ContainerAttrTypes, map[string]attr.Value{
		"name":              types.StringValue("main"),
		"image":             types.StringValue("python:3.12-slim"),
		"command":           command,
		"args":              types.ListNull(types.StringType),
		"working_dir":       types.StringNull(),
		"image_pull_policy": types.StringNull(),
		"env":               env,
		"ports":             types.ListNull(types.ObjectType{AttrTypes: PortAttrTypes}),
		"resources":         types.ObjectNull(ResourcesAttrTypes),
		"volume_mounts":     types.ListNull(types.ObjectType{AttrTypes: VolumeMountAttrTypes}),
	})

	containers, d := types.ListValue(types.ObjectType{AttrTypes: ContainerAttrTypes}, []attr.Value{container})
	if d.HasError() {
		t.Fatal(d)
	}

	specOverrides := types.StringNull()
	if overrides != "" {
		specOverrides = types.StringValue(overrides)
	}

	return Model{
		Metadata:           types.ObjectNull(MetadataAttrTypes),
		Containers:         containers,
		Volumes:            types.ListNull(types.ObjectType{AttrTypes: VolumeAttrTypes}),
		RestartPolicy:      types.StringNull(),
		ServiceAccountName: types.StringNull(),
		SpecOverrides:      specOverrides,
	}
}

func mustObject(t *testing.T, attrTypes map[string]attr.Type, values map[string]attr.Value) types.Object {
	t.Helper()
	obj, d := types.ObjectValue(attrTypes, values)
	if d.HasError() {
		t.Fatal(d)
	}
	return obj
}

func TestBuildPodTemplateTyped(t *testing.T) {
	tmpl, diags := BuildPodTemplate(context.Background(), buildModel(t, ""))
	if diags.HasError() {
		t.Fatal(diags)
	}
	spec := tmpl["spec"].(map[string]interface{})
	containers := spec["containers"].([]interface{})
	c := containers[0].(map[string]interface{})
	if c["name"] != "main" || c["image"] != "python:3.12-slim" {
		t.Errorf("container = %v", c)
	}
	env := c["env"].([]interface{})[0].(map[string]interface{})
	if env["name"] != "FOO" || env["value"] != "bar" {
		t.Errorf("env = %v", env)
	}
}

func TestBuildPodTemplateWithOverrides(t *testing.T) {
	tmpl, diags := BuildPodTemplate(context.Background(), buildModel(t, "runtimeClassName: gvisor\n"))
	if diags.HasError() {
		t.Fatal(diags)
	}
	spec := tmpl["spec"].(map[string]interface{})
	if spec["runtimeClassName"] != "gvisor" {
		t.Errorf("runtimeClassName = %v", spec["runtimeClassName"])
	}
	if len(spec["containers"].([]interface{})) != 1 {
		t.Error("typed containers lost after override merge")
	}
}

func TestFlattenRoundTrip(t *testing.T) {
	ctx := context.Background()
	m := buildModel(t, "")
	tmpl, diags := BuildPodTemplate(ctx, m)
	if diags.HasError() {
		t.Fatal(diags)
	}

	obj, diags := FlattenPodTemplate(ctx, tmpl, m)
	if diags.HasError() {
		t.Fatal(diags)
	}
	var got Model
	diags = obj.As(ctx, &got, objectAsOptions)
	if diags.HasError() {
		t.Fatal(diags)
	}

	var containers []ContainerModel
	diags = got.Containers.ElementsAs(ctx, &containers, false)
	if diags.HasError() {
		t.Fatal(diags)
	}
	if len(containers) != 1 {
		t.Fatalf("containers = %d", len(containers))
	}
	if containers[0].Name.ValueString() != "main" || containers[0].Image.ValueString() != "python:3.12-slim" {
		t.Errorf("round-trip container mismatch: %+v", containers[0])
	}
	var cmd []string
	containers[0].Command.ElementsAs(ctx, &cmd, false)
	if len(cmd) != 2 || cmd[0] != "sleep" {
		t.Errorf("round-trip command mismatch: %v", cmd)
	}
}
