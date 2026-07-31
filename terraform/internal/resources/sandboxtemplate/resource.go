// Package sandboxtemplate implements agentsandbox_sandbox_template, managing
// SandboxTemplate (extensions.agents.x-k8s.io/v1beta1) objects.
package sandboxtemplate

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigsyaml "sigs.k8s.io/yaml"

	"sigs.k8s.io/agent-sandbox/terraform/internal/client"
	"sigs.k8s.io/agent-sandbox/terraform/internal/common"
	"sigs.k8s.io/agent-sandbox/terraform/internal/podtemplate"
)

var objectAsOptions = basetypes.ObjectAsOptions{
	UnhandledNullAsEmpty:    true,
	UnhandledUnknownAsEmpty: true,
}

var networkPolicyAttrTypes = map[string]attr.Type{
	"ingress": types.StringType,
	"egress":  types.StringType,
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

type model struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Namespace   types.String `tfsdk:"namespace"`
	Labels      types.Map    `tfsdk:"labels"`
	Annotations types.Map    `tfsdk:"annotations"`

	PodTemplate          types.Object `tfsdk:"pod_template"`
	VolumeClaimTemplates types.List   `tfsdk:"volume_claim_templates"`
	Service              types.Bool   `tfsdk:"service"`

	NetworkPolicy              types.Object `tfsdk:"network_policy"`
	NetworkPolicyManagement    types.String `tfsdk:"network_policy_management"`
	EnvVarsInjectionPolicy     types.String `tfsdk:"env_vars_injection_policy"`
	VolumeClaimTemplatesPolicy types.String `tfsdk:"volume_claim_templates_policy"`

	UID types.String `tfsdk:"uid"`
}

func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_sandbox_template"
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

func (r *Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a `SandboxTemplate` (extensions.agents.x-k8s.io/v1beta1) — a " +
			"blueprint that SandboxWarmPools stamp sandboxes from. Requires the agent-sandbox " +
			"extensions to be installed.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true},
			"name":        common.NameSchema(),
			"namespace":   common.NamespaceSchema(),
			"labels":      common.LabelsSchema(),
			"annotations": common.AnnotationsSchema(),

			"pod_template":           podtemplate.Schema(),
			"volume_claim_templates": common.VolumeClaimTemplatesSchema(false),
			"service":                schema.BoolAttribute{Optional: true},

			"network_policy": schema.SingleNestedAttribute{
				Optional: true,
				MarkdownDescription: "Network policy applied to sandboxes from this template. " +
					"`ingress`/`egress` are YAML or JSON lists of networking.k8s.io/v1 " +
					"NetworkPolicyIngressRule/NetworkPolicyEgressRule objects.",
				Attributes: map[string]schema.Attribute{
					"ingress": schema.StringAttribute{Optional: true},
					"egress":  schema.StringAttribute{Optional: true},
				},
			},
			"network_policy_management": schema.StringAttribute{
				Optional: true, Computed: true,
				Default:    stringdefault.StaticString("Managed"),
				Validators: []validator.String{stringvalidator.OneOf("Managed", "Unmanaged")},
			},
			"env_vars_injection_policy": schema.StringAttribute{
				Optional: true, Computed: true,
				Default:    stringdefault.StaticString("Disallowed"),
				Validators: []validator.String{stringvalidator.OneOf("Allowed", "Overrides", "Disallowed")},
			},
			"volume_claim_templates_policy": schema.StringAttribute{
				Optional: true, Computed: true,
				Default:    stringdefault.StaticString("Disallowed"),
				Validators: []validator.String{stringvalidator.OneOf("Allowed", "Overrides", "Disallowed")},
			},

			"uid": schema.StringAttribute{Computed: true},
		},
	}
}

func (r *Resource) expand(ctx context.Context, m model) (*unstructured.Unstructured, diag.Diagnostics) {
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

	if !m.NetworkPolicy.IsNull() && !m.NetworkPolicy.IsUnknown() {
		var np struct {
			Ingress types.String `tfsdk:"ingress"`
			Egress  types.String `tfsdk:"egress"`
		}
		diags.Append(m.NetworkPolicy.As(ctx, &np, objectAsOptions)...)
		npSpec := map[string]interface{}{}
		if rules, err := parseRuleList(np.Ingress); err != nil {
			diags.AddError("Invalid network_policy.ingress", err.Error())
		} else if rules != nil {
			npSpec["ingress"] = rules
		}
		if rules, err := parseRuleList(np.Egress); err != nil {
			diags.AddError("Invalid network_policy.egress", err.Error())
		} else if rules != nil {
			npSpec["egress"] = rules
		}
		if len(npSpec) > 0 {
			spec["networkPolicy"] = npSpec
		}
	}

	setIfSet := func(key string, v types.String) {
		if !v.IsNull() && !v.IsUnknown() {
			spec[key] = v.ValueString()
		}
	}
	setIfSet("networkPolicyManagement", m.NetworkPolicyManagement)
	setIfSet("envVarsInjectionPolicy", m.EnvVarsInjectionPolicy)
	setIfSet("volumeClaimTemplatesPolicy", m.VolumeClaimTemplatesPolicy)

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
		"kind":       "SandboxTemplate",
		"metadata":   metadata,
		"spec":       spec,
	}}, diags
}

// parseRuleList converts a YAML/JSON string into a []interface{} of network
// policy rules; empty input yields nil.
func parseRuleList(v types.String) ([]interface{}, error) {
	if v.IsNull() || v.IsUnknown() || v.ValueString() == "" {
		return nil, nil
	}
	jsonBytes, err := sigsyaml.YAMLToJSON([]byte(v.ValueString()))
	if err != nil {
		return nil, fmt.Errorf("parsing as YAML/JSON: %w", err)
	}
	var rules []interface{}
	if err := json.Unmarshal(jsonBytes, &rules); err != nil {
		return nil, fmt.Errorf("expected a list of rules: %w", err)
	}
	return rules, nil
}

func (r *Resource) applyAndSet(ctx context.Context, plan *model, diags *diag.Diagnostics) {
	obj, d := r.expand(ctx, *plan)
	diags.Append(d...)
	if diags.HasError() {
		return
	}
	live, err := r.client.Apply(ctx, client.SandboxTemplateGVR, obj)
	if err != nil {
		diags.AddError("Failed to apply SandboxTemplate", err.Error())
		return
	}
	plan.ID = types.StringValue(common.MakeID(obj.GetNamespace(), obj.GetName()))
	plan.UID = types.StringValue(string(live.GetUID()))
}

func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.applyAndSet(ctx, &plan, &resp.Diagnostics)
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
	r.applyAndSet(ctx, &plan, &resp.Diagnostics)
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
	live, err := r.client.Get(ctx, client.SandboxTemplateGVR, ns, name)
	if apierrors.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read SandboxTemplate", err.Error())
		return
	}

	state.Name = types.StringValue(name)
	state.Namespace = types.StringValue(ns)
	state.UID = types.StringValue(string(live.GetUID()))

	labels, d := common.FlattenStringMap(ctx, toIfaceMap(live.GetLabels()), state.Labels)
	resp.Diagnostics.Append(d...)
	state.Labels = labels
	ann, d := common.FlattenStringMap(ctx, toIfaceMap(live.GetAnnotations()), state.Annotations)
	resp.Diagnostics.Append(d...)
	state.Annotations = ann

	var prior podtemplate.Model
	if !state.PodTemplate.IsNull() && !state.PodTemplate.IsUnknown() {
		resp.Diagnostics.Append(state.PodTemplate.As(ctx, &prior, objectAsOptions)...)
	}
	if tmpl, found, _ := unstructured.NestedMap(live.Object, "spec", "podTemplate"); found {
		obj, d := podtemplate.FlattenPodTemplate(ctx, tmpl, prior)
		resp.Diagnostics.Append(d...)
		state.PodTemplate = obj
	}

	if vcts, found, _ := unstructured.NestedSlice(live.Object, "spec", "volumeClaimTemplates"); found {
		list, d := common.FlattenVolumeClaimTemplates(ctx, vcts)
		resp.Diagnostics.Append(d...)
		state.VolumeClaimTemplates = list
	} else {
		state.VolumeClaimTemplates = types.ListNull(types.ObjectType{AttrTypes: common.VolumeClaimTemplateAttrTypes})
	}

	if svc, found, _ := unstructured.NestedBool(live.Object, "spec", "service"); found {
		state.Service = types.BoolValue(svc)
	} else {
		state.Service = types.BoolNull()
	}

	// network_policy fragments cannot be canonically recovered as the exact
	// user-supplied strings; keep prior state values (documented limitation).

	if v, found, _ := unstructured.NestedString(live.Object, "spec", "networkPolicyManagement"); found && v != "" {
		state.NetworkPolicyManagement = types.StringValue(v)
	}
	if v, found, _ := unstructured.NestedString(live.Object, "spec", "envVarsInjectionPolicy"); found && v != "" {
		state.EnvVarsInjectionPolicy = types.StringValue(v)
	}
	if v, found, _ := unstructured.NestedString(live.Object, "spec", "volumeClaimTemplatesPolicy"); found && v != "" {
		state.VolumeClaimTemplatesPolicy = types.StringValue(v)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
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
	if err := r.client.Delete(ctx, client.SandboxTemplateGVR, ns, name); err != nil {
		resp.Diagnostics.AddError("Failed to delete SandboxTemplate", err.Error())
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
