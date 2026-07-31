// Package warmpool implements agentsandbox_sandbox_warmpool, managing
// SandboxWarmPool (extensions.agents.x-k8s.io/v1beta1) objects.
package warmpool

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"sigs.k8s.io/agent-sandbox/terraform/internal/client"
	"sigs.k8s.io/agent-sandbox/terraform/internal/common"
)

var (
	_ resource.Resource                = &Resource{}
	_ resource.ResourceWithConfigure   = &Resource{}
	_ resource.ResourceWithImportState = &Resource{}
)

type Resource struct {
	client *client.Client
}

func New() resource.Resource { return &Resource{} }

type model struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Namespace   types.String `tfsdk:"namespace"`
	Labels      types.Map    `tfsdk:"labels"`
	Annotations types.Map    `tfsdk:"annotations"`

	Replicas            types.Int64  `tfsdk:"replicas"`
	SandboxTemplateName types.String `tfsdk:"sandbox_template_name"`
	UpdateStrategy      types.String `tfsdk:"update_strategy"`
	WaitForReady        types.Bool   `tfsdk:"wait_for_ready"`

	UID            types.String `tfsdk:"uid"`
	StatusReplicas types.Int64  `tfsdk:"status_replicas"`
	ReadyReplicas  types.Int64  `tfsdk:"ready_replicas"`
	Selector       types.String `tfsdk:"selector"`

	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_sandbox_warmpool"
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
		MarkdownDescription: "Manages a `SandboxWarmPool` (extensions.agents.x-k8s.io/v1beta1) — a pool " +
			"of pre-warmed sandboxes stamped from a SandboxTemplate.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true},
			"name":        common.NameSchema(),
			"namespace":   common.NamespaceSchema(),
			"labels":      common.LabelsSchema(),
			"annotations": common.AnnotationsSchema(),

			"replicas": schema.Int64Attribute{
				Optional: true, Computed: true,
				Default:             int64default.StaticInt64(1),
				Validators:          []validator.Int64{int64validator.AtLeast(0)},
				MarkdownDescription: "Desired number of warm sandboxes. May be driven by an HPA.",
			},
			"sandbox_template_name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Name of the SandboxTemplate (same namespace) to stamp sandboxes from.",
			},
			"update_strategy": schema.StringAttribute{
				Optional: true, Computed: true,
				Default:    stringdefault.StaticString("OnReplenish"),
				Validators: []validator.String{stringvalidator.OneOf("Recreate", "OnReplenish")},
			},
			"wait_for_ready": schema.BoolAttribute{
				Optional: true, Computed: true,
				Default:             booldefault.StaticBool(true),
				MarkdownDescription: "Wait until readyReplicas equals replicas before returning.",
			},

			"uid":             schema.StringAttribute{Computed: true},
			"status_replicas": schema.Int64Attribute{Computed: true},
			"ready_replicas":  schema.Int64Attribute{Computed: true},
			"selector":        schema.StringAttribute{Computed: true},

			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *Resource) expand(ctx context.Context, m model) (*unstructured.Unstructured, diag.Diagnostics) {
	var diags diag.Diagnostics

	spec := map[string]interface{}{
		"sandboxTemplateRef": map[string]interface{}{"name": m.SandboxTemplateName.ValueString()},
	}
	if !m.Replicas.IsNull() && !m.Replicas.IsUnknown() {
		spec["replicas"] = m.Replicas.ValueInt64()
	}
	if !m.UpdateStrategy.IsNull() && !m.UpdateStrategy.IsUnknown() {
		spec["updateStrategy"] = map[string]interface{}{"type": m.UpdateStrategy.ValueString()}
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
		"kind":       "SandboxWarmPool",
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
	if _, err := r.client.Apply(ctx, client.SandboxWarmPoolGVR, obj); err != nil {
		diags.AddError("Failed to apply SandboxWarmPool", err.Error())
		return
	}

	ns, name := obj.GetNamespace(), obj.GetName()
	if plan.WaitForReady.ValueBool() {
		if err := r.client.WaitForWarmPoolReady(ctx, ns, name, plan.Replicas.ValueInt64(), timeout); err != nil {
			diags.AddError("SandboxWarmPool did not become ready", err.Error())
			return
		}
	}

	live, err := r.client.Get(ctx, client.SandboxWarmPoolGVR, ns, name)
	if err != nil {
		diags.AddError("Failed to read SandboxWarmPool after apply", err.Error())
		return
	}
	plan.ID = types.StringValue(common.MakeID(ns, name))
	flattenStatus(live, plan)
}

func flattenStatus(u *unstructured.Unstructured, m *model) {
	m.UID = types.StringValue(string(u.GetUID()))
	replicas, _, _ := unstructured.NestedInt64(u.Object, "status", "replicas")
	m.StatusReplicas = types.Int64Value(replicas)
	ready, _, _ := unstructured.NestedInt64(u.Object, "status", "readyReplicas")
	m.ReadyReplicas = types.Int64Value(ready)
	sel, _, _ := unstructured.NestedString(u.Object, "status", "selector")
	if sel == "" {
		m.Selector = types.StringNull()
	} else {
		m.Selector = types.StringValue(sel)
	}
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
	live, err := r.client.Get(ctx, client.SandboxWarmPoolGVR, ns, name)
	if apierrors.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read SandboxWarmPool", err.Error())
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

	if replicas, found, _ := unstructured.NestedInt64(live.Object, "spec", "replicas"); found {
		state.Replicas = types.Int64Value(replicas)
	}
	if ref, found, _ := unstructured.NestedString(live.Object, "spec", "sandboxTemplateRef", "name"); found {
		state.SandboxTemplateName = types.StringValue(ref)
	}
	if strat, found, _ := unstructured.NestedString(live.Object, "spec", "updateStrategy", "type"); found && strat != "" {
		state.UpdateStrategy = types.StringValue(strat)
	}

	flattenStatus(live, &state)
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
	if err := r.client.Delete(ctx, client.SandboxWarmPoolGVR, ns, name); err != nil {
		resp.Diagnostics.AddError("Failed to delete SandboxWarmPool", err.Error())
		return
	}
	if err := r.client.WaitForDeleted(ctx, client.SandboxWarmPoolGVR, ns, name, timeout); err != nil {
		resp.Diagnostics.AddError("SandboxWarmPool deletion did not complete", err.Error())
	}
}

func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
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
