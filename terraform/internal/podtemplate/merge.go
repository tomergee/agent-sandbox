package podtemplate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	sigsyaml "sigs.k8s.io/yaml"
)

// MergeOverrides applies a user-supplied PodSpec fragment (YAML or JSON) on
// top of the base PodSpec built from typed attributes, using Kubernetes
// strategic-merge-patch semantics with corev1.PodSpec patch metadata:
// overrides win over typed fields, keyed lists (containers by name, env by
// name, ports by containerPort) merge by key, unkeyed lists (command, args,
// tolerations) are replaced wholesale, and explicit nulls delete fields.
func MergeOverrides(base map[string]interface{}, overrides string) (map[string]interface{}, error) {
	if strings.TrimSpace(overrides) == "" {
		return base, nil
	}
	patchJSON, err := sigsyaml.YAMLToJSON([]byte(overrides))
	if err != nil {
		return nil, fmt.Errorf("parsing overrides as YAML/JSON: %w", err)
	}
	baseJSON, err := json.Marshal(base)
	if err != nil {
		return nil, fmt.Errorf("marshaling base pod spec: %w", err)
	}
	mergedJSON, err := strategicpatch.StrategicMergePatch(baseJSON, patchJSON, corev1.PodSpec{})
	if err != nil {
		return nil, fmt.Errorf("strategic merge of overrides onto pod spec: %w", err)
	}
	var merged map[string]interface{}
	if err := json.Unmarshal(mergedJSON, &merged); err != nil {
		return nil, fmt.Errorf("unmarshaling merged pod spec: %w", err)
	}
	return merged, nil
}

// OverridesValidator rejects spec_overrides fragments that are not valid
// PodSpec YAML/JSON or that contain unknown top-level fields, catching typos
// at plan time instead of at apply.
func OverridesValidator() validator.String {
	return overridesValidator{}
}

type overridesValidator struct{}

func (overridesValidator) Description(context.Context) string {
	return "must be a YAML or JSON fragment of a Kubernetes PodSpec"
}

func (v overridesValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (overridesValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	raw := req.ConfigValue.ValueString()
	if strings.TrimSpace(raw) == "" {
		return
	}
	jsonBytes, err := sigsyaml.YAMLToJSON([]byte(raw))
	if err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid spec_overrides",
			fmt.Sprintf("Value is not valid YAML or JSON: %s", err))
		return
	}
	// Strategic-merge directives ($patch, $retainKeys, ...) are not PodSpec
	// fields; skip the strict decode when they are present.
	if strings.Contains(string(jsonBytes), `"$`) {
		return
	}
	dec := json.NewDecoder(bytes.NewReader(jsonBytes))
	dec.DisallowUnknownFields()
	var spec corev1.PodSpec
	if err := dec.Decode(&spec); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid spec_overrides",
			fmt.Sprintf("Value is not a valid PodSpec fragment: %s", err))
	}
}
