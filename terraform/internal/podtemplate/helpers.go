package podtemplate

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

var objectAsOptions = basetypes.ObjectAsOptions{
	UnhandledNullAsEmpty:    true,
	UnhandledUnknownAsEmpty: true,
}

func setString(m map[string]interface{}, key string, v types.String) {
	if !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" {
		m[key] = v.ValueString()
	}
}

func expandStringList(ctx context.Context, list types.List, diags *diag.Diagnostics) []interface{} {
	if list.IsNull() || list.IsUnknown() {
		return nil
	}
	var items []string
	diags.Append(list.ElementsAs(ctx, &items, false)...)
	out := make([]interface{}, len(items))
	for i, s := range items {
		out[i] = s
	}
	return out
}

func expandStringMap(ctx context.Context, m types.Map, diags *diag.Diagnostics) map[string]interface{} {
	if m.IsNull() || m.IsUnknown() {
		return nil
	}
	elems := map[string]string{}
	diags.Append(m.ElementsAs(ctx, &elems, false)...)
	out := make(map[string]interface{}, len(elems))
	for k, v := range elems {
		out[k] = v
	}
	return out
}

func flattenStringList(in interface{}) types.List {
	items, ok := in.([]interface{})
	if !ok || len(items) == 0 {
		return types.ListNull(types.StringType)
	}
	vals := make([]attr.Value, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			vals = append(vals, types.StringValue(s))
		}
	}
	list, _ := types.ListValue(types.StringType, vals)
	return list
}

func flattenStringMap(in interface{}) types.Map {
	m, ok := in.(map[string]interface{})
	if !ok || len(m) == 0 {
		return types.MapNull(types.StringType)
	}
	elems := make(map[string]attr.Value, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			elems[k] = types.StringValue(s)
		}
	}
	out, _ := types.MapValue(types.StringType, elems)
	return out
}

func strValue(in interface{}) types.String {
	if s, ok := in.(string); ok && s != "" {
		return types.StringValue(s)
	}
	return types.StringNull()
}
