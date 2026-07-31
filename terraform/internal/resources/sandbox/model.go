package sandbox

import (
	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type model struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Namespace   types.String `tfsdk:"namespace"`
	Labels      types.Map    `tfsdk:"labels"`
	Annotations types.Map    `tfsdk:"annotations"`

	PodTemplate          types.Object `tfsdk:"pod_template"`
	VolumeClaimTemplates types.List   `tfsdk:"volume_claim_templates"`
	Service              types.Bool   `tfsdk:"service"`
	ShutdownTime         types.String `tfsdk:"shutdown_time"`
	ShutdownPolicy       types.String `tfsdk:"shutdown_policy"`
	OperatingMode        types.String `tfsdk:"operating_mode"`
	WaitForReady         types.Bool   `tfsdk:"wait_for_ready"`

	UID         types.String `tfsdk:"uid"`
	ServiceFQDN types.String `tfsdk:"service_fqdn"`
	ServiceName types.String `tfsdk:"service_name"`
	PodIPs      types.List   `tfsdk:"pod_ips"`
	NodeName    types.String `tfsdk:"node_name"`
	Selector    types.String `tfsdk:"selector"`
	Conditions  types.List   `tfsdk:"conditions"`

	Timeouts timeouts.Value `tfsdk:"timeouts"`
}
