// Package claim implements agentsandbox_sandbox_claim, managing SandboxClaim
// (extensions.agents.x-k8s.io/v1beta1) objects — a claim binds a warm sandbox
// from a SandboxWarmPool to one session.
package claim

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"sigs.k8s.io/agent-sandbox/terraform/internal/client"
	"sigs.k8s.io/agent-sandbox/terraform/internal/common"
)

var objectAsOptions = basetypes.ObjectAsOptions{
	UnhandledNullAsEmpty:    true,
	UnhandledUnknownAsEmpty: true,
}

var envAttrTypes = map[string]attr.Type{
	"name":           types.StringType,
	"value":          types.StringType,
	"container_name": types.StringType,
}

var (
	_ resource.Resource                = &Resource{}
	_ resource.ResourceWithConfigure   = &Resource{}
	_ resource.ResourceWithImportState = &Resource{}
)

type Resource struct {
	client *client.Client
}

func New() resource.Resource { return &Resource{} }

type envModel struct {
	Name          types.String `tfsdk:"name"`
	Value         types.String `tfsdk:"value"`
	ContainerName types.String `tfsdk:"container_name"`
}

type model struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Namespace   types.String `tfsdk:"namespace"`
	Labels      types.Map    `tfsdk:"labels"`
	Annotations types.Map    `tfsdk:"annotations"`

	WarmPoolName          types.String `tfsdk:"warm_pool_name"`
	ShutdownTime          types.String `tfsdk:"shutdown_time"`
	ShutdownPolicy        types.String `tfsdk:"shutdown_policy"`
	AdditionalPodMetadata types.Object `tfsdk:"additional_pod_metadata"`
	Env                   types.List   `tfsdk:"env"`
	VolumeClaimTemplates  types.List   `tfsdk:"volume_claim_templates"`
	WaitForReady          types.Bool   `tfsdk:"wait_for_ready"`

	UID         types.String `tfsdk:"uid"`
	ServiceFQDN types.String `tfsdk:"service_fqdn"`
	ServiceName types.String `tfsdk:"service_name"`
	PodIPs      types.List   `tfsdk:"pod_ips"`
	NodeName    types.String `tfsdk:"node_name"`
	Conditions  types.List   `tfsdk:"conditions"`

	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

var podMetadataAttrTypes = map[string]attr.Type{
	"labels":      types.MapType{ElemType: types.StringType},
	"annotations": types.MapType{ElemType: types.StringType},
}

func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_sandbox_claim"
}

func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *client.Client, got %T", req.ProviderData))
		return
	}
	r.client = c
}

func (r *Resource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a `SandboxClaim` (extensions.agents.x-k8s.io/v1beta1) — binds a " +
			"pre-warmed sandbox from a SandboxWarmPool to a single session.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true},
			"name":        common.NameSchema(),
			"namespace":   common.NamespaceSchema(),
			"labels":      common.LabelsSchema(),
			"annotations": common.AnnotationsSchema(),

			"warm_pool_name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Name of the SandboxWarmPool (same namespace) to claim from.",
			},
			"shutdown_time": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "RFC 3339 timestamp after which the claimed sandbox is shut down.",
			},
			"shutdown_policy": schema.StringAttribute{
				Optional: true, Computed: true,
				Default:    stringdefault.StaticString("Retain"),
				Validators: []validator.String{stringvalidator.OneOf("Delete", "Retain")},
			},
			"additional_pod_metadata": schema.SingleNestedAttribute{
				Optional: true,
				Attributes: map[string]schema.Attribute{
					"labels":      schema.MapAttribute{Optional: true, ElementType: types.StringType},
					"annotations": schema.MapAttribute{Optional: true, ElementType: types.StringType},
				},
			},
			"env": schema.ListNestedAttribute{
				Optional: true,
				MarkdownDescription: "Environment variables injected into the claimed sandbox. Subject " +
					"to the template's env_vars_injection_policy.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name":           schema.StringAttribute{Required: true},
						"value":          schema.StringAttribute{Required: true},
						"container_name": schema.StringAttribute{Optional: true},
					},
				},
			},
			"volume_claim_templates": common.VolumeClaimTemplatesSchema(false),
			"wait_for_ready": schema.BoolAttribute{
				Optional: true, Computed: true,
				Default:             booldefault.StaticBool(true),
				MarkdownDescription: "Wait for the claim's Ready condition before returning.",
			},

			"uid":          schema.StringAttribute{Computed: true},
			"service_fqdn": schema.StringAttribute{Computed: true},
			"service_name": schema.StringAttribute{Computed: true},
			"pod_ips":      schema.ListAttribute{Computed: true, ElementType: types.StringType},
			"node_name":    schema.StringAttribute{Computed: true},
			"conditions":   common.ConditionsSchema(),

			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *Resource) expand(ctx context.Context, m model) (*unstructured.Unstructured, diag.Diagnostics) {
	var diags diag.Diagnostics

	spec := map[string]interface{}{
		"warmPoolRef": map[string]interface{}{"name": m.WarmPoolName.ValueString()},
	}

	lifecycle := map[string]interface{}{}
	if !m.ShutdownTime.IsNull() && !m.ShutdownTime.IsUnknown() && m.ShutdownTime.ValueString() != "" {
		lifecycle["shutdownTime"] = m.ShutdownTime.ValueString()
	}
	if !m.ShutdownPolicy.IsNull() && !m.ShutdownPolicy.IsUnknown() {
		lifecycle["shutdownPolicy"] = m.ShutdownPolicy.ValueString()
	}
	if len(lifecycle) > 0 {
		spec["lifecycle"] = lifecycle
	}

	if !m.AdditionalPodMetadata.IsNull() && !m.AdditionalPodMetadata.IsUnknown() {
		var pm struct {
			Labels      types.Map `tfsdk:"labels"`
			Annotations types.Map `tfsdk:"annotations"`
		}
		diags.Append(m.AdditionalPodMetadata.As(ctx, &pm, objectAsOptions)...)
		meta := map[string]interface{}{}
		if labels, d := common.ExpandStringMap(ctx, pm.Labels); len(labels) > 0 {
			meta["labels"] = labels
			diags.Append(d...)
		}
		if ann, d := common.ExpandStringMap(ctx, pm.Annotations); len(ann) > 0 {
			meta["annotations"] = ann
			diags.Append(d...)
		}
		if len(meta) > 0 {
			spec["additionalPodMetadata"] = meta
		}
	}

	if !m.Env.IsNull() && !m.Env.IsUnknown() {
		var envs []envModel
		diags.Append(m.Env.ElementsAs(ctx, &envs, false)...)
		items := make([]interface{}, 0, len(envs))
		for _, e := range envs {
			item := map[string]interface{}{
				"name":  e.Name.ValueString(),
				"value": e.Value.ValueString(),
			}
			if !e.ContainerName.IsNull() && e.ContainerName.ValueString() != "" {
				item["containerName"] = e.ContainerName.ValueString()
			}
			items = append(items, item)
		}
		if len(items) > 0 {
			spec["env"] = items
		}
	}

	vcts, d := common.ExpandVolumeClaimTemplates(ctx, m.VolumeClaimTemplates)
	diags.Append(d...)
	if len(vcts) > 0 {
		spec["volumeClaimTemplates"] = vcts
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
		"apiVersion": client.ExtensionsAPIVersion,
		"kind":       "SandboxClaim",
		"metadata":   metadata,
		"spec":       spec,
	}}, diags
}

func (r *Resource) apply(ctx context.Context, plan *model, timeout time.Duration, diags *diag.Diagnostics) {
	obj, d := r.expand(ctx, *plan)
	diags.Append(d...)
	if diags.HasError() {
		return
	}
	if _, err := r.client.Apply(ctx, client.SandboxClaimGVR, obj); err != nil {
		diags.AddError("Failed to apply SandboxClaim", err.Error())
		return
	}

	ns, name := obj.GetNamespace(), obj.GetName()
	if plan.WaitForReady.ValueBool() {
		if err := r.client.WaitForCondition(ctx, client.SandboxClaimGVR, ns, name, "Ready", timeout); err != nil {
			diags.AddError("SandboxClaim did not become Ready", err.Error())
			return
		}
	}

	live, err := r.client.Get(ctx, client.SandboxClaimGVR, ns, name)
	if err != nil {
		diags.AddError("Failed to read SandboxClaim after apply", err.Error())
		return
	}
	plan.ID = types.StringValue(common.MakeID(ns, name))
	diags.Append(flattenStatus(ctx, live, plan)...)
}

func flattenStatus(ctx context.Context, u *unstructured.Unstructured, m *model) diag.Diagnostics {
	var diags diag.Diagnostics

	m.UID = types.StringValue(string(u.GetUID()))

	fqdn, _, _ := unstructured.NestedString(u.Object, "status", "sandbox", "serviceFQDN")
	m.ServiceFQDN = optString(fqdn)
	svc, _, _ := unstructured.NestedString(u.Object, "status", "sandbox", "service")
	m.ServiceName = optString(svc)
	node, _, _ := unstructured.NestedString(u.Object, "status", "sandbox", "nodeName")
	m.NodeName = optString(node)

	ips, _, _ := unstructured.NestedStringSlice(u.Object, "status", "sandbox", "podIPs")
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

func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Create(ctx, 10*time.Minute)
	resp.Diagnostics.Append(d...)
	r.apply(ctx, &plan, timeout, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Update(ctx, 10*time.Minute)
	resp.Diagnostics.Append(d...)
	r.apply(ctx, &plan, timeout, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ns, name, err := common.ParseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid resource ID", err.Error())
		return
	}
	live, err := r.client.Get(ctx, client.SandboxClaimGVR, ns, name)
	if apierrors.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read SandboxClaim", err.Error())
		return
	}

	state.Name = types.StringValue(name)
	state.Namespace = types.StringValue(ns)

	labels, d := common.FlattenStringMap(ctx, toIfaceMap(live.GetLabels()), state.Labels)
	resp.Diagnostics.Append(d...)
	state.Labels = labels
	ann, d := common.FlattenStringMap(ctx, toIfaceMap(live.GetAnnotations()), state.Annotations)
	resp.Diagnostics.Append(d...)
	state.Annotations = ann

	if ref, found, _ := unstructured.NestedString(live.Object, "spec", "warmPoolRef", "name"); found {
		state.WarmPoolName = types.StringValue(ref)
	}
	if st, found, _ := unstructured.NestedString(live.Object, "spec", "lifecycle", "shutdownTime"); found && st != "" {
		state.ShutdownTime = types.StringValue(st)
	} else {
		state.ShutdownTime = types.StringNull()
	}
	if sp, found, _ := unstructured.NestedString(live.Object, "spec", "lifecycle", "shutdownPolicy"); found && sp != "" {
		state.ShutdownPolicy = types.StringValue(sp)
	}

	if meta, found, _ := unstructured.NestedMap(live.Object, "spec", "additionalPodMetadata"); found && len(meta) > 0 {
		labels := types.MapNull(types.StringType)
		if l, ok := meta["labels"].(map[string]interface{}); ok {
			labels, d = common.FlattenStringMap(ctx, l, types.MapNull(types.StringType))
			resp.Diagnostics.Append(d...)
		}
		annotations := types.MapNull(types.StringType)
		if a, ok := meta["annotations"].(map[string]interface{}); ok {
			annotations, d = common.FlattenStringMap(ctx, a, types.MapNull(types.StringType))
			resp.Diagnostics.Append(d...)
		}
		obj, d := types.ObjectValue(podMetadataAttrTypes, map[string]attr.Value{
			"labels": labels, "annotations": annotations,
		})
		resp.Diagnostics.Append(d...)
		state.AdditionalPodMetadata = obj
	} else {
		state.AdditionalPodMetadata = types.ObjectNull(podMetadataAttrTypes)
	}

	if envs, found, _ := unstructured.NestedSlice(live.Object, "spec", "env"); found && len(envs) > 0 {
		vals := make([]attr.Value, 0, len(envs))
		for _, raw := range envs {
			e, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			obj, d := types.ObjectValue(envAttrTypes, map[string]attr.Value{
				"name":           optString(str(e["name"])),
				"value":          optString(str(e["value"])),
				"container_name": optString(str(e["containerName"])),
			})
			resp.Diagnostics.Append(d...)
			vals = append(vals, obj)
		}
		list, d := types.ListValue(types.ObjectType{AttrTypes: envAttrTypes}, vals)
		resp.Diagnostics.Append(d...)
		state.Env = list
	} else {
		state.Env = types.ListNull(types.ObjectType{AttrTypes: envAttrTypes})
	}

	if vcts, found, _ := unstructured.NestedSlice(live.Object, "spec", "volumeClaimTemplates"); found {
		list, d := common.FlattenVolumeClaimTemplates(ctx, vcts)
		resp.Diagnostics.Append(d...)
		state.VolumeClaimTemplates = list
	} else {
		state.VolumeClaimTemplates = types.ListNull(types.ObjectType{AttrTypes: common.VolumeClaimTemplateAttrTypes})
	}

	resp.Diagnostics.Append(flattenStatus(ctx, live, &state)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := state.Timeouts.Delete(ctx, 10*time.Minute)
	resp.Diagnostics.Append(d...)
	ns, name, err := common.ParseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid resource ID", err.Error())
		return
	}
	if err := r.client.Delete(ctx, client.SandboxClaimGVR, ns, name); err != nil {
		resp.Diagnostics.AddError("Failed to delete SandboxClaim", err.Error())
		return
	}
	if err := r.client.WaitForDeleted(ctx, client.SandboxClaimGVR, ns, name, timeout); err != nil {
		resp.Diagnostics.AddError("SandboxClaim deletion did not complete", err.Error())
	}
}

func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func optString(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

func str(v interface{}) string {
	s, _ := v.(string)
	return s
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
