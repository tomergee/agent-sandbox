package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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
	"sigs.k8s.io/agent-sandbox/terraform/internal/podtemplate"
)

const appliedPodTemplateKey = "applied_pod_template"

var objectAsOptions = basetypes.ObjectAsOptions{
	UnhandledNullAsEmpty:    true,
	UnhandledUnknownAsEmpty: true,
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

func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_sandbox"
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
		MarkdownDescription: "Manages a `Sandbox` (agents.x-k8s.io/v1beta1) — an isolated, singleton " +
			"agent runtime environment reconciled by the agent-sandbox controller.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true},
			"name":        common.NameSchema(),
			"namespace":   common.NamespaceSchema(),
			"labels":      common.LabelsSchema(),
			"annotations": common.AnnotationsSchema(),

			"pod_template":           podtemplate.Schema(),
			"volume_claim_templates": common.VolumeClaimTemplatesSchema(true),
			"service": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Whether the controller creates a headless Service for the sandbox.",
			},
			"shutdown_time": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "RFC 3339 timestamp after which the sandbox is shut down.",
			},
			"shutdown_policy": schema.StringAttribute{
				Optional: true, Computed: true,
				Default:    stringdefault.StaticString("Retain"),
				Validators: []validator.String{stringvalidator.OneOf("Delete", "Retain")},
			},
			"operating_mode": schema.StringAttribute{
				Optional: true, Computed: true,
				Default:    stringdefault.StaticString("Running"),
				Validators: []validator.String{stringvalidator.OneOf("Running", "Suspended")},
			},
			"wait_for_ready": schema.BoolAttribute{
				Optional: true, Computed: true,
				Default:             booldefault.StaticBool(true),
				MarkdownDescription: "Wait for the Ready condition before returning from create/update.",
			},

			"uid":          schema.StringAttribute{Computed: true},
			"service_fqdn": schema.StringAttribute{Computed: true},
			"service_name": schema.StringAttribute{Computed: true},
			"pod_ips":      schema.ListAttribute{Computed: true, ElementType: types.StringType},
			"node_name":    schema.StringAttribute{Computed: true},
			"selector":     schema.StringAttribute{Computed: true},
			"conditions":   common.ConditionsSchema(),

			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
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
	r.savePrivatePodTemplate(ctx, &plan, resp.Private.SetKey, &resp.Diagnostics)
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
	r.savePrivatePodTemplate(ctx, &plan, resp.Private.SetKey, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// apply is the shared SSA path for Create and Update: apply the object,
// optionally wait for Ready, then refresh computed attributes from live.
func (r *Resource) apply(ctx context.Context, plan *model, timeout time.Duration, diags *diag.Diagnostics) {
	obj, d := expand(ctx, *plan)
	diags.Append(d...)
	if diags.HasError() {
		return
	}

	if _, err := r.client.Apply(ctx, client.SandboxGVR, obj); err != nil {
		diags.AddError("Failed to apply Sandbox", err.Error())
		return
	}

	ns, name := obj.GetNamespace(), obj.GetName()
	if plan.WaitForReady.ValueBool() {
		if err := r.client.WaitForCondition(ctx, client.SandboxGVR, ns, name, "Ready", timeout); err != nil {
			diags.AddError("Sandbox did not become Ready", err.Error())
			return
		}
	}

	live, err := r.client.Get(ctx, client.SandboxGVR, ns, name)
	if err != nil {
		diags.AddError("Failed to read Sandbox after apply", err.Error())
		return
	}
	plan.ID = types.StringValue(common.MakeID(ns, name))
	diags.Append(flattenStatus(ctx, live, plan)...)
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

	live, err := r.client.Get(ctx, client.SandboxGVR, ns, name)
	if apierrors.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Sandbox", err.Error())
		return
	}

	// If the live pod template still matches what we last applied (typed
	// fields + spec_overrides merged), keep the state representation intact —
	// this avoids perpetual diffs when spec_overrides modifies typed fields.
	priorApplied, d := req.Private.GetKey(ctx, appliedPodTemplateKey)
	resp.Diagnostics.Append(d...)
	livePodTemplate, _, _ := unstructured.NestedMap(live.Object, "spec", "podTemplate")
	if priorApplied == nil || !podTemplateEqual(priorApplied, livePodTemplate) {
		resp.Diagnostics.Append(flattenSpec(ctx, live, &state)...)
	} else {
		state.ID = types.StringValue(common.MakeID(ns, name))
		state.Name = types.StringValue(name)
		state.Namespace = types.StringValue(ns)
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
	if err := r.client.Delete(ctx, client.SandboxGVR, ns, name); err != nil {
		resp.Diagnostics.AddError("Failed to delete Sandbox", err.Error())
		return
	}
	if err := r.client.WaitForDeleted(ctx, client.SandboxGVR, ns, name, timeout); err != nil {
		resp.Diagnostics.AddError("Sandbox deletion did not complete", err.Error())
	}
}

func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// savePrivatePodTemplate records the merged podTemplate we applied so Read
// can distinguish external drift from override-induced differences.
func (r *Resource) savePrivatePodTemplate(ctx context.Context, plan *model, setKey func(context.Context, string, []byte) diag.Diagnostics, diags *diag.Diagnostics) {
	var ptModel podtemplate.Model
	diags.Append(plan.PodTemplate.As(ctx, &ptModel, objectAsOptions)...)
	tmpl, d := podtemplate.BuildPodTemplate(ctx, ptModel)
	diags.Append(d...)
	if diags.HasError() {
		return
	}
	raw, err := json.Marshal(tmpl)
	if err != nil {
		diags.AddError("Failed to record applied pod template", err.Error())
		return
	}
	diags.Append(setKey(ctx, appliedPodTemplateKey, raw)...)
}

func podTemplateEqual(appliedJSON []byte, live map[string]interface{}) bool {
	var applied map[string]interface{}
	if err := json.Unmarshal(appliedJSON, &applied); err != nil {
		return false
	}
	// Round-trip live through JSON so number types compare consistently.
	liveJSON, err := json.Marshal(live)
	if err != nil {
		return false
	}
	var liveNorm map[string]interface{}
	if err := json.Unmarshal(liveJSON, &liveNorm); err != nil {
		return false
	}
	return reflect.DeepEqual(applied, liveNorm)
}
