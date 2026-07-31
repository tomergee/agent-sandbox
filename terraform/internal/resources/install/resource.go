// Package install implements agentsandbox_install: fetches a versioned
// agent-sandbox release manifest and server-side-applies it to the cluster,
// with upgrade (prune) and destroy support.
package install

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sschema "k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"

	"sigs.k8s.io/agent-sandbox/terraform/internal/client"
)

const installNamespace = "agent-sandbox-system"

var versionRe = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)

var (
	_ resource.Resource              = &Resource{}
	_ resource.ResourceWithConfigure = &Resource{}
)

type Resource struct {
	client *client.Client
}

func New() resource.Resource { return &Resource{} }

type model struct {
	ID             types.String `tfsdk:"id"`
	Version        types.String `tfsdk:"version"`
	Manifest       types.String `tfsdk:"manifest"`
	ManifestURL    types.String `tfsdk:"manifest_url"`
	FieldManager   types.String `tfsdk:"field_manager"`
	ForceConflicts types.Bool   `tfsdk:"force_conflicts"`
	Wait           types.Bool   `tfsdk:"wait"`
	CRDsOnDestroy  types.String `tfsdk:"crds_on_destroy"`

	Namespace      types.String `tfsdk:"namespace"`
	AppliedObjects types.List   `tfsdk:"applied_objects"`

	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_install"
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
		MarkdownDescription: "Installs (and upgrades) the agent-sandbox controller by server-side-applying " +
			"the upstream release manifest. The controller is installed into the `agent-sandbox-system` " +
			"namespace — the upstream conversion webhook configuration hardcodes that namespace, so it " +
			"cannot be overridden. CR resources in the same configuration should declare " +
			"`depends_on = [agentsandbox_install.<name>]`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true},
			"version": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Upstream release tag, e.g. `v0.5.4`. Must be `v0.5.2` or newer (earlier releases used different asset names).",
				Validators: []validator.String{
					stringvalidator.RegexMatches(versionRe, "must be a release tag like v0.5.4"),
				},
			},
			"manifest": schema.StringAttribute{
				Optional: true, Computed: true,
				Default: stringdefault.StaticString("sandbox-with-extensions"),
				Validators: []validator.String{
					stringvalidator.OneOf("sandbox-with-extensions", "sandbox", "extensions"),
				},
				MarkdownDescription: "Which release asset to install: the combined manifest (default), core only, or extensions only.",
			},
			"manifest_url": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Override URL for the manifest (air-gapped mirrors). `version` still keys upgrades.",
			},
			"field_manager": schema.StringAttribute{
				Optional: true, Computed: true,
				Default: stringdefault.StaticString(client.FieldManager),
			},
			"force_conflicts": schema.BoolAttribute{
				Optional: true, Computed: true,
				Default:             booldefault.StaticBool(true),
				MarkdownDescription: "Take ownership of fields previously managed by other tools (e.g. kubectl).",
			},
			"wait": schema.BoolAttribute{
				Optional: true, Computed: true,
				Default:             booldefault.StaticBool(true),
				MarkdownDescription: "Wait for CRDs to be established and controller deployments to become available.",
			},
			"crds_on_destroy": schema.StringAttribute{
				Optional: true, Computed: true,
				Default:    stringdefault.StaticString("delete"),
				Validators: []validator.String{stringvalidator.OneOf("delete", "retain")},
				MarkdownDescription: "`delete` removes CRDs (and thus all sandbox CRs) on destroy; `retain` " +
					"leaves CRDs, CRs, and the namespace behind.",
			},

			"namespace":       schema.StringAttribute{Computed: true},
			"applied_objects": schema.ListAttribute{Computed: true, ElementType: types.StringType},

			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

// installManifest fetches, parses, sorts, and applies the manifest, waiting
// for CRDs mid-stream and deployments at the end. Returns the applied keys
// in apply order.
func (r *Resource) installManifest(ctx context.Context, plan *model, timeout time.Duration, diags *diag.Diagnostics) []string {
	url := ManifestURL(plan.Version.ValueString(), plan.Manifest.ValueString(), plan.ManifestURL.ValueString())
	data, err := FetchManifest(ctx, url)
	if err != nil {
		diags.AddError("Failed to fetch release manifest", err.Error())
		return nil
	}
	objs, err := ParseManifest(data)
	if err != nil {
		diags.AddError("Failed to parse release manifest", err.Error())
		return nil
	}
	SortForApply(objs)

	fieldManager := plan.FieldManager.ValueString()
	force := plan.ForceConflicts.ValueBool()
	waitEnabled := plan.Wait.ValueBool()

	applied := make([]string, 0, len(objs))
	crdsApplied := false
	var deployments []*unstructured.Unstructured

	for _, obj := range objs {
		// Once past the CRD weight band, make sure CRDs are established and
		// the RESTMapper sees them before applying anything else.
		if crdsApplied && kindWeight(obj.GetKind()) > 1 {
			r.waitForAppliedCRDs(ctx, objs, timeout, waitEnabled, diags)
			if diags.HasError() {
				return applied
			}
			crdsApplied = false
		}
		if err := r.applyObject(ctx, obj, fieldManager, force); err != nil {
			diags.AddError(fmt.Sprintf("Failed to apply %s %s", obj.GetKind(), obj.GetName()), err.Error())
			return applied
		}
		applied = append(applied, ObjectKey(obj))
		if obj.GetKind() == "CustomResourceDefinition" {
			crdsApplied = true
		}
		if obj.GetKind() == "Deployment" {
			deployments = append(deployments, obj)
		}
	}

	if waitEnabled {
		for _, dep := range deployments {
			if err := r.client.WaitForDeploymentAvailable(ctx, dep.GetNamespace(), dep.GetName(), timeout); err != nil {
				diags.AddError("Controller deployment not available", err.Error())
				return applied
			}
		}
	}
	return applied
}

func (r *Resource) waitForAppliedCRDs(ctx context.Context, objs []*unstructured.Unstructured, timeout time.Duration, waitEnabled bool, diags *diag.Diagnostics) {
	if waitEnabled {
		for _, obj := range objs {
			if obj.GetKind() != "CustomResourceDefinition" {
				continue
			}
			if err := r.client.WaitForCRDEstablished(ctx, obj.GetName(), timeout); err != nil {
				diags.AddError("CRD did not become established", err.Error())
				return
			}
		}
	}
	// New CRDs invalidate cached discovery.
	r.client.Discovery.Invalidate()
	if m, ok := r.client.Mapper.(interface{ Reset() }); ok {
		m.Reset()
	}
}

// applyObject server-side-applies an arbitrary manifest object, resolving
// its GVR and scope through the RESTMapper.
func (r *Resource) applyObject(ctx context.Context, obj *unstructured.Unstructured, fieldManager string, force bool) error {
	ri, err := r.resourceInterface(obj)
	if err != nil {
		return err
	}
	_, err = ri.Apply(ctx, obj.GetName(), obj, metav1.ApplyOptions{FieldManager: fieldManager, Force: force})
	return err
}

func (r *Resource) resourceInterface(obj *unstructured.Unstructured) (dynamic.ResourceInterface, error) {
	gvk := obj.GroupVersionKind()
	mapping, err := r.client.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, fmt.Errorf("resolving REST mapping for %s: %w", gvk, err)
	}
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		ns := obj.GetNamespace()
		if ns == "" {
			ns = installNamespace
		}
		return r.client.Dynamic.Resource(mapping.Resource).Namespace(ns), nil
	}
	return r.client.Dynamic.Resource(mapping.Resource), nil
}

func (r *Resource) resourceInterfaceForKey(key string) (dynamic.ResourceInterface, string, error) {
	apiVersion, kind, namespace, name, err := ParseObjectKey(key)
	if err != nil {
		return nil, "", err
	}
	gv, err := k8sschema.ParseGroupVersion(apiVersion)
	if err != nil {
		return nil, "", err
	}
	mapping, err := r.client.Mapper.RESTMapping(k8sschema.GroupKind{Group: gv.Group, Kind: kind}, gv.Version)
	if err != nil {
		return nil, "", err
	}
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		return r.client.Dynamic.Resource(mapping.Resource).Namespace(namespace), name, nil
	}
	return r.client.Dynamic.Resource(mapping.Resource), name, nil
}

func (r *Resource) deleteByKey(ctx context.Context, key string) error {
	ri, name, err := r.resourceInterfaceForKey(key)
	if err != nil {
		return err
	}
	policy := metav1.DeletePropagationForeground
	err = ri.Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &policy})
	if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		return nil
	}
	return err
}

func (r *Resource) finish(ctx context.Context, plan *model, applied []string, diags *diag.Diagnostics) {
	plan.ID = types.StringValue("agent-sandbox/" + plan.Manifest.ValueString())
	plan.Namespace = types.StringValue(installNamespace)
	list, d := types.ListValueFrom(ctx, types.StringType, applied)
	diags.Append(d...)
	plan.AppliedObjects = list
}

func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Create(ctx, 15*time.Minute)
	resp.Diagnostics.Append(d...)

	applied := r.installManifest(ctx, &plan, timeout, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	r.finish(ctx, &plan, applied, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Update(ctx, 15*time.Minute)
	resp.Diagnostics.Append(d...)

	applied := r.installManifest(ctx, &plan, timeout, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// Prune objects the new manifest no longer contains.
	var prior []string
	resp.Diagnostics.Append(state.AppliedObjects.ElementsAs(ctx, &prior, false)...)
	for _, key := range DiffAppliedSets(prior, applied) {
		if err := r.deleteByKey(ctx, key); err != nil {
			resp.Diagnostics.AddWarning("Failed to prune removed object",
				fmt.Sprintf("%s: %s (delete it manually)", key, err))
		}
	}

	r.finish(ctx, &plan, applied, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var keys []string
	resp.Diagnostics.Append(state.AppliedObjects.ElementsAs(ctx, &keys, false)...)
	if len(keys) == 0 {
		return
	}

	// If every tracked object is gone, the install was removed out of band.
	anyPresent := false
	for _, key := range keys {
		ri, name, err := r.resourceInterfaceForKey(key)
		if err != nil {
			// Mapping failures (e.g. CRDs already gone) don't prove absence
			// of everything; skip.
			continue
		}
		if _, err := ri.Get(ctx, name, metav1.GetOptions{}); err == nil {
			anyPresent = true
			break
		}
	}
	if !anyPresent {
		resp.State.RemoveResource(ctx)
		return
	}
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

	var keys []string
	resp.Diagnostics.Append(state.AppliedObjects.ElementsAs(ctx, &keys, false)...)

	retainCRDs := state.CRDsOnDestroy.ValueString() == "retain"

	// Delete in reverse apply order.
	for i := len(keys) - 1; i >= 0; i-- {
		key := keys[i]
		_, kind, _, name, err := ParseObjectKey(key)
		if err != nil {
			continue
		}
		if retainCRDs && (kind == "CustomResourceDefinition" || (kind == "Namespace" && name == installNamespace)) {
			continue
		}
		if err := r.deleteByKey(ctx, key); err != nil {
			resp.Diagnostics.AddWarning("Failed to delete object", fmt.Sprintf("%s: %s", key, err))
		}
	}

	if !retainCRDs {
		// Namespace termination is the long pole; bound it by the timeout.
		err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
			_, err := r.client.Typed.CoreV1().Namespaces().Get(ctx, installNamespace, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return false, nil
		})
		if err != nil {
			resp.Diagnostics.AddWarning("Namespace still terminating",
				fmt.Sprintf("namespace %s did not finish terminating within the timeout", installNamespace))
		}
	}
}
