// Package sandbox implements the agentsandbox_sandbox data source, exposing
// the live status (serviceFQDN, podIPs, conditions) of an existing Sandbox.
package sandbox

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"sigs.k8s.io/agent-sandbox/terraform/internal/client"
	"sigs.k8s.io/agent-sandbox/terraform/internal/common"
)

var (
	_ datasource.DataSource              = &DataSource{}
	_ datasource.DataSourceWithConfigure = &DataSource{}
)

type DataSource struct {
	client *client.Client
}

func New() datasource.DataSource { return &DataSource{} }

type model struct {
	ID            types.String `tfsdk:"id"`
	Name          types.String `tfsdk:"name"`
	Namespace     types.String `tfsdk:"namespace"`
	Labels        types.Map    `tfsdk:"labels"`
	Annotations   types.Map    `tfsdk:"annotations"`
	OperatingMode types.String `tfsdk:"operating_mode"`
	UID           types.String `tfsdk:"uid"`
	ServiceFQDN   types.String `tfsdk:"service_fqdn"`
	ServiceName   types.String `tfsdk:"service_name"`
	PodIPs        types.List   `tfsdk:"pod_ips"`
	NodeName      types.String `tfsdk:"node_name"`
	Selector      types.String `tfsdk:"selector"`
	Conditions    types.List   `tfsdk:"conditions"`
}

func (d *DataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_sandbox"
}

func (d *DataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *client.Client, got %T", req.ProviderData))
		return
	}
	d.client = c
}

func (d *DataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads an existing `Sandbox` and exposes its status.",
		Attributes: map[string]schema.Attribute{
			"id":        schema.StringAttribute{Computed: true},
			"name":      schema.StringAttribute{Required: true},
			"namespace": schema.StringAttribute{Optional: true, Computed: true},
			"labels": schema.MapAttribute{
				Computed: true, ElementType: types.StringType,
			},
			"annotations": schema.MapAttribute{
				Computed: true, ElementType: types.StringType,
			},
			"operating_mode": schema.StringAttribute{Computed: true},
			"uid":            schema.StringAttribute{Computed: true},
			"service_fqdn":   schema.StringAttribute{Computed: true},
			"service_name":   schema.StringAttribute{Computed: true},
			"pod_ips":        schema.ListAttribute{Computed: true, ElementType: types.StringType},
			"node_name":      schema.StringAttribute{Computed: true},
			"selector":       schema.StringAttribute{Computed: true},
			"conditions": schema.ListAttribute{
				Computed: true, ElementType: common.ConditionObjectType,
			},
		},
	}
}

func (d *DataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config model
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	ns := common.DefaultNamespace
	if !config.Namespace.IsNull() && config.Namespace.ValueString() != "" {
		ns = config.Namespace.ValueString()
	}
	name := config.Name.ValueString()

	live, err := d.client.Get(ctx, client.SandboxGVR, ns, name)
	if apierrors.IsNotFound(err) {
		resp.Diagnostics.AddError("Sandbox not found", fmt.Sprintf("sandboxes.agents.x-k8s.io %s/%s does not exist", ns, name))
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Sandbox", err.Error())
		return
	}

	config.ID = types.StringValue(common.MakeID(ns, name))
	config.Namespace = types.StringValue(ns)
	config.UID = types.StringValue(string(live.GetUID()))

	labels, dg := common.FlattenStringMap(ctx, toIfaceMap(live.GetLabels()), types.MapNull(types.StringType))
	resp.Diagnostics.Append(dg...)
	config.Labels = labels
	ann, dg := common.FlattenStringMap(ctx, toIfaceMap(live.GetAnnotations()), types.MapNull(types.StringType))
	resp.Diagnostics.Append(dg...)
	config.Annotations = ann

	om, _, _ := unstructured.NestedString(live.Object, "spec", "operatingMode")
	config.OperatingMode = optString(om)

	fqdn, _, _ := unstructured.NestedString(live.Object, "status", "serviceFQDN")
	config.ServiceFQDN = optString(fqdn)
	svc, _, _ := unstructured.NestedString(live.Object, "status", "service")
	config.ServiceName = optString(svc)
	node, _, _ := unstructured.NestedString(live.Object, "status", "nodeName")
	config.NodeName = optString(node)
	sel, _, _ := unstructured.NestedString(live.Object, "status", "selector")
	config.Selector = optString(sel)

	ips, _, _ := unstructured.NestedStringSlice(live.Object, "status", "podIPs")
	ipList, dg := types.ListValueFrom(ctx, types.StringType, ips)
	if ips == nil {
		ipList = types.ListNull(types.StringType)
	}
	resp.Diagnostics.Append(dg...)
	config.PodIPs = ipList

	conds, _, _ := unstructured.NestedSlice(live.Object, "status", "conditions")
	condList, dg := common.FlattenConditions(ctx, conds)
	resp.Diagnostics.Append(dg...)
	config.Conditions = condList

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
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
