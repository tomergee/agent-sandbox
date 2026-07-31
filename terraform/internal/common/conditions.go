package common

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// ConditionAttrTypes describes one entry of the computed `conditions` list,
// mirroring metav1.Condition.
var ConditionAttrTypes = map[string]attr.Type{
	"type":                 types.StringType,
	"status":               types.StringType,
	"reason":               types.StringType,
	"message":              types.StringType,
	"last_transition_time": types.StringType,
	"observed_generation":  types.Int64Type,
}

var ConditionObjectType = types.ObjectType{AttrTypes: ConditionAttrTypes}

// ConditionsSchema is the shared computed attribute exposing status.conditions.
func ConditionsSchema() schema.ListAttribute {
	return schema.ListAttribute{
		Computed:            true,
		ElementType:         ConditionObjectType,
		MarkdownDescription: "Latest observed conditions of the object.",
	}
}

// FlattenConditions converts the unstructured status.conditions slice into a
// types.List of condition objects.
func FlattenConditions(ctx context.Context, conds []interface{}) (types.List, diag.Diagnostics) {
	objs := make([]attr.Value, 0, len(conds))
	var diags diag.Diagnostics
	for _, raw := range conds {
		m, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		var observedGen types.Int64
		if og, ok := m["observedGeneration"].(int64); ok {
			observedGen = types.Int64Value(og)
		} else {
			observedGen = types.Int64Null()
		}
		obj, d := types.ObjectValue(ConditionAttrTypes, map[string]attr.Value{
			"type":                 stringOrNull(m["type"]),
			"status":               stringOrNull(m["status"]),
			"reason":               stringOrNull(m["reason"]),
			"message":              stringOrNull(m["message"]),
			"last_transition_time": stringOrNull(m["lastTransitionTime"]),
			"observed_generation":  observedGen,
		})
		diags.Append(d...)
		objs = append(objs, obj)
	}
	list, d := types.ListValue(ConditionObjectType, objs)
	diags.Append(d...)
	return list, diags
}

func stringOrNull(v interface{}) types.String {
	if s, ok := v.(string); ok {
		return types.StringValue(s)
	}
	return types.StringNull()
}
