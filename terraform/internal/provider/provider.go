// Package provider implements the agentsandbox Terraform provider for the
// OSS kubernetes-sigs/agent-sandbox controller.
package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"sigs.k8s.io/agent-sandbox/terraform/internal/client"
	sandboxds "sigs.k8s.io/agent-sandbox/terraform/internal/datasources/sandbox"
	"sigs.k8s.io/agent-sandbox/terraform/internal/resources/claim"
	"sigs.k8s.io/agent-sandbox/terraform/internal/resources/install"
	"sigs.k8s.io/agent-sandbox/terraform/internal/resources/sandbox"
	"sigs.k8s.io/agent-sandbox/terraform/internal/resources/sandboxtemplate"
	"sigs.k8s.io/agent-sandbox/terraform/internal/resources/warmpool"
)

var _ provider.Provider = &Provider{}

type Provider struct {
	version string
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &Provider{version: version}
	}
}

type providerModel struct {
	KubeconfigPath types.String `tfsdk:"kubeconfig_path"`
	Kubeconfig     types.String `tfsdk:"kubeconfig"`
	ConfigContext  types.String `tfsdk:"config_context"`
	InCluster      types.Bool   `tfsdk:"in_cluster"`
}

func (p *Provider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "agentsandbox"
	resp.Version = p.version
}

func (p *Provider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages [kubernetes-sigs/agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) " +
			"resources: controller installation plus Sandbox, SandboxTemplate, SandboxWarmPool, and SandboxClaim objects.",
		Attributes: map[string]schema.Attribute{
			"kubeconfig_path": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Path to a kubeconfig file. Falls back to `$KUBE_CONFIG_PATH`, " +
					"`$KUBECONFIG`, then `~/.kube/config`.",
			},
			"kubeconfig": schema.StringAttribute{
				Optional:            true,
				Sensitive:           true,
				MarkdownDescription: "Raw kubeconfig YAML content. Mutually exclusive with `kubeconfig_path`.",
			},
			"config_context": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Kubeconfig context to use. Falls back to `$KUBE_CTX`, then the current context.",
			},
			"in_cluster": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Use in-cluster service-account credentials instead of a kubeconfig.",
			},
		},
	}
}

func (p *Provider) ValidateConfig(ctx context.Context, req provider.ValidateConfigRequest, resp *provider.ValidateConfigResponse) {
	var config providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !config.Kubeconfig.IsNull() && !config.KubeconfigPath.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("kubeconfig"),
			"Conflicting provider configuration",
			"kubeconfig and kubeconfig_path are mutually exclusive.")
	}
}

func (p *Provider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// During validate/early plan, values may be unknown; defer client
	// construction failures to first use rather than erroring here.
	if config.Kubeconfig.IsUnknown() || config.KubeconfigPath.IsUnknown() ||
		config.ConfigContext.IsUnknown() || config.InCluster.IsUnknown() {
		return
	}

	c, err := client.New(client.Config{
		KubeconfigPath: config.KubeconfigPath.ValueString(),
		KubeconfigRaw:  config.Kubeconfig.ValueString(),
		Context:        config.ConfigContext.ValueString(),
		InCluster:      config.InCluster.ValueBool(),
		UserAgent:      fmt.Sprintf("terraform-provider-agentsandbox/%s", p.version),
	})
	if err != nil {
		resp.Diagnostics.AddError("Failed to configure Kubernetes client", err.Error())
		return
	}
	resp.ResourceData = c
	resp.DataSourceData = c
}

func (p *Provider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		install.New,
		sandbox.New,
		sandboxtemplate.New,
		warmpool.New,
		claim.New,
	}
}

func (p *Provider) DataSources(context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		sandboxds.New,
	}
}
