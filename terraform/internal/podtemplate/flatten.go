package podtemplate

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// FlattenPodTemplate maps a live podTemplate ({metadata, spec}) back onto the
// typed model for Read/import. Only fields the typed schema covers are read;
// spec_overrides is copied from prior state (its contents cannot be recovered
// from the live object).
func FlattenPodTemplate(ctx context.Context, tmpl map[string]interface{}, prior Model) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics

	metaObj := types.ObjectNull(MetadataAttrTypes)
	if meta, ok := tmpl["metadata"].(map[string]interface{}); ok {
		labels := flattenStringMap(meta["labels"])
		annotations := flattenStringMap(meta["annotations"])
		if !labels.IsNull() || !annotations.IsNull() {
			obj, d := types.ObjectValue(MetadataAttrTypes, map[string]attr.Value{
				"labels":      labels,
				"annotations": annotations,
			})
			diags.Append(d...)
			metaObj = obj
		}
	}

	spec, _ := tmpl["spec"].(map[string]interface{})

	containers := types.ListNull(types.ObjectType{AttrTypes: ContainerAttrTypes})
	if spec != nil {
		if raw, ok := spec["containers"].([]interface{}); ok {
			vals := make([]attr.Value, 0, len(raw))
			for _, item := range raw {
				c, ok := item.(map[string]interface{})
				if !ok {
					continue
				}
				obj, d := flattenContainer(ctx, c)
				diags.Append(d...)
				vals = append(vals, obj)
			}
			list, d := types.ListValue(types.ObjectType{AttrTypes: ContainerAttrTypes}, vals)
			diags.Append(d...)
			containers = list
		}
	}

	volumes := types.ListNull(types.ObjectType{AttrTypes: VolumeAttrTypes})
	if spec != nil {
		if raw, ok := spec["volumes"].([]interface{}); ok && len(raw) > 0 {
			vals := make([]attr.Value, 0, len(raw))
			for _, item := range raw {
				v, ok := item.(map[string]interface{})
				if !ok {
					continue
				}
				obj, d := flattenVolume(v)
				diags.Append(d...)
				vals = append(vals, obj)
			}
			list, d := types.ListValue(types.ObjectType{AttrTypes: VolumeAttrTypes}, vals)
			diags.Append(d...)
			volumes = list
		}
	}

	restartPolicy := types.StringNull()
	serviceAccount := types.StringNull()
	if spec != nil {
		restartPolicy = strValue(spec["restartPolicy"])
		serviceAccount = strValue(spec["serviceAccountName"])
	}

	obj, d := types.ObjectValue(ModelAttrTypes, map[string]attr.Value{
		"metadata":             metaObj,
		"containers":           containers,
		"volumes":              volumes,
		"restart_policy":       restartPolicy,
		"service_account_name": serviceAccount,
		"spec_overrides":       prior.SpecOverrides,
	})
	diags.Append(d...)
	return obj, diags
}

func flattenContainer(ctx context.Context, c map[string]interface{}) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics

	env := types.ListNull(types.ObjectType{AttrTypes: EnvVarAttrTypes})
	if raw, ok := c["env"].([]interface{}); ok && len(raw) > 0 {
		vals := make([]attr.Value, 0, len(raw))
		for _, item := range raw {
			e, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			obj, d := flattenEnvVar(e)
			diags.Append(d...)
			vals = append(vals, obj)
		}
		list, d := types.ListValue(types.ObjectType{AttrTypes: EnvVarAttrTypes}, vals)
		diags.Append(d...)
		env = list
	}

	ports := types.ListNull(types.ObjectType{AttrTypes: PortAttrTypes})
	if raw, ok := c["ports"].([]interface{}); ok && len(raw) > 0 {
		vals := make([]attr.Value, 0, len(raw))
		for _, item := range raw {
			p, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			port := types.Int64Null()
			if n, ok := p["containerPort"].(int64); ok {
				port = types.Int64Value(n)
			} else if f, ok := p["containerPort"].(float64); ok {
				port = types.Int64Value(int64(f))
			}
			obj, d := types.ObjectValue(PortAttrTypes, map[string]attr.Value{
				"name":           strValue(p["name"]),
				"container_port": port,
				"protocol":       strValue(p["protocol"]),
			})
			diags.Append(d...)
			vals = append(vals, obj)
		}
		list, d := types.ListValue(types.ObjectType{AttrTypes: PortAttrTypes}, vals)
		diags.Append(d...)
		ports = list
	}

	resources := types.ObjectNull(ResourcesAttrTypes)
	if res, ok := c["resources"].(map[string]interface{}); ok && len(res) > 0 {
		obj, d := types.ObjectValue(ResourcesAttrTypes, map[string]attr.Value{
			"limits":   flattenStringMap(res["limits"]),
			"requests": flattenStringMap(res["requests"]),
		})
		diags.Append(d...)
		resources = obj
	}

	mounts := types.ListNull(types.ObjectType{AttrTypes: VolumeMountAttrTypes})
	if raw, ok := c["volumeMounts"].([]interface{}); ok && len(raw) > 0 {
		vals := make([]attr.Value, 0, len(raw))
		for _, item := range raw {
			vm, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			readOnly := types.BoolNull()
			if b, ok := vm["readOnly"].(bool); ok {
				readOnly = types.BoolValue(b)
			}
			obj, d := types.ObjectValue(VolumeMountAttrTypes, map[string]attr.Value{
				"name":       strValue(vm["name"]),
				"mount_path": strValue(vm["mountPath"]),
				"sub_path":   strValue(vm["subPath"]),
				"read_only":  readOnly,
			})
			diags.Append(d...)
			vals = append(vals, obj)
		}
		list, d := types.ListValue(types.ObjectType{AttrTypes: VolumeMountAttrTypes}, vals)
		diags.Append(d...)
		mounts = list
	}

	obj, d := types.ObjectValue(ContainerAttrTypes, map[string]attr.Value{
		"name":              strValue(c["name"]),
		"image":             strValue(c["image"]),
		"command":           flattenStringList(c["command"]),
		"args":              flattenStringList(c["args"]),
		"working_dir":       strValue(c["workingDir"]),
		"image_pull_policy": strValue(c["imagePullPolicy"]),
		"env":               env,
		"ports":             ports,
		"resources":         resources,
		"volume_mounts":     mounts,
	})
	diags.Append(d...)
	return obj, diags
}

func flattenEnvVar(e map[string]interface{}) (types.Object, diag.Diagnostics) {
	cmRef := types.ObjectNull(KeyRefAttrTypes)
	secretRef := types.ObjectNull(KeyRefAttrTypes)
	fieldRef := types.ObjectNull(FieldRefAttrTypes)
	if vf, ok := e["valueFrom"].(map[string]interface{}); ok {
		if ref, ok := vf["configMapKeyRef"].(map[string]interface{}); ok {
			cmRef, _ = types.ObjectValue(KeyRefAttrTypes, map[string]attr.Value{
				"name": strValue(ref["name"]), "key": strValue(ref["key"]),
			})
		}
		if ref, ok := vf["secretKeyRef"].(map[string]interface{}); ok {
			secretRef, _ = types.ObjectValue(KeyRefAttrTypes, map[string]attr.Value{
				"name": strValue(ref["name"]), "key": strValue(ref["key"]),
			})
		}
		if ref, ok := vf["fieldRef"].(map[string]interface{}); ok {
			fieldRef, _ = types.ObjectValue(FieldRefAttrTypes, map[string]attr.Value{
				"field_path": strValue(ref["fieldPath"]),
			})
		}
	}
	return types.ObjectValue(EnvVarAttrTypes, map[string]attr.Value{
		"name":               strValue(e["name"]),
		"value":              strValue(e["value"]),
		"config_map_key_ref": cmRef,
		"secret_key_ref":     secretRef,
		"field_ref":          fieldRef,
	})
}

func flattenVolume(v map[string]interface{}) (types.Object, diag.Diagnostics) {
	emptyDir := types.ObjectNull(EmptyDirAttrTypes)
	if ed, ok := v["emptyDir"].(map[string]interface{}); ok {
		emptyDir, _ = types.ObjectValue(EmptyDirAttrTypes, map[string]attr.Value{
			"medium": strValue(ed["medium"]), "size_limit": strValue(ed["sizeLimit"]),
		})
	}
	configMap := types.ObjectNull(NameRefAttrTypes)
	if cm, ok := v["configMap"].(map[string]interface{}); ok {
		configMap, _ = types.ObjectValue(NameRefAttrTypes, map[string]attr.Value{
			"name": strValue(cm["name"]),
		})
	}
	secret := types.ObjectNull(SecretVolAttrTypes)
	if s, ok := v["secret"].(map[string]interface{}); ok {
		secret, _ = types.ObjectValue(SecretVolAttrTypes, map[string]attr.Value{
			"secret_name": strValue(s["secretName"]),
		})
	}
	pvc := types.ObjectNull(PVCVolAttrTypes)
	if p, ok := v["persistentVolumeClaim"].(map[string]interface{}); ok {
		pvc, _ = types.ObjectValue(PVCVolAttrTypes, map[string]attr.Value{
			"claim_name": strValue(p["claimName"]),
		})
	}
	return types.ObjectValue(VolumeAttrTypes, map[string]attr.Value{
		"name":                    strValue(v["name"]),
		"empty_dir":               emptyDir,
		"config_map":              configMap,
		"secret":                  secret,
		"persistent_volume_claim": pvc,
	})
}
