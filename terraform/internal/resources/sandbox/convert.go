package sandbox

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"sigs.k8s.io/agent-sandbox/terraform/internal/client"
	"sigs.k8s.io/agent-sandbox/terraform/internal/common"
	"sigs.k8s.io/agent-sandbox/terraform/internal/podtemplate"
)

// expand builds the Sandbox unstructured object from the plan.
func expand(ctx context.Context, m model) (*unstructured.Unstructured, diag.Diagnostics) {
	var diags diag.Diagnostics

	var ptModel podtemplate.Model
	diags.Append(m.PodTemplate.As(ctx, &ptModel, objectAsOptions)...)
	if diags.HasError() {
		return nil, diags
	}
	tmpl, d := podtemplate.BuildPodTemplate(ctx, ptModel)
	diags.Append(d...)
	if diags.HasError() {
		return nil, diags
	}

	spec := map[string]interface{}{"podTemplate": tmpl}

	vcts, d := common.ExpandVolumeClaimTemplates(ctx, m.VolumeClaimTemplates)
	diags.Append(d...)
	if len(vcts) > 0 {
		spec["volumeClaimTemplates"] = vcts
	}
	if !m.Service.IsNull() && !m.Service.IsUnknown() {
		spec["service"] = m.Service.ValueBool()
	}
	if !m.ShutdownTime.IsNull() && !m.ShutdownTime.IsUnknown() && m.ShutdownTime.ValueString() != "" {
		spec["shutdownTime"] = m.ShutdownTime.ValueString()
	}
	if !m.ShutdownPolicy.IsNull() && !m.ShutdownPolicy.IsUnknown() {
		spec["shutdownPolicy"] = m.ShutdownPolicy.ValueString()
	}
	if !m.OperatingMode.IsNull() && !m.OperatingMode.IsUnknown() {
		spec["operatingMode"] = m.OperatingMode.ValueString()
	}

	metadata := map[string]interface{}{
		"name":      m.Name.ValueString(),
		"namespace": m.Namespace.ValueString(),
	}
	if labels, d := common.ExpandStringMap(ctx, m.Labels); len(labels) > 0 {
		metadata["labels"] = labels
		diags.Append(d...)
	}
	if ann, d := common.ExpandStringMap(ctx, m.Annotations); len(ann) > 0 {
		metadata["annotations"] = ann
		diags.Append(d...)
	}

	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": client.SandboxAPIVersion,
		"kind":       "Sandbox",
		"metadata":   metadata,
		"spec":       spec,
	}}, diags
}

// flattenSpec maps the live Sandbox spec back onto the model (used by Read
// and import). prior supplies values that cannot be recovered from the live
// object (spec_overrides).
func flattenSpec(ctx context.Context, u *unstructured.Unstructured, m *model) diag.Diagnostics {
	var diags diag.Diagnostics

	m.Name = types.StringValue(u.GetName())
	m.Namespace = types.StringValue(u.GetNamespace())
	m.ID = types.StringValue(common.MakeID(u.GetNamespace(), u.GetName()))

	labels, d := common.FlattenStringMap(ctx, toIfaceMap(u.GetLabels()), m.Labels)
	diags.Append(d...)
	m.Labels = labels
	ann, d := common.FlattenStringMap(ctx, toIfaceMap(u.GetAnnotations()), m.Annotations)
	diags.Append(d...)
	m.Annotations = ann

	var prior podtemplate.Model
	if !m.PodTemplate.IsNull() && !m.PodTemplate.IsUnknown() {
		diags.Append(m.PodTemplate.As(ctx, &prior, objectAsOptions)...)
	}
	if tmpl, found, _ := unstructured.NestedMap(u.Object, "spec", "podTemplate"); found {
		obj, d := podtemplate.FlattenPodTemplate(ctx, tmpl, prior)
		diags.Append(d...)
		m.PodTemplate = obj
	}

	if vcts, found, _ := unstructured.NestedSlice(u.Object, "spec", "volumeClaimTemplates"); found {
		list, d := common.FlattenVolumeClaimTemplates(ctx, vcts)
		diags.Append(d...)
		m.VolumeClaimTemplates = list
	} else {
		m.VolumeClaimTemplates = types.ListNull(types.ObjectType{AttrTypes: common.VolumeClaimTemplateAttrTypes})
	}

	if svc, found, _ := unstructured.NestedBool(u.Object, "spec", "service"); found {
		m.Service = types.BoolValue(svc)
	} else {
		m.Service = types.BoolNull()
	}
	if st, found, _ := unstructured.NestedString(u.Object, "spec", "shutdownTime"); found && st != "" {
		m.ShutdownTime = types.StringValue(st)
	} else {
		m.ShutdownTime = types.StringNull()
	}
	if sp, found, _ := unstructured.NestedString(u.Object, "spec", "shutdownPolicy"); found && sp != "" {
		m.ShutdownPolicy = types.StringValue(sp)
	}
	if om, found, _ := unstructured.NestedString(u.Object, "spec", "operatingMode"); found && om != "" {
		m.OperatingMode = types.StringValue(om)
	}

	return diags
}

// flattenStatus fills the computed status attributes.
func flattenStatus(ctx context.Context, u *unstructured.Unstructured, m *model) diag.Diagnostics {
	var diags diag.Diagnostics

	m.UID = types.StringValue(string(u.GetUID()))

	fqdn, _, _ := unstructured.NestedString(u.Object, "status", "serviceFQDN")
	m.ServiceFQDN = optString(fqdn)
	svc, _, _ := unstructured.NestedString(u.Object, "status", "service")
	m.ServiceName = optString(svc)
	node, _, _ := unstructured.NestedString(u.Object, "status", "nodeName")
	m.NodeName = optString(node)
	sel, _, _ := unstructured.NestedString(u.Object, "status", "selector")
	m.Selector = optString(sel)

	ips, _, _ := unstructured.NestedStringSlice(u.Object, "status", "podIPs")
	ipList, d := types.ListValueFrom(ctx, types.StringType, ips)
	if ips == nil {
		ipList = types.ListNull(types.StringType)
	}
	diags.Append(d...)
	m.PodIPs = ipList

	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	condList, d := common.FlattenConditions(ctx, conds)
	diags.Append(d...)
	m.Conditions = condList

	return diags
}

func optString(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

func toIfaceMap(in map[string]string) map[string]interface{} {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
