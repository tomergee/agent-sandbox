// Package podtemplate implements the shared pod_template attribute used by
// the sandbox and sandbox_template resources: a typed subset of the
// Kubernetes PodSpec plus a raw spec_overrides escape hatch merged on top
// with strategic-merge-patch semantics.
package podtemplate

import (
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type Model struct {
	Metadata           types.Object `tfsdk:"metadata"`
	Containers         types.List   `tfsdk:"containers"`
	Volumes            types.List   `tfsdk:"volumes"`
	RestartPolicy      types.String `tfsdk:"restart_policy"`
	ServiceAccountName types.String `tfsdk:"service_account_name"`
	SpecOverrides      types.String `tfsdk:"spec_overrides"`
}

type MetadataModel struct {
	Labels      types.Map `tfsdk:"labels"`
	Annotations types.Map `tfsdk:"annotations"`
}

type ContainerModel struct {
	Name            types.String `tfsdk:"name"`
	Image           types.String `tfsdk:"image"`
	Command         types.List   `tfsdk:"command"`
	Args            types.List   `tfsdk:"args"`
	WorkingDir      types.String `tfsdk:"working_dir"`
	ImagePullPolicy types.String `tfsdk:"image_pull_policy"`
	Env             types.List   `tfsdk:"env"`
	Ports           types.List   `tfsdk:"ports"`
	Resources       types.Object `tfsdk:"resources"`
	VolumeMounts    types.List   `tfsdk:"volume_mounts"`
}

type EnvVarModel struct {
	Name            types.String `tfsdk:"name"`
	Value           types.String `tfsdk:"value"`
	ConfigMapKeyRef types.Object `tfsdk:"config_map_key_ref"`
	SecretKeyRef    types.Object `tfsdk:"secret_key_ref"`
	FieldRef        types.Object `tfsdk:"field_ref"`
}

type PortModel struct {
	Name          types.String `tfsdk:"name"`
	ContainerPort types.Int64  `tfsdk:"container_port"`
	Protocol      types.String `tfsdk:"protocol"`
}

type ResourcesModel struct {
	Limits   types.Map `tfsdk:"limits"`
	Requests types.Map `tfsdk:"requests"`
}

type VolumeMountModel struct {
	Name      types.String `tfsdk:"name"`
	MountPath types.String `tfsdk:"mount_path"`
	SubPath   types.String `tfsdk:"sub_path"`
	ReadOnly  types.Bool   `tfsdk:"read_only"`
}

type VolumeModel struct {
	Name                  types.String `tfsdk:"name"`
	EmptyDir              types.Object `tfsdk:"empty_dir"`
	ConfigMap             types.Object `tfsdk:"config_map"`
	Secret                types.Object `tfsdk:"secret"`
	PersistentVolumeClaim types.Object `tfsdk:"persistent_volume_claim"`
}

// Attribute type maps, needed to construct values during flatten.

var MetadataAttrTypes = map[string]attr.Type{
	"labels":      types.MapType{ElemType: types.StringType},
	"annotations": types.MapType{ElemType: types.StringType},
}

var KeyRefAttrTypes = map[string]attr.Type{
	"name": types.StringType,
	"key":  types.StringType,
}

var FieldRefAttrTypes = map[string]attr.Type{
	"field_path": types.StringType,
}

var EnvVarAttrTypes = map[string]attr.Type{
	"name":               types.StringType,
	"value":              types.StringType,
	"config_map_key_ref": types.ObjectType{AttrTypes: KeyRefAttrTypes},
	"secret_key_ref":     types.ObjectType{AttrTypes: KeyRefAttrTypes},
	"field_ref":          types.ObjectType{AttrTypes: FieldRefAttrTypes},
}

var PortAttrTypes = map[string]attr.Type{
	"name":           types.StringType,
	"container_port": types.Int64Type,
	"protocol":       types.StringType,
}

var ResourcesAttrTypes = map[string]attr.Type{
	"limits":   types.MapType{ElemType: types.StringType},
	"requests": types.MapType{ElemType: types.StringType},
}

var VolumeMountAttrTypes = map[string]attr.Type{
	"name":       types.StringType,
	"mount_path": types.StringType,
	"sub_path":   types.StringType,
	"read_only":  types.BoolType,
}

var ContainerAttrTypes = map[string]attr.Type{
	"name":              types.StringType,
	"image":             types.StringType,
	"command":           types.ListType{ElemType: types.StringType},
	"args":              types.ListType{ElemType: types.StringType},
	"working_dir":       types.StringType,
	"image_pull_policy": types.StringType,
	"env":               types.ListType{ElemType: types.ObjectType{AttrTypes: EnvVarAttrTypes}},
	"ports":             types.ListType{ElemType: types.ObjectType{AttrTypes: PortAttrTypes}},
	"resources":         types.ObjectType{AttrTypes: ResourcesAttrTypes},
	"volume_mounts":     types.ListType{ElemType: types.ObjectType{AttrTypes: VolumeMountAttrTypes}},
}

var EmptyDirAttrTypes = map[string]attr.Type{
	"medium":     types.StringType,
	"size_limit": types.StringType,
}

var NameRefAttrTypes = map[string]attr.Type{
	"name": types.StringType,
}

var SecretVolAttrTypes = map[string]attr.Type{
	"secret_name": types.StringType,
}

var PVCVolAttrTypes = map[string]attr.Type{
	"claim_name": types.StringType,
}

var VolumeAttrTypes = map[string]attr.Type{
	"name":                    types.StringType,
	"empty_dir":               types.ObjectType{AttrTypes: EmptyDirAttrTypes},
	"config_map":              types.ObjectType{AttrTypes: NameRefAttrTypes},
	"secret":                  types.ObjectType{AttrTypes: SecretVolAttrTypes},
	"persistent_volume_claim": types.ObjectType{AttrTypes: PVCVolAttrTypes},
}

var ModelAttrTypes = map[string]attr.Type{
	"metadata":             types.ObjectType{AttrTypes: MetadataAttrTypes},
	"containers":           types.ListType{ElemType: types.ObjectType{AttrTypes: ContainerAttrTypes}},
	"volumes":              types.ListType{ElemType: types.ObjectType{AttrTypes: VolumeAttrTypes}},
	"restart_policy":       types.StringType,
	"service_account_name": types.StringType,
	"spec_overrides":       types.StringType,
}
