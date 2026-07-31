package common

import (
	"context"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var dns1123 = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// NameSchema returns the required, replace-on-change `name` attribute.
func NameSchema() schema.StringAttribute {
	return schema.StringAttribute{
		Required:            true,
		MarkdownDescription: "Object name. Changing it forces replacement.",
		PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
		Validators: []validator.String{
			stringvalidator.RegexMatches(dns1123, "must be a lowercase DNS-1123 label"),
			stringvalidator.LengthAtMost(253),
		},
	}
}

// NamespaceSchema returns the optional `namespace` attribute defaulting to
// "default"; changing it forces replacement.
func NamespaceSchema() schema.StringAttribute {
	return schema.StringAttribute{
		Optional:            true,
		Computed:            true,
		Default:             stringdefault.StaticString(DefaultNamespace),
		MarkdownDescription: "Object namespace. Defaults to `default`. Changing it forces replacement.",
		PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
	}
}

// LabelsSchema / AnnotationsSchema are optional string maps.
func LabelsSchema() schema.MapAttribute {
	return schema.MapAttribute{
		Optional:            true,
		ElementType:         types.StringType,
		MarkdownDescription: "Labels to set on the object metadata.",
	}
}

func AnnotationsSchema() schema.MapAttribute {
	return schema.MapAttribute{
		Optional:            true,
		ElementType:         types.StringType,
		MarkdownDescription: "Annotations to set on the object metadata.",
	}
}

// ExpandStringMap converts a types.Map into map[string]interface{} for
// unstructured content; returns nil for null/unknown maps.
func ExpandStringMap(ctx context.Context, m types.Map) (map[string]interface{}, diag.Diagnostics) {
	var diags diag.Diagnostics
	if m.IsNull() || m.IsUnknown() {
		return nil, diags
	}
	elems := map[string]string{}
	diags.Append(m.ElementsAs(ctx, &elems, false)...)
	if diags.HasError() {
		return nil, diags
	}
	out := make(map[string]interface{}, len(elems))
	for k, v := range elems {
		out[k] = v
	}
	return out, diags
}

// FlattenStringMap converts a map from an unstructured object back to a
// types.Map, preserving null when empty and the prior state was null.
func FlattenStringMap(ctx context.Context, in map[string]interface{}, prior types.Map) (types.Map, diag.Diagnostics) {
	var diags diag.Diagnostics
	if len(in) == 0 {
		if prior.IsNull() {
			return types.MapNull(types.StringType), diags
		}
		m, d := types.MapValue(types.StringType, nil)
		diags.Append(d...)
		return m, diags
	}
	elems := make(map[string]string, len(in))
	for k, v := range in {
		if s, ok := v.(string); ok {
			elems[k] = s
		}
	}
	m, d := types.MapValueFrom(ctx, types.StringType, elems)
	diags.Append(d...)
	return m, diags
}
