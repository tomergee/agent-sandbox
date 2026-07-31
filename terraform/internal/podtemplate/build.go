package podtemplate

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// BuildPodTemplate converts the typed model into the podTemplate value
// ({metadata, spec}) for the CR, applying spec_overrides on top of the typed
// PodSpec.
func BuildPodTemplate(ctx context.Context, m Model) (map[string]interface{}, diag.Diagnostics) {
	var diags diag.Diagnostics

	spec, d := buildPodSpec(ctx, m)
	diags.Append(d...)
	if diags.HasError() {
		return nil, diags
	}

	if !m.SpecOverrides.IsNull() && !m.SpecOverrides.IsUnknown() {
		merged, err := MergeOverrides(spec, m.SpecOverrides.ValueString())
		if err != nil {
			diags.AddError("Invalid pod_template.spec_overrides", err.Error())
			return nil, diags
		}
		spec = merged
	}

	tmpl := map[string]interface{}{"spec": spec}

	if !m.Metadata.IsNull() && !m.Metadata.IsUnknown() {
		var meta MetadataModel
		diags.Append(m.Metadata.As(ctx, &meta, objectAsOptions)...)
		metaMap := map[string]interface{}{}
		if labels := expandStringMap(ctx, meta.Labels, &diags); len(labels) > 0 {
			metaMap["labels"] = labels
		}
		if ann := expandStringMap(ctx, meta.Annotations, &diags); len(ann) > 0 {
			metaMap["annotations"] = ann
		}
		if len(metaMap) > 0 {
			tmpl["metadata"] = metaMap
		}
	}

	return tmpl, diags
}

func buildPodSpec(ctx context.Context, m Model) (map[string]interface{}, diag.Diagnostics) {
	var diags diag.Diagnostics
	spec := map[string]interface{}{}

	var containers []ContainerModel
	diags.Append(m.Containers.ElementsAs(ctx, &containers, false)...)
	if diags.HasError() {
		return nil, diags
	}
	built := make([]interface{}, 0, len(containers))
	for _, c := range containers {
		built = append(built, buildContainer(ctx, c, &diags))
	}
	spec["containers"] = built

	if !m.Volumes.IsNull() && !m.Volumes.IsUnknown() {
		var volumes []VolumeModel
		diags.Append(m.Volumes.ElementsAs(ctx, &volumes, false)...)
		vols := make([]interface{}, 0, len(volumes))
		for _, v := range volumes {
			vols = append(vols, buildVolume(ctx, v, &diags))
		}
		if len(vols) > 0 {
			spec["volumes"] = vols
		}
	}

	setString(spec, "restartPolicy", m.RestartPolicy)
	setString(spec, "serviceAccountName", m.ServiceAccountName)

	return spec, diags
}

func buildContainer(ctx context.Context, c ContainerModel, diags *diag.Diagnostics) map[string]interface{} {
	out := map[string]interface{}{
		"name":  c.Name.ValueString(),
		"image": c.Image.ValueString(),
	}
	if list := expandStringList(ctx, c.Command, diags); list != nil {
		out["command"] = list
	}
	if list := expandStringList(ctx, c.Args, diags); list != nil {
		out["args"] = list
	}
	setString(out, "workingDir", c.WorkingDir)
	setString(out, "imagePullPolicy", c.ImagePullPolicy)

	if !c.Env.IsNull() && !c.Env.IsUnknown() {
		var envs []EnvVarModel
		diags.Append(c.Env.ElementsAs(ctx, &envs, false)...)
		items := make([]interface{}, 0, len(envs))
		for _, e := range envs {
			items = append(items, buildEnvVar(ctx, e, diags))
		}
		if len(items) > 0 {
			out["env"] = items
		}
	}

	if !c.Ports.IsNull() && !c.Ports.IsUnknown() {
		var ports []PortModel
		diags.Append(c.Ports.ElementsAs(ctx, &ports, false)...)
		items := make([]interface{}, 0, len(ports))
		for _, p := range ports {
			port := map[string]interface{}{"containerPort": p.ContainerPort.ValueInt64()}
			setString(port, "name", p.Name)
			setString(port, "protocol", p.Protocol)
			items = append(items, port)
		}
		if len(items) > 0 {
			out["ports"] = items
		}
	}

	if !c.Resources.IsNull() && !c.Resources.IsUnknown() {
		var res ResourcesModel
		diags.Append(c.Resources.As(ctx, &res, objectAsOptions)...)
		resources := map[string]interface{}{}
		if limits := expandStringMap(ctx, res.Limits, diags); len(limits) > 0 {
			resources["limits"] = limits
		}
		if requests := expandStringMap(ctx, res.Requests, diags); len(requests) > 0 {
			resources["requests"] = requests
		}
		if len(resources) > 0 {
			out["resources"] = resources
		}
	}

	if !c.VolumeMounts.IsNull() && !c.VolumeMounts.IsUnknown() {
		var mounts []VolumeMountModel
		diags.Append(c.VolumeMounts.ElementsAs(ctx, &mounts, false)...)
		items := make([]interface{}, 0, len(mounts))
		for _, vm := range mounts {
			mount := map[string]interface{}{
				"name":      vm.Name.ValueString(),
				"mountPath": vm.MountPath.ValueString(),
			}
			setString(mount, "subPath", vm.SubPath)
			if !vm.ReadOnly.IsNull() && !vm.ReadOnly.IsUnknown() {
				mount["readOnly"] = vm.ReadOnly.ValueBool()
			}
			items = append(items, mount)
		}
		if len(items) > 0 {
			out["volumeMounts"] = items
		}
	}

	return out
}

func buildEnvVar(ctx context.Context, e EnvVarModel, diags *diag.Diagnostics) map[string]interface{} {
	out := map[string]interface{}{"name": e.Name.ValueString()}
	setString(out, "value", e.Value)

	valueFrom := map[string]interface{}{}
	if ref := expandKeyRef(ctx, e.ConfigMapKeyRef, diags); ref != nil {
		valueFrom["configMapKeyRef"] = ref
	}
	if ref := expandKeyRef(ctx, e.SecretKeyRef, diags); ref != nil {
		valueFrom["secretKeyRef"] = ref
	}
	if !e.FieldRef.IsNull() && !e.FieldRef.IsUnknown() {
		var fr struct {
			FieldPath types.String `tfsdk:"field_path"`
		}
		diags.Append(e.FieldRef.As(ctx, &fr, objectAsOptions)...)
		valueFrom["fieldRef"] = map[string]interface{}{"fieldPath": fr.FieldPath.ValueString()}
	}
	if len(valueFrom) > 0 {
		out["valueFrom"] = valueFrom
	}
	return out
}

func expandKeyRef(ctx context.Context, obj types.Object, diags *diag.Diagnostics) map[string]interface{} {
	if obj.IsNull() || obj.IsUnknown() {
		return nil
	}
	var ref struct {
		Name types.String `tfsdk:"name"`
		Key  types.String `tfsdk:"key"`
	}
	diags.Append(obj.As(ctx, &ref, objectAsOptions)...)
	return map[string]interface{}{"name": ref.Name.ValueString(), "key": ref.Key.ValueString()}
}

func buildVolume(ctx context.Context, v VolumeModel, diags *diag.Diagnostics) map[string]interface{} {
	out := map[string]interface{}{"name": v.Name.ValueString()}

	if !v.EmptyDir.IsNull() && !v.EmptyDir.IsUnknown() {
		var ed struct {
			Medium    types.String `tfsdk:"medium"`
			SizeLimit types.String `tfsdk:"size_limit"`
		}
		diags.Append(v.EmptyDir.As(ctx, &ed, objectAsOptions)...)
		emptyDir := map[string]interface{}{}
		setString(emptyDir, "medium", ed.Medium)
		setString(emptyDir, "sizeLimit", ed.SizeLimit)
		out["emptyDir"] = emptyDir
	}
	if !v.ConfigMap.IsNull() && !v.ConfigMap.IsUnknown() {
		var cm struct {
			Name types.String `tfsdk:"name"`
		}
		diags.Append(v.ConfigMap.As(ctx, &cm, objectAsOptions)...)
		out["configMap"] = map[string]interface{}{"name": cm.Name.ValueString()}
	}
	if !v.Secret.IsNull() && !v.Secret.IsUnknown() {
		var s struct {
			SecretName types.String `tfsdk:"secret_name"`
		}
		diags.Append(v.Secret.As(ctx, &s, objectAsOptions)...)
		out["secret"] = map[string]interface{}{"secretName": s.SecretName.ValueString()}
	}
	if !v.PersistentVolumeClaim.IsNull() && !v.PersistentVolumeClaim.IsUnknown() {
		var pvc struct {
			ClaimName types.String `tfsdk:"claim_name"`
		}
		diags.Append(v.PersistentVolumeClaim.As(ctx, &pvc, objectAsOptions)...)
		out["persistentVolumeClaim"] = map[string]interface{}{"claimName": pvc.ClaimName.ValueString()}
	}
	return out
}
