package podtemplate

import (
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Schema returns the shared pod_template attribute used by
// agentsandbox_sandbox and agentsandbox_sandbox_template.
func Schema() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		Required: true,
		MarkdownDescription: "Pod template for the sandbox. Common fields are typed; anything else " +
			"(runtimeClassName, tolerations, nodeSelector, securityContext, initContainers, ...) can be set " +
			"through `spec_overrides`, a YAML/JSON PodSpec fragment merged over the typed fields with " +
			"Kubernetes strategic-merge-patch semantics (overrides win; containers/env merge by name, " +
			"ports by containerPort; unkeyed lists such as command/args are replaced wholesale).",
		Attributes: map[string]schema.Attribute{
			"metadata": schema.SingleNestedAttribute{
				Optional: true,
				Attributes: map[string]schema.Attribute{
					"labels":      schema.MapAttribute{Optional: true, ElementType: types.StringType},
					"annotations": schema.MapAttribute{Optional: true, ElementType: types.StringType},
				},
			},
			"containers": schema.ListNestedAttribute{
				Required:   true,
				Validators: []validator.List{listvalidator.SizeAtLeast(1)},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name":        schema.StringAttribute{Required: true},
						"image":       schema.StringAttribute{Required: true},
						"command":     schema.ListAttribute{Optional: true, ElementType: types.StringType},
						"args":        schema.ListAttribute{Optional: true, ElementType: types.StringType},
						"working_dir": schema.StringAttribute{Optional: true},
						"image_pull_policy": schema.StringAttribute{
							Optional:   true,
							Validators: []validator.String{stringvalidator.OneOf("Always", "IfNotPresent", "Never")},
						},
						"env": schema.ListNestedAttribute{
							Optional: true,
							NestedObject: schema.NestedAttributeObject{
								Attributes: map[string]schema.Attribute{
									"name":  schema.StringAttribute{Required: true},
									"value": schema.StringAttribute{Optional: true},
									"config_map_key_ref": schema.SingleNestedAttribute{
										Optional: true,
										Attributes: map[string]schema.Attribute{
											"name": schema.StringAttribute{Required: true},
											"key":  schema.StringAttribute{Required: true},
										},
									},
									"secret_key_ref": schema.SingleNestedAttribute{
										Optional: true,
										Attributes: map[string]schema.Attribute{
											"name": schema.StringAttribute{Required: true},
											"key":  schema.StringAttribute{Required: true},
										},
									},
									"field_ref": schema.SingleNestedAttribute{
										Optional: true,
										Attributes: map[string]schema.Attribute{
											"field_path": schema.StringAttribute{Required: true},
										},
									},
								},
							},
						},
						"ports": schema.ListNestedAttribute{
							Optional: true,
							NestedObject: schema.NestedAttributeObject{
								Attributes: map[string]schema.Attribute{
									"name":           schema.StringAttribute{Optional: true},
									"container_port": schema.Int64Attribute{Required: true},
									"protocol": schema.StringAttribute{
										Optional:   true,
										Validators: []validator.String{stringvalidator.OneOf("TCP", "UDP", "SCTP")},
									},
								},
							},
						},
						"resources": schema.SingleNestedAttribute{
							Optional: true,
							Attributes: map[string]schema.Attribute{
								"limits":   schema.MapAttribute{Optional: true, ElementType: types.StringType},
								"requests": schema.MapAttribute{Optional: true, ElementType: types.StringType},
							},
							MarkdownDescription: "Resource limits/requests as quantity strings, e.g. `cpu = \"500m\"`.",
						},
						"volume_mounts": schema.ListNestedAttribute{
							Optional: true,
							NestedObject: schema.NestedAttributeObject{
								Attributes: map[string]schema.Attribute{
									"name":       schema.StringAttribute{Required: true},
									"mount_path": schema.StringAttribute{Required: true},
									"sub_path":   schema.StringAttribute{Optional: true},
									"read_only":  schema.BoolAttribute{Optional: true},
								},
							},
						},
					},
				},
			},
			"volumes": schema.ListNestedAttribute{
				Optional: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{Required: true},
						"empty_dir": schema.SingleNestedAttribute{
							Optional: true,
							Attributes: map[string]schema.Attribute{
								"medium":     schema.StringAttribute{Optional: true},
								"size_limit": schema.StringAttribute{Optional: true},
							},
						},
						"config_map": schema.SingleNestedAttribute{
							Optional: true,
							Attributes: map[string]schema.Attribute{
								"name": schema.StringAttribute{Required: true},
							},
						},
						"secret": schema.SingleNestedAttribute{
							Optional: true,
							Attributes: map[string]schema.Attribute{
								"secret_name": schema.StringAttribute{Required: true},
							},
						},
						"persistent_volume_claim": schema.SingleNestedAttribute{
							Optional: true,
							Attributes: map[string]schema.Attribute{
								"claim_name": schema.StringAttribute{Required: true},
							},
						},
					},
				},
			},
			"restart_policy": schema.StringAttribute{
				Optional:   true,
				Validators: []validator.String{stringvalidator.OneOf("Always", "OnFailure", "Never")},
			},
			"service_account_name": schema.StringAttribute{Optional: true},
			"spec_overrides": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "YAML or JSON PodSpec fragment strategically merged over the typed " +
					"fields. Use for fields not covered by the typed schema (e.g. `runtimeClassName: gvisor`). " +
					"Fields set here win over typed attributes.",
				Validators: []validator.String{OverridesValidator()},
			},
		},
	}
}
