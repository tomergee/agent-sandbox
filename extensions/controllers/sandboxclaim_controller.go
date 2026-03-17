// Copyright 2025 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controllers

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	k8errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	sandboxcontrollers "sigs.k8s.io/agent-sandbox/controllers"
	extensionsv1alpha1 "sigs.k8s.io/agent-sandbox/extensions/api/v1alpha1"
	asmetrics "sigs.k8s.io/agent-sandbox/internal/metrics"
)

// TODO: These constants should be imported from the main controller package Issue #216
const (
	sandboxLabel = "agents.x-k8s.io/sandbox-name-hash"
)


// ErrTemplateNotFound is a sentinel error indicating a SandboxTemplate was not found.
var ErrTemplateNotFound = errors.New("SandboxTemplate not found")

// SandboxClaimReconciler reconciles a SandboxClaim object
type SandboxClaimReconciler struct {
	client.Client
	Scheme                  *runtime.Scheme
	Recorder                record.EventRecorder
	Tracer                  asmetrics.Instrumenter
	MaxConcurrentReconciles int
	readyClaims             sync.Map
}

//+kubebuilder:rbac:groups=extensions.agents.x-k8s.io,resources=sandboxclaims,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=extensions.agents.x-k8s.io,resources=sandboxclaims/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=agents.x-k8s.io,resources=sandboxes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=extensions.agents.x-k8s.io,resources=sandboxtemplates,verbs=get;list;watch
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch
//+kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *SandboxClaimReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	claim := &extensionsv1alpha1.SandboxClaim{}
	if err := r.Get(ctx, req.NamespacedName, claim); err != nil {
		if k8errors.IsNotFound(err) {
			key := req.Namespace + "/" + req.Name
			r.readyClaims.Delete(key)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get sandbox claim %q: %w", req.NamespacedName, err)
	}

	// Start Tracing Span
	ctx, end := r.Tracer.StartSpan(ctx, claim, "ReconcileSandboxClaim", nil)
	defer end()

	if !claim.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	// Initialize trace ID for active resources missing an ID
	tc := r.Tracer.GetTraceContext(ctx)
	if tc != "" && (claim.Annotations == nil || claim.Annotations[asmetrics.TraceContextAnnotation] == "") {
		patch := client.MergeFrom(claim.DeepCopy())
		if claim.Annotations == nil {
			claim.Annotations = make(map[string]string)
		}
		claim.Annotations[asmetrics.TraceContextAnnotation] = tc
		if err := r.Patch(ctx, claim, patch); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	originalClaimStatus := claim.Status.DeepCopy()

	// Check Expiration
	// We calculate this upfront to decide the flow.
	claimExpired, timeLeft := r.checkExpiration(claim)

	// Handle "Delete" Policy immediately
	// If we delete the claim, we return immediately.
	// Continuing would try to update the status of a deleted object, causing a crash/error.
	if claimExpired && claim.Spec.Lifecycle != nil && claim.Spec.Lifecycle.ShutdownPolicy == extensionsv1alpha1.ShutdownPolicyDelete {
		log.Info("Deleting Claim because ShutdownPolicy=Delete and time has expired")
		if r.Recorder != nil {
			r.Recorder.Event(claim, corev1.EventTypeNormal, extensionsv1alpha1.ClaimExpiredReason, "Deleting Claim (ShutdownPolicy=Delete)")
		}
		if err := r.Delete(ctx, claim); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		return ctrl.Result{}, nil
	}

	// Manage Resources based on State
	var sandbox *v1alpha1.Sandbox
	var reconcileErr error

	if claimExpired {
		// Policy=Retain (since Delete handled above)
		// Ensure Sandbox is deleted, but keep the Claim.
		sandbox, reconcileErr = r.reconcileExpired(ctx, claim)
	} else {
		// Ensure Sandbox exists and is configured.
		sandbox, reconcileErr = r.reconcileActive(ctx, claim)
	}

	// Update Status & Events
	r.computeAndSetStatus(claim, sandbox, reconcileErr, claimExpired)

	if !hasExpiredCondition(originalClaimStatus.Conditions) && hasExpiredCondition(claim.Status.Conditions) {
		if r.Recorder != nil {
			r.Recorder.Event(claim, corev1.EventTypeNormal, extensionsv1alpha1.ClaimExpiredReason, "Claim expired")
		}
	}

	if updateErr := r.updateStatus(ctx, originalClaimStatus, claim); updateErr != nil {
		return ctrl.Result{}, errors.Join(reconcileErr, updateErr)
	}

	r.recordCreationLatencyMetric(ctx, claim, *originalClaimStatus, sandbox)


	// Determine Result
	var result ctrl.Result
	if !claimExpired && timeLeft > 0 {
		result = ctrl.Result{RequeueAfter: timeLeft}
	}

	// Suppress expected user errors (like missing templates) to avoid crash loops
	if errors.Is(reconcileErr, ErrTemplateNotFound) {
		return result, nil
	}

	return result, reconcileErr
}

// checkExpiration calculates if the claim is expired and how much time is left.
func (r *SandboxClaimReconciler) checkExpiration(claim *extensionsv1alpha1.SandboxClaim) (bool, time.Duration) {
	if claim.Spec.Lifecycle == nil || claim.Spec.Lifecycle.ShutdownTime == nil {
		return false, 0
	}

	now := time.Now()
	expiry := claim.Spec.Lifecycle.ShutdownTime.Time

	if now.After(expiry) {
		return true, 0
	}

	return false, expiry.Sub(now)
}

// reconcileActive handles the creation and updates of running sandboxes.
func (r *SandboxClaimReconciler) reconcileActive(ctx context.Context, claim *extensionsv1alpha1.SandboxClaim) (*v1alpha1.Sandbox, error) {
	// Try getting template
	template, templateErr := r.getTemplate(ctx, claim)

	// If getting template failed with a real error (not just NotFound), fail fast.
	if templateErr != nil && !k8errors.IsNotFound(templateErr) {
		return nil, templateErr
	}

	// Only attempt network policy reconciliation if template was found.
	if templateErr == nil || k8errors.IsNotFound(templateErr) {
		// This ensures the firewall is up before the pod starts.
		if template != nil {
			if npErr := r.reconcileNetworkPolicy(ctx, claim, template); npErr != nil {
				return nil, fmt.Errorf("failed to reconcile network policy: %w", npErr)
			}
		}

		// Try getting sandbox even if template is not found
		// It is possible that the template was deleted after the sandbox was created
		sandbox, err := r.getOrCreateSandbox(ctx, claim, template)

		// Special Case: If we failed to create because template was missing, return that specific error
		if sandbox == nil && err == nil && templateErr != nil {
			return nil, ErrTemplateNotFound
		}
		return sandbox, err
	}

	return nil, templateErr
}

// reconcileExpired ensures the Sandbox is deleted for Retained claims.
func (r *SandboxClaimReconciler) reconcileExpired(ctx context.Context, claim *extensionsv1alpha1.SandboxClaim) (*v1alpha1.Sandbox, error) {
	log := log.FromContext(ctx)
	sandbox := &v1alpha1.Sandbox{}

	// Check if Sandbox exists
	if err := r.Get(ctx, client.ObjectKeyFromObject(claim), sandbox); err != nil {
		if k8errors.IsNotFound(err) {
			return nil, nil // Sandbox is gone, life is good.
		}
		return nil, err
	}

	// Sandbox exists, delete it.
	if sandbox.DeletionTimestamp.IsZero() {
		log.Info("Deleting Sandbox because Claim expired (Policy=Retain)", "Sandbox", sandbox.Name)
		if err := r.Delete(ctx, sandbox); err != nil {
			return sandbox, fmt.Errorf("failed to delete expired sandbox: %w", err)
		}
	}

	return sandbox, nil
}

func (r *SandboxClaimReconciler) updateStatus(ctx context.Context, oldStatus *extensionsv1alpha1.SandboxClaimStatus, claim *extensionsv1alpha1.SandboxClaim) error {
	log := log.FromContext(ctx)

	sort.Slice(oldStatus.Conditions, func(i, j int) bool {
		return oldStatus.Conditions[i].Type < oldStatus.Conditions[j].Type
	})
	sort.Slice(claim.Status.Conditions, func(i, j int) bool {
		return claim.Status.Conditions[i].Type < claim.Status.Conditions[j].Type
	})

	if reflect.DeepEqual(oldStatus, &claim.Status) {
		return nil
	}

	oldClaim := claim.DeepCopy()
	oldClaim.Status = *oldStatus
	patch := client.MergeFrom(oldClaim)
	if err := r.Status().Patch(ctx, claim, patch); err != nil {
		log.Error(err, "Failed to patch sandboxclaim status")
		return err
	}

	return nil
}

func (r *SandboxClaimReconciler) computeReadyCondition(claim *extensionsv1alpha1.SandboxClaim, sandbox *v1alpha1.Sandbox, err error, isClaimExpired bool) metav1.Condition {
	if err != nil {
		reason := "ReconcilerError"
		if errors.Is(err, ErrTemplateNotFound) {
			reason = "TemplateNotFound"
			return metav1.Condition{
				Type:               string(v1alpha1.SandboxConditionReady),
				Status:             metav1.ConditionFalse,
				Reason:             reason,
				Message:            fmt.Sprintf("SandboxTemplate %q not found", claim.Spec.TemplateRef.Name),
				ObservedGeneration: claim.Generation,
			}
		}
		return metav1.Condition{
			Type:               string(v1alpha1.SandboxConditionReady),
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            "Error seen: " + err.Error(),
			ObservedGeneration: claim.Generation,
		}
	}

	if isClaimExpired {
		return metav1.Condition{
			Type:               string(v1alpha1.SandboxConditionReady),
			Status:             metav1.ConditionFalse,
			Reason:             extensionsv1alpha1.ClaimExpiredReason,
			Message:            "Claim expired. Sandbox resources deleted.",
			ObservedGeneration: claim.Generation,
		}
	}

	if sandbox == nil {
		// Only handle genuine missing sandbox here (expired case is handled above)
		return metav1.Condition{
			Type:               string(v1alpha1.SandboxConditionReady),
			Status:             metav1.ConditionFalse,
			Reason:             "SandboxMissing",
			Message:            "Sandbox does not exist",
			ObservedGeneration: claim.Generation,
		}
	}

	// Check if Core Controller marked it as Expired
	if isSandboxExpired(sandbox) {
		return metav1.Condition{
			Type:               string(v1alpha1.SandboxConditionReady),
			Status:             metav1.ConditionFalse,
			Reason:             v1alpha1.SandboxReasonExpired,
			Message:            "Underlying Sandbox resource has expired independently of the Claim.",
			ObservedGeneration: claim.Generation,
		}
	}

	// Forward the condition from Sandbox Status
	for _, condition := range sandbox.Status.Conditions {
		if condition.Type == string(v1alpha1.SandboxConditionReady) {
			return condition
		}
	}

	return metav1.Condition{
		Type:               string(v1alpha1.SandboxConditionReady),
		Status:             metav1.ConditionFalse,
		Reason:             "SandboxNotReady",
		Message:            "Sandbox is not ready",
		ObservedGeneration: claim.Generation,
	}
}

func (r *SandboxClaimReconciler) computeAndSetStatus(claim *extensionsv1alpha1.SandboxClaim, sandbox *v1alpha1.Sandbox, err error, isClaimExpired bool) {
	readyCondition := r.computeReadyCondition(claim, sandbox, err, isClaimExpired)
	meta.SetStatusCondition(&claim.Status.Conditions, readyCondition)

	if sandbox != nil {
		claim.Status.SandboxStatus.Name = sandbox.Name
	}
}

// tryAdoptSandboxFromPool attempts to adopt an existing Sandbox from the WarmPool.
func (r *SandboxClaimReconciler) tryAdoptSandboxFromPool(ctx context.Context, claim *extensionsv1alpha1.SandboxClaim, template *extensionsv1alpha1.SandboxTemplate) (*v1alpha1.Sandbox, error) {
	log := log.FromContext(ctx)

	sandboxList := &v1alpha1.SandboxList{}

	// Filter by TemplateRef
	err := r.List(ctx, sandboxList, &client.ListOptions{
		Namespace: claim.Namespace,
		LabelSelector: labels.SelectorFromSet(labels.Set{
			sandboxTemplateRefHash: sandboxcontrollers.NameHash(claim.Spec.TemplateRef.Name),
		}),
	})
	if err != nil || len(sandboxList.Items) == 0 {
		return nil, err
	}

	// Sort Sandboxes by creation timestamp (oldest first) to ensure FIFO
	sort.Slice(sandboxList.Items, func(i, j int) bool {
		return sandboxList.Items[i].CreationTimestamp.Time.Before(sandboxList.Items[j].CreationTimestamp.Time)
	})

	// Determine the search range for collision avoidance.
	n := len(sandboxList.Items)
	workerCount := r.MaxConcurrentReconciles
	if workerCount <= 0 {
		workerCount = 1
	}
	searchWindow := n
	if workerCount < searchWindow {
		searchWindow = workerCount
	}
	// Compute a starting index deterministic to this specific Claim UID.
	hashValue := sandboxcontrollers.GetNumericHash(string(claim.UID))
	startIndex := int(hashValue % uint32(searchWindow))

	// Iterate through the entire list starting from the hashed offset.
	for i := range n {
		currIndex := (startIndex + i) % n
		staleSandbox := sandboxList.Items[currIndex]
		if !staleSandbox.DeletionTimestamp.IsZero() {
			continue
		}

		// Fetch fresh copy to avoid double adoption
		sandbox := &v1alpha1.Sandbox{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: staleSandbox.Namespace, Name: staleSandbox.Name}, sandbox); err != nil {
			log.Error(err, "Failed to fetch fresh sandbox for adoption", "sandbox", staleSandbox.Name)
			continue
		}

		// Check ownership
		controllerRef := metav1.GetControllerOf(sandbox)
		if controllerRef == nil || controllerRef.Kind != "SandboxWarmPool" {
			continue // Skip sandboxes not managed by our warm pool
		}
		// Check if sandbox is ready
		isReady := false
		for _, condition := range sandbox.Status.Conditions {
			if condition.Type == string(v1alpha1.SandboxConditionReady) && condition.Status == metav1.ConditionTrue {
				isReady = true
				break
			}
		}
		if !isReady {
			continue
		}

		// Check if it has a Claim UID label (should not)
		if _, exists := sandbox.Labels[extensionsv1alpha1.SandboxIDLabel]; exists {
			continue
		}

		log.Info("Attempting to adopt sandbox from warm pool", "sandbox", sandbox.Name)

		// Remove old controller ref
		var newOwnerRefs []metav1.OwnerReference
		for _, ref := range sandbox.OwnerReferences {
			if ref.Controller != nil && *ref.Controller && ref.UID == controllerRef.UID {
				continue // Skip the warmpool ref
			}
			newOwnerRefs = append(newOwnerRefs, ref)
		}
		sandbox.OwnerReferences = newOwnerRefs

		// Add new controller ref (SandboxClaim)
		if err := controllerutil.SetControllerReference(claim, sandbox, r.Scheme); err != nil {
			log.Error(err, "Failed to set controller reference for adopted sandbox", "sandbox", sandbox.Name)
			continue
		}

		// Set labels
		if sandbox.Labels == nil {
			sandbox.Labels = make(map[string]string)
		}
		sandbox.Labels[extensionsv1alpha1.SandboxIDLabel] = string(claim.UID)
		
		// Remove the pool label so the warm pool controller stops reconciling this sandbox
		delete(sandbox.Labels, poolLabel)
		delete(sandbox.Labels, sandboxTemplateRefHash)

		// Use Update instead of Patch for Conflict detection
		if err := r.Update(ctx, sandbox); err != nil {
			log.Error(err, "Failed to update sandbox for adoption", "sandbox", sandbox.Name)
			continue
		}

		log.Info("Successfully adopted sandbox from warm pool", "sandbox", sandbox.Name)
		return sandbox, nil
	}

	return nil, nil // No suitable sandbox found or failed to adopt
}

func (r *SandboxClaimReconciler) createSandbox(ctx context.Context, claim *extensionsv1alpha1.SandboxClaim, template *extensionsv1alpha1.SandboxTemplate) (*v1alpha1.Sandbox, error) {
	logger := log.FromContext(ctx)

	if template == nil {
		logger.Error(ErrTemplateNotFound, "cannot create sandbox")
		return nil, ErrTemplateNotFound
	}

	logger.Info("creating sandbox from template", "template", template.Name)
	sandbox := &v1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: claim.Namespace,
			Name:      claim.Name,
		},
	}

	// Propagate the trace context annotation to the Sandbox resource
	if sandbox.Annotations == nil {
		sandbox.Annotations = make(map[string]string)
	}
	if tc, ok := claim.Annotations[asmetrics.TraceContextAnnotation]; ok {
		sandbox.Annotations[asmetrics.TraceContextAnnotation] = tc
	}

	template.Spec.PodTemplate.DeepCopyInto(&sandbox.Spec.PodTemplate)
	// TODO: this is a workaround, remove replica assignment related issue #202
	replicas := int32(1)
	sandbox.Spec.Replicas = &replicas
	// Enforce a secure-by-default policy by disabling the automatic mounting
	// of the service account token, adhering to security best practices for
	// sandboxed environments.
	if sandbox.Spec.PodTemplate.Spec.AutomountServiceAccountToken == nil {
		automount := false
		sandbox.Spec.PodTemplate.Spec.AutomountServiceAccountToken = &automount
	}
	if sandbox.Spec.PodTemplate.ObjectMeta.Labels == nil {
		sandbox.Spec.PodTemplate.ObjectMeta.Labels = make(map[string]string)
	}
	sandbox.Spec.PodTemplate.ObjectMeta.Labels[extensionsv1alpha1.SandboxIDLabel] = string(claim.UID)

	// Set the SandboxIDLabel on the Sandbox itself as well
	if sandbox.Labels == nil {
		sandbox.Labels = make(map[string]string)
	}
	sandbox.Labels[extensionsv1alpha1.SandboxIDLabel] = string(claim.UID)

	if err := controllerutil.SetControllerReference(claim, sandbox, r.Scheme); err != nil {
		err = fmt.Errorf("failed to set controller reference for sandbox: %w", err)
		logger.Error(err, "Error creating sandbox for claim", "claimName", claim.Name)
		return nil, err
	}

	if err := r.Create(ctx, sandbox); err != nil {
		err = fmt.Errorf("sandbox create error: %w", err)
		logger.Error(err, "Error creating sandbox for claim", "claimName", claim.Name)
		return nil, err
	}

	logger.Info("Created sandbox for claim", "claim", claim.Name)

	if r.Recorder != nil {
		r.Recorder.Event(claim, corev1.EventTypeNormal, "SandboxProvisioned", fmt.Sprintf("Created Sandbox %q", sandbox.Name))
	}

	return sandbox, nil
}

func (r *SandboxClaimReconciler) getOrCreateSandbox(ctx context.Context, claim *extensionsv1alpha1.SandboxClaim, template *extensionsv1alpha1.SandboxTemplate) (*v1alpha1.Sandbox, error) {
	logger := log.FromContext(ctx)

	// 1. Check if we already have a sandbox name in status
	if claim.Status.SandboxStatus.Name != "" {
		sandbox := &v1alpha1.Sandbox{}
		err := r.Get(ctx, types.NamespacedName{Namespace: claim.Namespace, Name: claim.Status.SandboxStatus.Name}, sandbox)
		if err == nil {
			logger.Info("Found existing sandbox from status", "name", sandbox.Name)
			if !metav1.IsControlledBy(sandbox, claim) {
				err := fmt.Errorf("sandbox %q is not controlled by claim %q. Expected ownership to be updated.", sandbox.Name, claim.Name)
				logger.Error(err, "Sandbox controller mismatch")
				return nil, err
			}
			return sandbox, nil
		}
		if !k8errors.IsNotFound(err) {
			return nil, err
		}
		// NotFound, maybe it was deleted? Fall through.
	}

	// 2. Try to find by GUID label (most robust)
	sandboxList := &v1alpha1.SandboxList{}
	if err := r.List(ctx, sandboxList, client.InNamespace(claim.Namespace), client.MatchingLabels{
		extensionsv1alpha1.SandboxIDLabel: string(claim.UID),
	}); err != nil {
		return nil, err
	}
	if len(sandboxList.Items) > 0 {
		if len(sandboxList.Items) > 1 {
			logger.Info("Multiple sandboxes found for claim GUID, using the first one", "guid", claim.UID)
		}
		sandbox := &sandboxList.Items[0]
		logger.Info("Found existing sandbox by GUID label", "name", sandbox.Name)
		
		// Double check ownership
		if !metav1.IsControlledBy(sandbox, claim) {
			logger.Info("Sandbox found by label but not owned by claim, attempting to fix ownership", "sandbox", sandbox.Name)
			patch := client.MergeFrom(sandbox.DeepCopy())
			if err := controllerutil.SetControllerReference(claim, sandbox, r.Scheme); err != nil {
				return nil, fmt.Errorf("failed to restore controller reference: %w", err)
			}
			if err := r.Patch(ctx, sandbox, patch); err != nil {
				return nil, fmt.Errorf("failed to patch sandbox (restoring ownership): %w", err)
			}
		}
		return sandbox, nil
	}

	// 3. Try to get sandbox with claim name (fallback for old or explicitly named sandboxes)
	sandbox := &v1alpha1.Sandbox{}
	err := r.Get(ctx, types.NamespacedName{Namespace: claim.Namespace, Name: claim.Name}, sandbox)
	if err == nil {
		logger.Info("Found existing sandbox with claim name", "name", sandbox.Name)
		if !metav1.IsControlledBy(sandbox, claim) {
			err := fmt.Errorf("sandbox %q is not controlled by claim %q. Please use a different claim name or delete the sandbox manually", sandbox.Name, claim.Name)
			logger.Error(err, "Sandbox controller mismatch")
			return nil, err
		}
		return sandbox, nil
	}
	if !k8errors.IsNotFound(err) {
		return nil, err
	}

	// 4. Try to ADOPT from pool
	adoptedSandbox, err := r.tryAdoptSandboxFromPool(ctx, claim, template)
	if err != nil {
		return nil, err
	}
	if adoptedSandbox != nil {
		return adoptedSandbox, nil
	}

	// 5. Create NEW sandbox
	return r.createSandbox(ctx, claim, template)
}

func (r *SandboxClaimReconciler) getTemplate(ctx context.Context, claim *extensionsv1alpha1.SandboxClaim) (*extensionsv1alpha1.SandboxTemplate, error) {
	template := &extensionsv1alpha1.SandboxTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: claim.Namespace,
			Name:      claim.Spec.TemplateRef.Name,
		},
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(template), template); err != nil {
		if !k8errors.IsNotFound(err) {
			err = fmt.Errorf("failed to get sandbox template %q: %w", claim.Spec.TemplateRef.Name, err)
		}
		return nil, err
	}

	return template, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SandboxClaimReconciler) SetupWithManager(mgr ctrl.Manager, concurrentWorkers int) error {
	r.MaxConcurrentReconciles = concurrentWorkers


	return ctrl.NewControllerManagedBy(mgr).
		For(&extensionsv1alpha1.SandboxClaim{}).
		Owns(&v1alpha1.Sandbox{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: concurrentWorkers}).
		Complete(r)
}


// reconcileNetworkPolicy ensures a NetworkPolicy exists for the claimed Sandbox.
func (r *SandboxClaimReconciler) reconcileNetworkPolicy(ctx context.Context, claim *extensionsv1alpha1.SandboxClaim, template *extensionsv1alpha1.SandboxTemplate) error {
	logger := log.FromContext(ctx)

	// 1. Cleanup Check: If missing, delete existing policy
	if template == nil || template.Spec.NetworkPolicy == nil {
		existingNP := &networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      claim.Name + "-network-policy",
				Namespace: claim.Namespace,
			},
		}
		if err := r.Delete(ctx, existingNP); err != nil {
			if !k8errors.IsNotFound(err) {
				logger.Error(err, "Failed to clean up disabled NetworkPolicy")
				return err
			}
		} else {
			logger.Info("Deleted disabled NetworkPolicy", "name", existingNP.Name)
		}
		return nil
	}

	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      claim.Name + "-network-policy",
			Namespace: claim.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, np, func() error {
		np.Spec.PodSelector = metav1.LabelSelector{
			MatchLabels: map[string]string{
				extensionsv1alpha1.SandboxIDLabel: string(claim.UID),
			},
		}
		np.Spec.PolicyTypes = []networkingv1.PolicyType{
			networkingv1.PolicyTypeIngress,
			networkingv1.PolicyTypeEgress,
		}

		templateNP := template.Spec.NetworkPolicy

		if len(templateNP.Ingress) > 0 {
			np.Spec.Ingress = templateNP.Ingress
		}

		if len(templateNP.Egress) > 0 {
			np.Spec.Egress = templateNP.Egress
		}

		return controllerutil.SetControllerReference(claim, np, r.Scheme)
	})

	if err != nil {
		logger.Error(err, "Failed to create or update NetworkPolicy for claim")
		return err
	}

	logger.Info("Successfully reconciled NetworkPolicy for claim", "NetworkPolicy.Name", np.Name)
	return nil
}

// recordCreationLatencyMetric detects and records transitions to Ready state.
func (r *SandboxClaimReconciler) recordCreationLatencyMetric(ctx context.Context, claim *extensionsv1alpha1.SandboxClaim, originalClaimStatus extensionsv1alpha1.SandboxClaimStatus, sandbox *v1alpha1.Sandbox) {
	newStatus := &claim.Status
	newReady := meta.FindStatusCondition(newStatus.Conditions, string(v1alpha1.SandboxConditionReady))
	if newReady == nil || newReady.Status != metav1.ConditionTrue {
		return
	}

	// Do not record creation metric if we have already seen the ready state.
	oldReady := meta.FindStatusCondition(originalClaimStatus.Conditions, string(v1alpha1.SandboxConditionReady))
	if oldReady != nil && oldReady.Status == metav1.ConditionTrue {
		return
	}

	// Record Prometheus Metric for SandboxClaim creation-to-ready latency
	key := claim.Namespace + "/" + claim.Name
	if _, loaded := r.readyClaims.LoadOrStore(key, true); !loaded {
		latencySeconds := time.Since(claim.CreationTimestamp.Time).Seconds()
		asmetrics.SandboxClaimReadyLatency.WithLabelValues(claim.Namespace).Observe(latencySeconds)

		// Record SandbClaimReadyMS in milliseconds to logs
		log := log.FromContext(ctx)
		log.Info("SandbClaimReadyMS", "namespace", claim.Namespace, "name", claim.Name, "latency_ms", time.Since(claim.CreationTimestamp.Time).Milliseconds())
	}

	launchType := asmetrics.LaunchTypeCold
	// This is unlikely to happen; here for completeness only.
	if sandbox == nil {
		launchType = asmetrics.LaunchTypeUnknown
	} else if sandbox.Annotations[sandboxcontrollers.SandboxPodNameAnnotation] != "" {
		// Existence of the SandboxPodNameAnnotation implies the pod was adopted from a warm pool.
		launchType = asmetrics.LaunchTypeWarm
	}

	// SandboxClaim doesn't react to TemplateRef updates currently, so we don't need to handle the
	// startup latency when the TemplateRef is updated.
	asmetrics.RecordClaimStartupLatency(claim.CreationTimestamp.Time, launchType, claim.Spec.TemplateRef.Name)
}

// isSandboxExpired checks the Sandbox status condition set by the Core Controller
func isSandboxExpired(sandbox *v1alpha1.Sandbox) bool {
	return hasExpiredCondition(sandbox.Status.Conditions)
}

// hasExpiredCondition Helper to check if conditions list contains the expired reason
func hasExpiredCondition(conditions []metav1.Condition) bool {
	for _, cond := range conditions {
		if cond.Type == string(v1alpha1.SandboxConditionReady) {
			if cond.Reason == extensionsv1alpha1.ClaimExpiredReason || cond.Reason == v1alpha1.SandboxReasonExpired {
				return true
			}
		}
	}
	return false
}
