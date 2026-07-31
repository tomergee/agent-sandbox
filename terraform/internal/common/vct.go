package common

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// VolumeClaimTemplateModel is one entry of volume_claim_templates.
type VolumeClaimTemplateModel struct {
	Name             types.String `tfsdk:"name"`
	AccessModes      types.List   `tfsdk:"access_modes"`
	Storage          types.String `tfsdk:"storage"`
	StorageClassName types.String `tfsdk:"storage_class_name"`
}

var VolumeClaimTemplateAttrTypes = map[string]attr.Type{
	"name":               types.StringType,
	"access_modes":       types.ListType{ElemType: types.StringType},
	"storage":            types.StringType,
	"storage_class_name": types.StringType,
}

// VolumeClaimTemplatesSchema returns the shared volume_claim_templates
// attribute. requiresReplace should be true for Sandbox, where upstream
// marks the field immutable after creation.
func VolumeClaimTemplatesSchema(requiresReplace bool) schema.ListNestedAttribute {
	a := schema.ListNestedAttribute{
		Optional: true,
		MarkdownDescription: "PersistentVolumeClaim templates created for the sandbox. " +
			"On `agentsandbox_sandbox` this list is immutable after creation.",
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"name": schema.StringAttribute{Required: true},
				"access_modes": schema.ListAttribute{
					Required:    true,
					ElementType: types.StringType,
					Validators:  []validator.List{listvalidator.SizeAtLeast(1)},
				},
				"storage": schema.StringAttribute{
					Required:            true,
					MarkdownDescription: "Requested storage quantity, e.g. `1Gi`.",
				},
				"storage_class_name": schema.StringAttribute{Optional: true},
			},
		},
	}
	if requiresReplace {
		a.PlanModifiers = []planmodifier.List{listplanmodifier.RequiresReplace()}
	}
	return a
}

// ExpandVolumeClaimTemplates converts models to the unstructured
// volumeClaimTemplates array in PersistentVolumeClaimTemplate shape.
func ExpandVolumeClaimTemplates(ctx context.Context, list types.List) ([]interface{}, diag.Diagnostics) {
	var diags diag.Diagnostics
	if list.IsNull() || list.IsUnknown() {
		return nil, diags
	}
	var models []VolumeClaimTemplateModel
	diags.Append(list.ElementsAs(ctx, &models, false)...)
	if diags.HasError() {
		return nil, diags
	}
	out := make([]interface{}, 0, len(models))
	for _, m := range models {
		var modes []string
		diags.Append(m.AccessModes.ElementsAs(ctx, &modes, false)...)
		modesAny := make([]interface{}, len(modes))
		for i, mode := range modes {
			modesAny[i] = mode
		}
		spec := map[string]interface{}{
			"accessModes": modesAny,
			"resources": map[string]interface{}{
				"requests": map[string]interface{}{"storage": m.Storage.ValueString()},
			},
		}
		if !m.StorageClassName.IsNull() && m.StorageClassName.ValueString() != "" {
			spec["storageClassName"] = m.StorageClassName.ValueString()
		}
		out = append(out, map[string]interface{}{
			"metadata": map[string]interface{}{"name": m.Name.ValueString()},
			"spec":     spec,
		})
	}
	return out, diags
}

// FlattenVolumeClaimTemplates maps the live volumeClaimTemplates array back
// to models for Read/import.
func FlattenVolumeClaimTemplates(ctx context.Context, in []interface{}) (types.List, diag.Diagnostics) {
	var diags diag.Diagnostics
	elemType := types.ObjectType{AttrTypes: VolumeClaimTemplateAttrTypes}
	if len(in) == 0 {
		return types.ListNull(elemType), diags
	}
	objs := make([]attr.Value, 0, len(in))
	for _, raw := range in {
		m, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		name := nestedString(m, "metadata", "name")
		spec, _ := m["spec"].(map[string]interface{})
		var modeVals []attr.Value
		if spec != nil {
			if modes, ok := spec["accessModes"].([]interface{}); ok {
				for _, mode := range modes {
					if s, ok := mode.(string); ok {
						modeVals = append(modeVals, types.StringValue(s))
					}
				}
			}
		}
		modesList, d := types.ListValue(types.StringType, modeVals)
		diags.Append(d...)
		storage := types.StringNull()
		if s := nestedString(spec, "resources", "requests", "storage"); s != "" {
			storage = types.StringValue(s)
		}
		scn := types.StringNull()
		if spec != nil {
			if s, ok := spec["storageClassName"].(string); ok && s != "" {
				scn = types.StringValue(s)
			}
		}
		obj, d := types.ObjectValue(VolumeClaimTemplateAttrTypes, map[string]attr.Value{
			"name":               types.StringValue(name),
			"access_modes":       modesList,
			"storage":            storage,
			"storage_class_name": scn,
		})
		diags.Append(d...)
		objs = append(objs, obj)
	}
	list, d := types.ListValue(elemType, objs)
	diags.Append(d...)
	return list, diags
}

func nestedString(m map[string]interface{}, path ...string) string {
	cur := m
	for i, key := range path {
		if cur == nil {
			return ""
		}
		if i == len(path)-1 {
			s, _ := cur[key].(string)
			return s
		}
		next, ok := cur[key].(map[string]interface{})
		if !ok {
			return ""
		}
		cur = next
	}
	return ""
}
