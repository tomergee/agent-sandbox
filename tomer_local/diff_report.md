diff --git a/controllers/sandbox_controller.go b/controllers/sandbox_controller.go
index 26d040b..8c71be8 100644
--- a/controllers/sandbox_controller.go
+++ b/controllers/sandbox_controller.go
@@ -261,8 +261,27 @@ func (r *SandboxReconciler) updateStatus(ctx context.Context, oldStatus *sandbox
 		return nil
 	}
 
-	if err := r.Status().Update(ctx, sandbox); err != nil {
-		log.Error(err, "Failed to update sandbox status")
+	// Fetch the latest version of the sandbox to avoid "object has been modified" errors.
+	latestSandbox := &sandboxv1alpha1.Sandbox{}
+	if err := r.Get(ctx, types.NamespacedName{Name: sandbox.Name, Namespace: sandbox.Namespace}, latestSandbox); err != nil {
+		log.Error(err, "Failed to get latest sandbox before status update")
+		return err
+	}
+
+	// Create base object for diffing from latest sandbox, but with old status.
+	// This ensures we only patch status changes and avoid metadata conflicts.
+	oldSandbox := latestSandbox.DeepCopy()
+	oldSandbox.Status = *oldStatus
+
+	// Set desired status on latest sandbox
+	latestSandbox.Status = sandbox.Status
+
+	// Diff latest against old (base)
+	patch := client.MergeFrom(oldSandbox)
+
+	// Apply patch to the LATEST sandbox object.
+	if err := r.Status().Patch(ctx, latestSandbox, patch); err != nil {
+		log.Error(err, "Failed to patch sandbox status")
 		return err
 	}
 
@@ -270,16 +289,17 @@ func (r *SandboxReconciler) updateStatus(ctx context.Context, oldStatus *sandbox
 	return nil
 }
 
+// GetNumericHash generates a raw FNV-1a hash value.
+func GetNumericHash(input string) uint32 {
+	h := fnv.New32a()
+	h.Write([]byte(input))
+	return h.Sum32()
+}
+
 // NameHash generates an FNV-1a hash from a string and returns
 // it as a fixed-length hexadecimal string.
 func NameHash(objectName string) string {
-	h := fnv.New32a()
-	h.Write([]byte(objectName))
-	hashValue := h.Sum32()
-
-	// Convert the uint32 to a hexadecimal string.
-	// This results in an 8-character string (e.g., "a5b3c2d1").
-	return fmt.Sprintf("%08x", hashValue)
+	return fmt.Sprintf("%08x", GetNumericHash(objectName))
 }
 
 func (r *SandboxReconciler) reconcileService(ctx context.Context, sandbox *sandboxv1alpha1.Sandbox, nameHash string) (*corev1.Service, error) {
@@ -422,20 +442,30 @@ func (r *SandboxReconciler) reconcilePod(ctx context.Context, sandbox *sandboxv1
 			})
 		}
 
+		changed := false
 		if pod.Labels == nil {
 			pod.Labels = make(map[string]string)
 		}
-		pod.Labels[sandboxLabel] = nameHash
+		if pod.Labels[sandboxLabel] != nameHash {
+			pod.Labels[sandboxLabel] = nameHash
+			changed = true
+		}
 
 		// Set controller reference if the pod is not controlled by anything.
 		if controllerRef := metav1.GetControllerOf(pod); controllerRef == nil {
 			if err := ctrl.SetControllerReference(sandbox, pod, r.Scheme); err != nil {
 				return nil, fmt.Errorf("SetControllerReference for Pod failed: %w", err)
 			}
+			changed = true
 		}
 
-		if err := r.Update(ctx, pod); err != nil {
-			return nil, fmt.Errorf("failed to update pod: %w", err)
+		if changed {
+			log.Info("Updating Pod labels/owner", "Pod.Namespace", pod.Namespace, "Pod.Name", pod.Name)
+			if err := r.Update(ctx, pod); err != nil {
+				return nil, fmt.Errorf("failed to update pod: %w", err)
+			}
+		} else {
+			log.Info("Pod is already up to date, skipping update", "Pod.Namespace", pod.Namespace, "Pod.Name", pod.Name)
 		}
 
 		// TODO - Do we enfore (change) spec if a pod exists ?
diff --git a/extensions/controllers/sandboxclaim_controller.go b/extensions/controllers/sandboxclaim_controller.go
index 2f96c99..a3958c5 100644
--- a/extensions/controllers/sandboxclaim_controller.go
+++ b/extensions/controllers/sandboxclaim_controller.go
@@ -20,6 +20,7 @@ import (
 	"fmt"
 	"reflect"
 	"sort"
+	"sync"
 	"time"
 
 	corev1 "k8s.io/api/core/v1"
@@ -27,10 +28,10 @@ import (
 	k8errors "k8s.io/apimachinery/pkg/api/errors"
 	"k8s.io/apimachinery/pkg/api/meta"
 	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
+	"k8s.io/apimachinery/pkg/types"
 	"k8s.io/apimachinery/pkg/labels"
 	"k8s.io/apimachinery/pkg/runtime"
 	"k8s.io/client-go/tools/record"
-	"k8s.io/kubectl/pkg/util/podutils"
 	ctrl "sigs.k8s.io/controller-runtime"
 	"sigs.k8s.io/controller-runtime/pkg/client"
 	"sigs.k8s.io/controller-runtime/pkg/controller"
@@ -48,15 +49,18 @@ const (
 	sandboxLabel = "agents.x-k8s.io/sandbox-name-hash"
 )
 
+
 // ErrTemplateNotFound is a sentinel error indicating a SandboxTemplate was not found.
 var ErrTemplateNotFound = errors.New("SandboxTemplate not found")
 
 // SandboxClaimReconciler reconciles a SandboxClaim object
 type SandboxClaimReconciler struct {
 	client.Client
-	Scheme   *runtime.Scheme
-	Recorder record.EventRecorder
-	Tracer   asmetrics.Instrumenter
+	Scheme                  *runtime.Scheme
+	Recorder                record.EventRecorder
+	Tracer                  asmetrics.Instrumenter
+	MaxConcurrentReconciles int
+	readyClaims             sync.Map
 }
 
 //+kubebuilder:rbac:groups=extensions.agents.x-k8s.io,resources=sandboxclaims,verbs=get;list;watch;create;update;patch;delete
@@ -75,6 +79,8 @@ func (r *SandboxClaimReconciler) Reconcile(ctx context.Context, req ctrl.Request
 	claim := &extensionsv1alpha1.SandboxClaim{}
 	if err := r.Get(ctx, req.NamespacedName, claim); err != nil {
 		if k8errors.IsNotFound(err) {
+			key := req.Namespace + "/" + req.Name
+			r.readyClaims.Delete(key)
 			return ctrl.Result{}, nil
 		}
 		return ctrl.Result{}, fmt.Errorf("failed to get sandbox claim %q: %w", req.NamespacedName, err)
@@ -144,12 +150,12 @@ func (r *SandboxClaimReconciler) Reconcile(ctx context.Context, req ctrl.Request
 		}
 	}
 
+	r.recordCreationLatencyMetric(ctx, claim, *originalClaimStatus, sandbox)
+
 	if updateErr := r.updateStatus(ctx, originalClaimStatus, claim); updateErr != nil {
 		return ctrl.Result{}, errors.Join(reconcileErr, updateErr)
 	}
 
-	r.recordCreationLatencyMetric(claim, originalClaimStatus, sandbox)
-
 	// Determine Result
 	var result ctrl.Result
 	if !claimExpired && timeLeft > 0 {
@@ -251,8 +257,11 @@ func (r *SandboxClaimReconciler) updateStatus(ctx context.Context, oldStatus *ex
 		return nil
 	}
 
-	if err := r.Status().Update(ctx, claim); err != nil {
-		log.Error(err, "Failed to update sandboxclaim status")
+	oldClaim := claim.DeepCopy()
+	oldClaim.Status = *oldStatus
+	patch := client.MergeFrom(oldClaim)
+	if err := r.Status().Patch(ctx, claim, patch); err != nil {
+		log.Error(err, "Failed to patch sandboxclaim status")
 		return err
 	}
 
@@ -338,84 +347,118 @@ func (r *SandboxClaimReconciler) computeAndSetStatus(claim *extensionsv1alpha1.S
 	}
 }
 
-// tryAdoptPodFromPool attempts to find and adopt a pod from the warm pool
-func (r *SandboxClaimReconciler) tryAdoptPodFromPool(ctx context.Context, claim *extensionsv1alpha1.SandboxClaim, sandbox *v1alpha1.Sandbox) (*corev1.Pod, error) {
+// tryAdoptSandboxFromPool attempts to adopt an existing Sandbox from the WarmPool.
+func (r *SandboxClaimReconciler) tryAdoptSandboxFromPool(ctx context.Context, claim *extensionsv1alpha1.SandboxClaim, template *extensionsv1alpha1.SandboxTemplate) (*v1alpha1.Sandbox, error) {
 	log := log.FromContext(ctx)
 
-	// List all pods with the podTemplateHashLabel matching the hash
-	podList := &corev1.PodList{}
-	labelSelector := labels.SelectorFromSet(labels.Set{
-		sandboxTemplateRefHash: sandboxcontrollers.NameHash(claim.Spec.TemplateRef.Name),
-	})
+	sandboxList := &v1alpha1.SandboxList{}
 
-	if err := r.List(ctx, podList, &client.ListOptions{
-		LabelSelector: labelSelector,
-		Namespace:     claim.Namespace,
-	}); err != nil {
-		log.Error(err, "Failed to list pods from warm pool")
+	// Filter by TemplateRef
+	err := r.List(ctx, sandboxList, &client.ListOptions{
+		Namespace: claim.Namespace,
+		LabelSelector: labels.SelectorFromSet(labels.Set{
+			sandboxTemplateRefHash: sandboxcontrollers.NameHash(claim.Spec.TemplateRef.Name),
+		}),
+	})
+	if err != nil || len(sandboxList.Items) == 0 {
 		return nil, err
 	}
 
-	// Filter pods and create a slice of pointers for sorting
-	candidates := make([]*corev1.Pod, 0, len(podList.Items))
-	for i := range podList.Items {
-		pod := &podList.Items[i]
+	// Sort Sandboxes by creation timestamp (oldest first) to ensure FIFO
+	sort.Slice(sandboxList.Items, func(i, j int) bool {
+		return sandboxList.Items[i].CreationTimestamp.Time.Before(sandboxList.Items[j].CreationTimestamp.Time)
+	})
 
-		// Skip pods that are being deleted
-		if !pod.DeletionTimestamp.IsZero() {
+	// Determine the search range for collision avoidance.
+	n := len(sandboxList.Items)
+	workerCount := r.MaxConcurrentReconciles
+	if workerCount <= 0 {
+		workerCount = 1
+	}
+	searchWindow := n
+	if workerCount < searchWindow {
+		searchWindow = workerCount
+	}
+	// Compute a starting index deterministic to this specific Claim UID.
+	hashValue := sandboxcontrollers.GetNumericHash(string(claim.UID))
+	startIndex := int(hashValue % uint32(searchWindow))
+
+	// Iterate through the entire list starting from the hashed offset.
+	for i := range n {
+		currIndex := (startIndex + i) % n
+		staleSandbox := sandboxList.Items[currIndex]
+		if !staleSandbox.DeletionTimestamp.IsZero() {
 			continue
 		}
 
-		// Skip pods that already have a different controller
-		if controllerRef := metav1.GetControllerOf(pod); controllerRef != nil && controllerRef.Kind != "SandboxWarmPool" {
-			log.Info("Ignoring pod with different controller, but this shouldn't happen because this pod shouldn't have template ref label",
-				"pod", pod.Name,
-				"controller", controllerRef.Name,
-				"controllerKind", controllerRef.Kind)
+		// Fetch fresh copy to avoid double adoption
+		sandbox := &v1alpha1.Sandbox{}
+		if err := r.Get(ctx, types.NamespacedName{Namespace: staleSandbox.Namespace, Name: staleSandbox.Name}, sandbox); err != nil {
+			log.Error(err, "Failed to fetch fresh sandbox for adoption", "sandbox", staleSandbox.Name)
 			continue
 		}
 
-		candidates = append(candidates, pod)
-	}
-
-	if len(candidates) == 0 {
-		log.Info("No available pods in warm pool (all pods are being deleted, owned by other controllers, or pool is empty)")
-		return nil, nil
-	}
-
-	// Sort pods using podutils.ByLogging to select the best available pod.
-	sort.Sort(podutils.ByLogging(candidates))
-
-	// Get the first available pod
-	pod := candidates[0]
-	log.Info("Adopting pod from warm pool", "pod", pod.Name)
+		// Check ownership
+		controllerRef := metav1.GetControllerOf(sandbox)
+		if controllerRef == nil || controllerRef.Kind != "SandboxWarmPool" {
+			continue // Skip sandboxes not managed by our warm pool
+		}
+		// Check if sandbox is ready
+		isReady := false
+		for _, condition := range sandbox.Status.Conditions {
+			if condition.Type == string(v1alpha1.SandboxConditionReady) && condition.Status == metav1.ConditionTrue {
+				isReady = true
+				break
+			}
+		}
+		if !isReady {
+			continue
+		}
 
-	// Remove the pool labels
-	delete(pod.Labels, poolLabel)
-	delete(pod.Labels, sandboxTemplateRefHash)
+		// Check if it has a Claim UID label (should not)
+		if _, exists := sandbox.Labels[extensionsv1alpha1.SandboxIDLabel]; exists {
+			continue
+		}
 
-	// Remove existing owner references (from SandboxWarmPool)
-	pod.OwnerReferences = nil
+		log.Info("Attempting to adopt sandbox from warm pool", "sandbox", sandbox.Name)
 
-	nameHash := sandboxcontrollers.NameHash(claim.Name)
-	if pod.Labels == nil {
-		pod.Labels = make(map[string]string)
-	}
+		// Remove old controller ref
+		var newOwnerRefs []metav1.OwnerReference
+		for _, ref := range sandbox.OwnerReferences {
+			if ref.Controller != nil && *ref.Controller && ref.UID == controllerRef.UID {
+				continue // Skip the warmpool ref
+			}
+			newOwnerRefs = append(newOwnerRefs, ref)
+		}
+		sandbox.OwnerReferences = newOwnerRefs
 
-	pod.Labels[sandboxLabel] = nameHash
+		// Add new controller ref (SandboxClaim)
+		if err := controllerutil.SetControllerReference(claim, sandbox, r.Scheme); err != nil {
+			log.Error(err, "Failed to set controller reference for adopted sandbox", "sandbox", sandbox.Name)
+			continue
+		}
 
-	// Label required by NetworkPolicy
-	// We add the new label with the Claim UID for unique targeting.
-	pod.Labels[extensionsv1alpha1.SandboxIDLabel] = string(claim.UID)
+		// Set labels
+		if sandbox.Labels == nil {
+			sandbox.Labels = make(map[string]string)
+		}
+		sandbox.Labels[extensionsv1alpha1.SandboxIDLabel] = string(claim.UID)
+		
+		// Remove the pool label so the warm pool controller stops reconciling this sandbox
+		delete(sandbox.Labels, poolLabel)
+		delete(sandbox.Labels, sandboxTemplateRefHash)
+
+		// Use Update instead of Patch for Conflict detection
+		if err := r.Update(ctx, sandbox); err != nil {
+			log.Error(err, "Failed to update sandbox for adoption", "sandbox", sandbox.Name)
+			continue
+		}
 
-	// Update the pod
-	if err := r.Update(ctx, pod); err != nil {
-		log.Error(err, "Failed to update adopted pod")
-		return nil, err
+		log.Info("Successfully adopted sandbox from warm pool", "sandbox", sandbox.Name)
+		return sandbox, nil
 	}
 
-	log.Info("Successfully adopted pod from warm pool", "pod", pod.Name, "sandbox", sandbox.Name)
-	return pod, nil
+	return nil, nil // No suitable sandbox found or failed to adopt
 }
 
 func (r *SandboxClaimReconciler) createSandbox(ctx context.Context, claim *extensionsv1alpha1.SandboxClaim, template *extensionsv1alpha1.SandboxTemplate) (*v1alpha1.Sandbox, error) {
@@ -458,27 +501,18 @@ func (r *SandboxClaimReconciler) createSandbox(ctx context.Context, claim *exten
 	}
 	sandbox.Spec.PodTemplate.ObjectMeta.Labels[extensionsv1alpha1.SandboxIDLabel] = string(claim.UID)
 
+	// Set the SandboxIDLabel on the Sandbox itself as well
+	if sandbox.Labels == nil {
+		sandbox.Labels = make(map[string]string)
+	}
+	sandbox.Labels[extensionsv1alpha1.SandboxIDLabel] = string(claim.UID)
+
 	if err := controllerutil.SetControllerReference(claim, sandbox, r.Scheme); err != nil {
 		err = fmt.Errorf("failed to set controller reference for sandbox: %w", err)
 		logger.Error(err, "Error creating sandbox for claim", "claimName", claim.Name)
 		return nil, err
 	}
 
-	// Before creating the sandbox, try to adopt a pod from the warm pool
-	adoptedPod, adoptErr := r.tryAdoptPodFromPool(ctx, claim, sandbox)
-	if adoptErr != nil {
-		logger.Error(adoptErr, "Failed to adopt pod from warm pool")
-		return nil, adoptErr
-	}
-
-	if adoptedPod != nil {
-		logger.Info("Adopted pod from warm pool for sandbox", "pod", adoptedPod.Name, "sandbox", sandbox.Name)
-		if sandbox.Annotations == nil {
-			sandbox.Annotations = make(map[string]string)
-		}
-		sandbox.Annotations[sandboxcontrollers.SandboxPodNameAnnotation] = adoptedPod.Name
-	}
-
 	if err := r.Create(ctx, sandbox); err != nil {
 		err = fmt.Errorf("sandbox create error: %w", err)
 		logger.Error(err, "Error creating sandbox for claim", "claimName", claim.Name)
@@ -496,22 +530,59 @@ func (r *SandboxClaimReconciler) createSandbox(ctx context.Context, claim *exten
 
 func (r *SandboxClaimReconciler) getOrCreateSandbox(ctx context.Context, claim *extensionsv1alpha1.SandboxClaim, template *extensionsv1alpha1.SandboxTemplate) (*v1alpha1.Sandbox, error) {
 	logger := log.FromContext(ctx)
-	sandbox := &v1alpha1.Sandbox{
-		ObjectMeta: metav1.ObjectMeta{
-			Namespace: claim.Namespace,
-			Name:      claim.Name,
-		},
-	}
-	if err := r.Get(ctx, client.ObjectKeyFromObject(sandbox), sandbox); err != nil {
-		sandbox = nil
+
+	// 1. Check if we already have a sandbox name in status
+	if claim.Status.SandboxStatus.Name != "" {
+		sandbox := &v1alpha1.Sandbox{}
+		err := r.Get(ctx, types.NamespacedName{Namespace: claim.Namespace, Name: claim.Status.SandboxStatus.Name}, sandbox)
+		if err == nil {
+			logger.Info("Found existing sandbox from status", "name", sandbox.Name)
+			if !metav1.IsControlledBy(sandbox, claim) {
+				err := fmt.Errorf("sandbox %q is not controlled by claim %q. Expected ownership to be updated.", sandbox.Name, claim.Name)
+				logger.Error(err, "Sandbox controller mismatch")
+				return nil, err
+			}
+			return sandbox, nil
+		}
 		if !k8errors.IsNotFound(err) {
-			err = fmt.Errorf("failed to get sandbox %q: %w", claim.Name, err)
 			return nil, err
 		}
+		// NotFound, maybe it was deleted? Fall through.
 	}
 
-	if sandbox != nil {
-		logger.Info("sandbox already exists, skipping update", "name", sandbox.Name)
+	// 2. Try to find by GUID label (most robust)
+	sandboxList := &v1alpha1.SandboxList{}
+	if err := r.List(ctx, sandboxList, client.InNamespace(claim.Namespace), client.MatchingLabels{
+		extensionsv1alpha1.SandboxIDLabel: string(claim.UID),
+	}); err != nil {
+		return nil, err
+	}
+	if len(sandboxList.Items) > 0 {
+		if len(sandboxList.Items) > 1 {
+			logger.Info("Multiple sandboxes found for claim GUID, using the first one", "guid", claim.UID)
+		}
+		sandbox := &sandboxList.Items[0]
+		logger.Info("Found existing sandbox by GUID label", "name", sandbox.Name)
+		
+		// Double check ownership
+		if !metav1.IsControlledBy(sandbox, claim) {
+			logger.Info("Sandbox found by label but not owned by claim, attempting to fix ownership", "sandbox", sandbox.Name)
+			patch := client.MergeFrom(sandbox.DeepCopy())
+			if err := controllerutil.SetControllerReference(claim, sandbox, r.Scheme); err != nil {
+				return nil, fmt.Errorf("failed to restore controller reference: %w", err)
+			}
+			if err := r.Patch(ctx, sandbox, patch); err != nil {
+				return nil, fmt.Errorf("failed to patch sandbox (restoring ownership): %w", err)
+			}
+		}
+		return sandbox, nil
+	}
+
+	// 3. Try to get sandbox with claim name (fallback for old or explicitly named sandboxes)
+	sandbox := &v1alpha1.Sandbox{}
+	err := r.Get(ctx, types.NamespacedName{Namespace: claim.Namespace, Name: claim.Name}, sandbox)
+	if err == nil {
+		logger.Info("Found existing sandbox with claim name", "name", sandbox.Name)
 		if !metav1.IsControlledBy(sandbox, claim) {
 			err := fmt.Errorf("sandbox %q is not controlled by claim %q. Please use a different claim name or delete the sandbox manually", sandbox.Name, claim.Name)
 			logger.Error(err, "Sandbox controller mismatch")
@@ -519,7 +590,20 @@ func (r *SandboxClaimReconciler) getOrCreateSandbox(ctx context.Context, claim *
 		}
 		return sandbox, nil
 	}
+	if !k8errors.IsNotFound(err) {
+		return nil, err
+	}
 
+	// 4. Try to ADOPT from pool
+	adoptedSandbox, err := r.tryAdoptSandboxFromPool(ctx, claim, template)
+	if err != nil {
+		return nil, err
+	}
+	if adoptedSandbox != nil {
+		return adoptedSandbox, nil
+	}
+
+	// 5. Create NEW sandbox
 	return r.createSandbox(ctx, claim, template)
 }
 
@@ -542,6 +626,9 @@ func (r *SandboxClaimReconciler) getTemplate(ctx context.Context, claim *extensi
 
 // SetupWithManager sets up the controller with the Manager.
 func (r *SandboxClaimReconciler) SetupWithManager(mgr ctrl.Manager, concurrentWorkers int) error {
+	r.MaxConcurrentReconciles = concurrentWorkers
+
+
 	return ctrl.NewControllerManagedBy(mgr).
 		For(&extensionsv1alpha1.SandboxClaim{}).
 		Owns(&v1alpha1.Sandbox{}).
@@ -549,6 +636,7 @@ func (r *SandboxClaimReconciler) SetupWithManager(mgr ctrl.Manager, concurrentWo
 		Complete(r)
 }
 
+
 // reconcileNetworkPolicy ensures a NetworkPolicy exists for the claimed Sandbox.
 func (r *SandboxClaimReconciler) reconcileNetworkPolicy(ctx context.Context, claim *extensionsv1alpha1.SandboxClaim, template *extensionsv1alpha1.SandboxTemplate) error {
 	logger := log.FromContext(ctx)
@@ -613,11 +701,21 @@ func (r *SandboxClaimReconciler) reconcileNetworkPolicy(ctx context.Context, cla
 }
 
 // recordCreationLatencyMetric detects and records transitions to Ready state.
-func (r *SandboxClaimReconciler) recordCreationLatencyMetric(
-	claim *extensionsv1alpha1.SandboxClaim,
-	oldStatus *extensionsv1alpha1.SandboxClaimStatus,
-	sandbox *v1alpha1.Sandbox,
-) {
+func (r *SandboxClaimReconciler) recordCreationLatencyMetric(ctx context.Context, claim *extensionsv1alpha1.SandboxClaim, originalClaimStatus extensionsv1alpha1.SandboxClaimStatus, sandbox *v1alpha1.Sandbox) {
+	// Record Prometheus Metric for SandboxClaim creation-to-ready latency
+	wasReady := meta.IsStatusConditionTrue(originalClaimStatus.Conditions, string(v1alpha1.SandboxConditionReady))
+	isReady := meta.IsStatusConditionTrue(claim.Status.Conditions, string(v1alpha1.SandboxConditionReady))
+	if !wasReady && isReady {
+		key := claim.Namespace + "/" + claim.Name
+		if _, loaded := r.readyClaims.LoadOrStore(key, true); !loaded {
+			latencySeconds := time.Since(claim.CreationTimestamp.Time).Seconds()
+			asmetrics.SandboxClaimReadyLatency.WithLabelValues(claim.Namespace).Observe(latencySeconds)
+
+			// Record SandbClaimReadyMS in milliseconds to logs
+			log := log.FromContext(ctx)
+			log.Info("SandbClaimReadyMS", "namespace", claim.Namespace, "name", claim.Name, "latency_ms", time.Since(claim.CreationTimestamp.Time).Milliseconds())
+		}
+	}
 
 	newStatus := &claim.Status
 	newReady := meta.FindStatusCondition(newStatus.Conditions, string(v1alpha1.SandboxConditionReady))
@@ -626,7 +724,7 @@ func (r *SandboxClaimReconciler) recordCreationLatencyMetric(
 	}
 
 	// Do not record creation metric if we have already seen the ready state.
-	oldReady := meta.FindStatusCondition(oldStatus.Conditions, string(v1alpha1.SandboxConditionReady))
+	oldReady := meta.FindStatusCondition(originalClaimStatus.Conditions, string(v1alpha1.SandboxConditionReady))
 	if oldReady != nil && oldReady.Status == metav1.ConditionTrue {
 		return
 	}
@@ -643,6 +741,9 @@ func (r *SandboxClaimReconciler) recordCreationLatencyMetric(
 	// SandboxClaim doesn't react to TemplateRef updates currently, so we don't need to handle the
 	// startup latency when the TemplateRef is updated.
 	asmetrics.RecordClaimStartupLatency(claim.CreationTimestamp.Time, launchType, claim.Spec.TemplateRef.Name)
+
+	// Record SandboxClaimReadyLatency (in seconds) for backward compatibility/comparison.
+	asmetrics.SandboxClaimReadyLatency.WithLabelValues(claim.Namespace).Observe(time.Since(claim.CreationTimestamp.Time).Seconds())
 }
 
 // isSandboxExpired checks the Sandbox status condition set by the Core Controller
diff --git a/extensions/controllers/sandboxclaim_controller_test.go b/extensions/controllers/sandboxclaim_controller_test.go
index 16503e6..c15554d 100644
--- a/extensions/controllers/sandboxclaim_controller_test.go
+++ b/extensions/controllers/sandboxclaim_controller_test.go
@@ -328,7 +328,8 @@ func TestSandboxClaimReconcile(t *testing.T) {
 			}
 
 			allObjects := append(tc.existingObjects, claimToUse)
-			client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(allObjects...).WithStatusSubresource(claimToUse).Build()
+			client := fake.NewClientBuilder().WithScheme(scheme).
+				WithObjects(allObjects...).WithStatusSubresource(claimToUse).Build()
 
 			reconciler := &SandboxClaimReconciler{
 				Client:   client,
@@ -642,12 +643,12 @@ func TestSandboxClaimPodAdoption(t *testing.T) {
 	warmPoolUID := types.UID("warmpool-uid-123")
 	poolNameHash := sandboxcontrollers.NameHash("test-pool")
 
-	createWarmPoolPod := func(name string, creationTime metav1.Time, ready bool) *corev1.Pod {
-		conditionStatus := corev1.ConditionFalse
+	createWarmPoolSandbox := func(name string, creationTime metav1.Time, ready bool) *sandboxv1alpha1.Sandbox {
+		conditionStatus := metav1.ConditionFalse
 		if ready {
-			conditionStatus = corev1.ConditionTrue
+			conditionStatus = metav1.ConditionTrue
 		}
-		return &corev1.Pod{
+		return &sandboxv1alpha1.Sandbox{
 			ObjectMeta: metav1.ObjectMeta{
 				Name:              name,
 				Namespace:         "default",
@@ -666,28 +667,19 @@ func TestSandboxClaimPodAdoption(t *testing.T) {
 					},
 				},
 			},
-			Status: corev1.PodStatus{
-				Phase: corev1.PodRunning,
-				Conditions: []corev1.PodCondition{
+			Status: sandboxv1alpha1.SandboxStatus{
+				Conditions: []metav1.Condition{
 					{
-						Type:   corev1.PodReady,
+						Type:   string(sandboxv1alpha1.SandboxConditionReady),
 						Status: conditionStatus,
 					},
 				},
 			},
-			Spec: corev1.PodSpec{
-				Containers: []corev1.Container{
-					{
-						Name:  "test-container",
-						Image: "test-image",
-					},
-				},
-			},
 		}
 	}
 
-	createPodWithDifferentController := func(name string) *corev1.Pod {
-		return &corev1.Pod{
+	createSandboxWithDifferentController := func(name string) *sandboxv1alpha1.Sandbox {
+		return &sandboxv1alpha1.Sandbox{
 			ObjectMeta: metav1.ObjectMeta{
 				Name:      name,
 				Namespace: "default",
@@ -704,102 +696,94 @@ func TestSandboxClaimPodAdoption(t *testing.T) {
 					},
 				},
 			},
-			Spec: corev1.PodSpec{
-				Containers: []corev1.Container{
-					{
-						Name:  "test-container",
-						Image: "test-image",
-					},
-				},
-			},
 		}
 	}
 
-	createDeletingPod := func(name string) *corev1.Pod {
-		pod := createWarmPoolPod(name, metav1.Now(), true)
+	createDeletingSandbox := func(name string) *sandboxv1alpha1.Sandbox {
+		sandbox := createWarmPoolSandbox(name, metav1.Now(), true)
 		now := metav1.Now()
-		pod.DeletionTimestamp = &now
+		sandbox.DeletionTimestamp = &now
 		// Add a finalizer so the fake client accepts the object with deletionTimestamp
-		pod.Finalizers = []string{"test-finalizer"}
-		return pod
+		sandbox.Finalizers = []string{"test-finalizer"}
+		return sandbox
 	}
 
 	testCases := []struct {
-		name                string
-		existingObjects     []client.Object
-		expectPodAdoption   bool
-		expectedAdoptedPod  string // name of the pod that should be adopted
-		expectSandboxCreate bool
+		name                   string
+		existingObjects        []client.Object
+		expectSandboxAdoption  bool
+		expectedAdoptedSandbox string
+		expectSandboxCreate    bool
 	}{
 		{
-			name: "adopts oldest pod from warm pool",
+			name: "adopts oldest sandbox from warm pool",
 			existingObjects: []client.Object{
 				template,
 				claim,
-				createWarmPoolPod("pool-pod-1", metav1.Time{Time: metav1.Now().Add(-3600)}, true), // oldest
-				createWarmPoolPod("pool-pod-2", metav1.Time{Time: metav1.Now().Add(-1800)}, true),
-				createWarmPoolPod("pool-pod-3", metav1.Now(), true),
+				createWarmPoolSandbox("pool-sandbox-1", metav1.Time{Time: metav1.Now().Add(-3600)}, true), // oldest
+				createWarmPoolSandbox("pool-sandbox-2", metav1.Time{Time: metav1.Now().Add(-1800)}, true),
+				createWarmPoolSandbox("pool-sandbox-3", metav1.Now(), true),
 			},
-			expectPodAdoption:   true,
-			expectedAdoptedPod:  "pool-pod-1",
-			expectSandboxCreate: true,
+			expectSandboxAdoption:  true,
+			expectedAdoptedSandbox: "pool-sandbox-1",
+			expectSandboxCreate:    false,
 		},
 		{
-			name: "creates sandbox without adoption when no warm pool pods exist",
+			name: "creates sandbox without adoption when no warm pool sandboxes exist",
 			existingObjects: []client.Object{
 				template,
 				claim,
 			},
-			expectPodAdoption:   false,
-			expectSandboxCreate: true,
+			expectSandboxAdoption:  false,
+			expectSandboxCreate:    true,
 		},
 		{
-			name: "skips pods with different controller",
+			name: "skips sandboxes with different controller",
 			existingObjects: []client.Object{
 				template,
 				claim,
-				createPodWithDifferentController("other-pod-1"),
-				createWarmPoolPod("pool-pod-1", metav1.Now(), true),
+				createSandboxWithDifferentController("other-sandbox-1"),
+				createWarmPoolSandbox("pool-sandbox-1", metav1.Now(), true),
 			},
-			expectPodAdoption:   true,
-			expectedAdoptedPod:  "pool-pod-1",
-			expectSandboxCreate: true,
+			expectSandboxAdoption:  true,
+			expectedAdoptedSandbox: "pool-sandbox-1",
+			expectSandboxCreate:    false,
 		},
 		{
-			name: "skips pods being deleted",
+			name: "skips sandboxes being deleted",
 			existingObjects: []client.Object{
 				template,
 				claim,
-				createDeletingPod("deleting-pod"),
-				createWarmPoolPod("pool-pod-1", metav1.Now(), true),
+				createDeletingSandbox("deleting-sandbox"),
+				createWarmPoolSandbox("pool-sandbox-1", metav1.Now(), true),
 			},
-			expectPodAdoption:   true,
-			expectedAdoptedPod:  "pool-pod-1",
-			expectSandboxCreate: true,
+			expectSandboxAdoption:  true,
+			expectedAdoptedSandbox: "pool-sandbox-1",
+			expectSandboxCreate:    false,
 		},
 		{
-			name: "no adoption when only ineligible pods exist",
+			name: "no adoption when only ineligible sandboxes exist",
 			existingObjects: []client.Object{
 				template,
 				claim,
-				createPodWithDifferentController("other-pod-1"),
-				createDeletingPod("deleting-pod"),
+				createSandboxWithDifferentController("other-sandbox-1"),
+				createDeletingSandbox("deleting-sandbox"),
 			},
-			expectPodAdoption:   false,
-			expectSandboxCreate: true,
+			expectSandboxAdoption:  false,
+			expectSandboxCreate:    true,
 		},
 		{
-			name: "prioritizes ready pods",
+			name: "prioritizes ready sandboxes",
 			existingObjects: []client.Object{
 				template,
 				claim,
-				createWarmPoolPod("not-ready", metav1.Time{Time: metav1.Now().Add(-2 * time.Hour)}, false),
-				createWarmPoolPod("middle-ready", metav1.Time{Time: metav1.Now().Add(-1 * time.Hour)}, true),
-				createWarmPoolPod("young-ready", metav1.Now(), true),
+				createWarmPoolSandbox("not-ready", metav1.Time{Time: metav1.Now().Add(-2 * time.Hour)}, false),
+				createWarmPoolSandbox("middle-ready", metav1.Time{Time: metav1.Now().Add(-1 * time.Hour)}, true),
+				createWarmPoolSandbox("young-ready", metav1.Now(), true),
 			},
-			expectPodAdoption:   true,
-			expectedAdoptedPod:  "middle-ready",
-			expectSandboxCreate: true,
+			expectSandboxAdoption:  true,
+			expectedAdoptedSandbox: "middle-ready",
+			expectSandboxCreate:    false,
 		},
 	}
 
@@ -842,48 +826,66 @@ func TestSandboxClaimPodAdoption(t *testing.T) {
 				t.Fatalf("expected sandbox not to be created but it exists")
 			}
 
-			if tc.expectPodAdoption {
-				// Verify the adopted pod has correct labels and owner reference
-				var adoptedPod corev1.Pod
+			// Verify claim status
+			var updatedClaim extensionsv1alpha1.SandboxClaim
+			err = client.Get(ctx, types.NamespacedName{Name: "test-claim", Namespace: "default"}, &updatedClaim)
+			if err != nil {
+				t.Fatalf("failed to get updated claim: %v", err)
+			}
+
+			if tc.expectSandboxAdoption {
+				if updatedClaim.Status.SandboxStatus.Name != tc.expectedAdoptedSandbox {
+					t.Errorf("expected claim status to have sandbox name %q, but got %q", tc.expectedAdoptedSandbox, updatedClaim.Status.SandboxStatus.Name)
+				}
+
+				// Verify the adopted sandbox has correct labels and owner reference
+				var adoptedSandbox sandboxv1alpha1.Sandbox
 				err = client.Get(ctx, types.NamespacedName{
-					Name:      tc.expectedAdoptedPod,
+					Name:      tc.expectedAdoptedSandbox,
 					Namespace: "default",
-				}, &adoptedPod)
+				}, &adoptedSandbox)
 				if err != nil {
-					t.Fatalf("failed to get adopted pod: %v", err)
+					t.Fatalf("failed to get adopted sandbox: %v", err)
 				}
 
 				// 1. Verify pool labels were removed
-				if _, exists := adoptedPod.Labels[poolLabel]; exists {
-					t.Errorf("expected pool label to be removed from adopted pod")
+				if _, exists := adoptedSandbox.Labels[poolLabel]; exists {
+					t.Errorf("expected pool label to be removed from adopted sandbox")
 				}
-				if _, exists := adoptedPod.Labels[sandboxTemplateRefHash]; exists {
-					t.Errorf("expected sandbox template ref label to be removed from adopted pod")
+				if _, exists := adoptedSandbox.Labels[sandboxTemplateRefHash]; exists {
+					t.Errorf("expected sandbox template ref label to be removed from adopted sandbox")
 				}
 
 				// 2. Verify Security Label (UID) was added
 				expectedUID := string(types.UID("claim-uid")) // MATCHES CLAIM UID
-				if val, exists := adoptedPod.Labels[extensionsv1alpha1.SandboxIDLabel]; !exists || val != expectedUID {
-					t.Errorf("expected pod to have security label %q with value %q, but got %q", extensionsv1alpha1.SandboxIDLabel, expectedUID, val)
+				if val, exists := adoptedSandbox.Labels[extensionsv1alpha1.SandboxIDLabel]; !exists || val != expectedUID {
+					t.Errorf("expected sandbox to have security label %q with value %q, but got %q", extensionsv1alpha1.SandboxIDLabel, expectedUID, val)
 				}
 
 				// 3. Verify Legacy Hash Label (Required by Base Controller) was added
-				expectedLegacyHash := sandboxcontrollers.NameHash("test-claim")
-				if val, exists := adoptedPod.Labels[sandboxLabel]; !exists || val != expectedLegacyHash {
-					t.Errorf("expected pod to have legacy label %q with value %q, but got %q", sandboxLabel, expectedLegacyHash, val)
-				}
+
 
 				// 4. Verify OwnerReference is nil
-				if len(adoptedPod.OwnerReferences) != 0 {
-					t.Errorf("expected adopted pod owner references to be cleared, got %v", adoptedPod.OwnerReferences)
+				if len(adoptedSandbox.OwnerReferences) != 1 {
+					t.Errorf("expected adopted sandbox to have 1 owner reference, got %v", adoptedSandbox.OwnerReferences)
+				} else {
+					owner := adoptedSandbox.OwnerReferences[0]
+					if owner.Kind != "SandboxClaim" || owner.Name != "test-claim" {
+						t.Errorf("expected adopted sandbox to be owned by SandboxClaim test-claim, got %v", owner)
+					}
 				}
-
 			} else if tc.expectSandboxCreate {
-				// Verify no pod name annotation when no adoption occurred
-				if sandbox.Annotations != nil {
-					if _, exists := sandbox.Annotations[sandboxcontrollers.SandboxPodNameAnnotation]; exists {
-						t.Errorf("expected no pod name annotation but found one")
-					}
+				if updatedClaim.Status.SandboxStatus.Name == "" {
+					t.Errorf("expected claim status to have a sandbox name, but it was empty")
+				}
+				// Verify the created sandbox has correct name (it should be equal to updatedClaim.Status.SandboxStatus.Name)
+				var createdSandbox sandboxv1alpha1.Sandbox
+				err = client.Get(ctx, types.NamespacedName{
+					Name:      updatedClaim.Status.SandboxStatus.Name,
+					Namespace: "default",
+				}, &createdSandbox)
+				if err != nil {
+					t.Fatalf("failed to get created sandbox: %v", err)
 				}
 			}
 		})
@@ -958,7 +960,11 @@ func TestRecordCreationLatencyMetric(t *testing.T) {
 			asmetrics.ClaimStartupLatency.Reset()
 			r := &SandboxClaimReconciler{}
 
-			r.recordCreationLatencyMetric(tc.claim, tc.oldStatus, tc.sandbox)
+			var oldStatus extensionsv1alpha1.SandboxClaimStatus
+			if tc.oldStatus != nil {
+				oldStatus = *tc.oldStatus
+			}
+			r.recordCreationLatencyMetric(context.Background(), tc.claim, oldStatus, tc.sandbox)
 
 			// Verify the metric was observed in the Prometheus registry
 			count := testutil.CollectAndCount(asmetrics.ClaimStartupLatency)
diff --git a/extensions/controllers/sandboxwarmpool_controller.go b/extensions/controllers/sandboxwarmpool_controller.go
index bd6e88b..779f4da 100644
--- a/extensions/controllers/sandboxwarmpool_controller.go
+++ b/extensions/controllers/sandboxwarmpool_controller.go
@@ -20,7 +20,6 @@ import (
 	"fmt"
 	"sort"
 
-	corev1 "k8s.io/api/core/v1"
 	"k8s.io/apimachinery/pkg/api/equality"
 	k8serrors "k8s.io/apimachinery/pkg/api/errors"
 	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
@@ -31,6 +30,7 @@ import (
 	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
 	"sigs.k8s.io/controller-runtime/pkg/log"
 
+	v1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
 	sandboxcontrollers "sigs.k8s.io/agent-sandbox/controllers"
 	extensionsv1alpha1 "sigs.k8s.io/agent-sandbox/extensions/api/v1alpha1"
 )
@@ -47,6 +47,8 @@ type SandboxWarmPoolReconciler struct {
 
 //+kubebuilder:rbac:groups=extensions.agents.x-k8s.io,resources=sandboxwarmpools,verbs=get;list;watch;create;update;patch;delete
 //+kubebuilder:rbac:groups=extensions.agents.x-k8s.io,resources=sandboxwarmpools/status,verbs=get;update;patch
+//+kubebuilder:rbac:groups=agents.x-k8s.io,resources=sandboxes,verbs=get;list;watch;create;update;patch;delete
+//+kubebuilder:rbac:groups=agents.x-k8s.io,resources=sandboxes/status,verbs=get;update;patch
 //+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete
 
 // Reconcile implements the reconciliation loop for SandboxWarmPool
@@ -94,56 +96,56 @@ func (r *SandboxWarmPoolReconciler) reconcilePool(ctx context.Context, warmPool
 	// Compute hash of the warm pool name for the pool label
 	poolNameHash := sandboxcontrollers.NameHash(warmPool.Name)
 
-	// List all pods with the pool label matching the warm pool name hash
-	podList := &corev1.PodList{}
+	// List all sandboxes with the pool label matching the warm pool name hash
+	sandboxList := &v1alpha1.SandboxList{}
 	labelSelector := labels.SelectorFromSet(labels.Set{
 		poolLabel: poolNameHash,
 	})
 
-	if err := r.List(ctx, podList, &client.ListOptions{
+	if err := r.List(ctx, sandboxList, &client.ListOptions{
 		LabelSelector: labelSelector,
 		Namespace:     warmPool.Namespace,
 	}); err != nil {
-		log.Error(err, "Failed to list pods")
+		log.Error(err, "Failed to list sandboxes")
 		return err
 	}
 
-	// Filter pods by ownership and adopt orphans
-	var activePods []corev1.Pod
+	// Filter sandboxes by ownership and adopt orphans
+	var activeSandboxes []v1alpha1.Sandbox
 	var allErrors error
 
-	for _, pod := range podList.Items {
-		// Skip pods that are being deleted
-		if !pod.DeletionTimestamp.IsZero() {
+	for _, sandbox := range sandboxList.Items {
+		// Skip sandboxes that are being deleted
+		if !sandbox.DeletionTimestamp.IsZero() {
 			continue
 		}
 
 		// Get the controller owner reference
-		controllerRef := metav1.GetControllerOf(&pod)
+		controllerRef := metav1.GetControllerOf(&sandbox)
 
 		if controllerRef == nil {
-			// Pod has no controller - adopt it
-			log.Info("Adopting orphaned pod", "pod", pod.Name)
-			if err := r.adoptPod(ctx, warmPool, &pod); err != nil {
-				log.Error(err, "Failed to adopt pod", "pod", pod.Name)
+			// Sandbox has no controller - adopt it
+			log.Info("Adopting orphaned sandbox", "sandbox", sandbox.Name)
+			if err := r.adoptSandbox(ctx, warmPool, &sandbox); err != nil {
+				log.Error(err, "Failed to adopt sandbox", "sandbox", sandbox.Name)
 				allErrors = errors.Join(allErrors, err)
 				continue
 			}
-			activePods = append(activePods, pod)
+			activeSandboxes = append(activeSandboxes, sandbox)
 		} else if controllerRef.UID == warmPool.UID {
-			// Pod belongs to this warmpool - include it
-			activePods = append(activePods, pod)
+			// Sandbox belongs to this warmpool - include it
+			activeSandboxes = append(activeSandboxes, sandbox)
 		} else {
-			// Pod has a different controller - ignore it
-			log.Info("Ignoring pod with different controller",
-				"pod", pod.Name,
+			// Sandbox has a different controller - ignore it
+			log.Info("Ignoring sandbox with different controller",
+				"sandbox", sandbox.Name,
 				"controller", controllerRef.Name,
 				"controllerKind", controllerRef.Kind)
 		}
 	}
 
 	desiredReplicas := warmPool.Spec.Replicas
-	currentReplicas := int32(len(activePods))
+	currentReplicas := int32(len(activeSandboxes))
 
 	log.Info("Pool status",
 		"desired", desiredReplicas,
@@ -156,9 +158,9 @@ func (r *SandboxWarmPoolReconciler) reconcilePool(ctx context.Context, warmPool
 
 	// Calculate ready replicas
 	readyReplicas := int32(0)
-	for _, pod := range activePods {
-		for _, cond := range pod.Status.Conditions {
-			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
+	for _, sandbox := range activeSandboxes {
+		for _, cond := range sandbox.Status.Conditions {
+			if cond.Type == string(v1alpha1.SandboxConditionReady) && cond.Status == metav1.ConditionTrue {
 				readyReplicas++
 				break
 			}
@@ -166,35 +168,35 @@ func (r *SandboxWarmPoolReconciler) reconcilePool(ctx context.Context, warmPool
 	}
 	warmPool.Status.ReadyReplicas = readyReplicas
 
-	// Create new pods if we need more
+	// Create new sandboxes if we need more
 	if currentReplicas < desiredReplicas {
-		podsToCreate := desiredReplicas - currentReplicas
-		log.Info("Creating new pods", "count", podsToCreate)
+		sandboxesToCreate := desiredReplicas - currentReplicas
+		log.Info("Creating new sandboxes", "count", sandboxesToCreate)
 
-		for i := int32(0); i < podsToCreate; i++ {
-			if err := r.createPoolPod(ctx, warmPool, poolNameHash); err != nil {
-				log.Error(err, "Failed to create pod")
+		for i := int32(0); i < sandboxesToCreate; i++ {
+			if err := r.createPoolSandbox(ctx, warmPool, poolNameHash); err != nil {
+				log.Error(err, "Failed to create sandbox")
 				allErrors = errors.Join(allErrors, err)
 			}
 		}
 	}
 
-	// Delete excess pods if we have too many
+	// Delete excess sandboxes if we have too many
 	if currentReplicas > desiredReplicas {
-		podsToDelete := currentReplicas - desiredReplicas
-		log.Info("Deleting excess pods", "count", podsToDelete)
+		sandboxesToDelete := currentReplicas - desiredReplicas
+		log.Info("Deleting excess sandboxes", "count", sandboxesToDelete)
 
-		// Sort active pods by creation timestamp (newest first)
-		sort.Slice(activePods, func(i, j int) bool {
-			return activePods[i].CreationTimestamp.After(activePods[j].CreationTimestamp.Time)
+		// Sort active sandboxes by creation timestamp (newest first)
+		sort.Slice(activeSandboxes, func(i, j int) bool {
+			return activeSandboxes[i].CreationTimestamp.After(activeSandboxes[j].CreationTimestamp.Time)
 		})
 
-		// Delete the first N active pods from the sorted list (newest first)
-		for i := int32(0); i < podsToDelete && i < int32(len(activePods)); i++ {
-			pod := &activePods[i]
+		// Delete the first N active sandboxes from the sorted list (newest first)
+		for i := int32(0); i < sandboxesToDelete && i < int32(len(activeSandboxes)); i++ {
+			sandbox := &activeSandboxes[i]
 
-			if err := r.Delete(ctx, pod); err != nil {
-				log.Error(err, "Failed to delete pod", "pod", pod.Name)
+			if err := r.Delete(ctx, sandbox); err != nil {
+				log.Error(err, "Failed to delete sandbox", "sandbox", sandbox.Name)
 				allErrors = errors.Join(allErrors, err)
 			}
 		}
@@ -204,22 +206,17 @@ func (r *SandboxWarmPoolReconciler) reconcilePool(ctx context.Context, warmPool
 }
 
 // adoptPod sets this warmpool as the owner of an orphaned pod
-func (r *SandboxWarmPoolReconciler) adoptPod(ctx context.Context, warmPool *extensionsv1alpha1.SandboxWarmPool, pod *corev1.Pod) error {
-	if err := controllerutil.SetControllerReference(warmPool, pod, r.Scheme()); err != nil {
+func (r *SandboxWarmPoolReconciler) adoptSandbox(ctx context.Context, warmPool *extensionsv1alpha1.SandboxWarmPool, sandbox *v1alpha1.Sandbox) error {
+	if err := controllerutil.SetControllerReference(warmPool, sandbox, r.Scheme()); err != nil {
 		return err
 	}
-	return r.Update(ctx, pod)
+	return r.Update(ctx, sandbox)
 }
 
 // createPoolPod creates a new pod for the warm pool
-func (r *SandboxWarmPoolReconciler) createPoolPod(ctx context.Context, warmPool *extensionsv1alpha1.SandboxWarmPool, poolNameHash string) error {
+func (r *SandboxWarmPoolReconciler) createPoolSandbox(ctx context.Context, warmPool *extensionsv1alpha1.SandboxWarmPool, poolNameHash string) error {
 	log := log.FromContext(ctx)
 
-	// Create labels for the pod
-	podLabels := make(map[string]string)
-	podLabels[poolLabel] = poolNameHash
-	podLabels[sandboxTemplateRefHash] = sandboxcontrollers.NameHash(warmPool.Spec.TemplateRef.Name)
-
 	// Try getting template
 	var template *extensionsv1alpha1.SandboxTemplate
 	var err error
@@ -228,45 +225,46 @@ func (r *SandboxWarmPoolReconciler) createPoolPod(ctx context.Context, warmPool
 		return err
 	}
 
-	for k, v := range template.Spec.PodTemplate.ObjectMeta.Labels {
-		podLabels[k] = v
-	}
-
-	// Create annotations for the pod
-	podAnnotations := make(map[string]string)
-	for k, v := range template.Spec.PodTemplate.ObjectMeta.Annotations {
-		podAnnotations[k] = v
-	}
+	// Create labels for the sandbox
+	sandboxLabels := make(map[string]string)
+	sandboxLabels[poolLabel] = poolNameHash
+	sandboxLabels[sandboxTemplateRefHash] = sandboxcontrollers.NameHash(warmPool.Spec.TemplateRef.Name)
 
-	// Create the pod
-	pod := &corev1.Pod{
+	// Create the sandbox
+	sandbox := &v1alpha1.Sandbox{
 		ObjectMeta: metav1.ObjectMeta{
 			GenerateName: fmt.Sprintf("%s-", warmPool.Name),
 			Namespace:    warmPool.Namespace,
-			Labels:       podLabels,
-			Annotations:  podAnnotations,
+			Labels:       sandboxLabels,
+		},
+		Spec: v1alpha1.SandboxSpec{
+			PodTemplate: template.Spec.PodTemplate,
 		},
-		Spec: template.Spec.PodTemplate.Spec,
 	}
 
-	// pod.Labels[podNameLabel] = sandboxcontrollers.NameHash(pod.Name)
+	// Propagate pool label to pod template so a dynamic pod can be found
+	labels := make(map[string]string)
+	for k, v := range template.Spec.PodTemplate.ObjectMeta.Labels {
+		labels[k] = v
+	}
+	labels[poolLabel] = poolNameHash
+	sandbox.Spec.PodTemplate.ObjectMeta.Labels = labels
 
-	// Set controller reference so the Pod is owned by the SandboxWarmPool
-	if err := ctrl.SetControllerReference(warmPool, pod, r.Scheme()); err != nil {
-		return fmt.Errorf("SetControllerReference for Pod failed: %w", err)
+	// Set controller reference so the Sandbox is owned by the SandboxWarmPool
+	if err := ctrl.SetControllerReference(warmPool, sandbox, r.Scheme()); err != nil {
+		return fmt.Errorf("SetControllerReference for Sandbox failed: %w", err)
 	}
 
-	// Create the Pod
-	if err := r.Create(ctx, pod); err != nil {
-		log.Error(err, "Failed to create pod")
+	// Create the Sandbox
+	if err := r.Create(ctx, sandbox); err != nil {
+		log.Error(err, "Failed to create sandbox")
 		return err
 	}
 
-	log.Info("Created new pool pod", "pod", pod.Name, "poolName", warmPool.Name, "poolNameHash", poolNameHash)
+	log.Info("Created new pool sandbox", "sandbox", sandbox.Name, "poolName", warmPool.Name, "poolNameHash", poolNameHash)
 	return nil
 }
 
-// updateStatus updates the status of the SandboxWarmPool if it has changed
 func (r *SandboxWarmPoolReconciler) updateStatus(ctx context.Context, oldStatus *extensionsv1alpha1.SandboxWarmPoolStatus, warmPool *extensionsv1alpha1.SandboxWarmPool) error {
 	log := log.FromContext(ctx)
 
@@ -275,8 +273,27 @@ func (r *SandboxWarmPoolReconciler) updateStatus(ctx context.Context, oldStatus
 		return nil
 	}
 
-	if err := r.Status().Update(ctx, warmPool); err != nil {
-		log.Error(err, "Failed to update SandboxWarmPool status")
+	// Fetch the latest version of the warmpool to avoid "object has been modified" errors.
+	latestWarmPool := &extensionsv1alpha1.SandboxWarmPool{}
+	if err := r.Get(ctx, client.ObjectKeyFromObject(warmPool), latestWarmPool); err != nil {
+		log.Error(err, "Failed to get latest SandboxWarmPool before status update")
+		return err
+	}
+
+	// Create base object for diffing from latest warmpool, but with old status.
+	// This ensures we only patch status changes and avoid metadata conflicts.
+	oldWarmPool := latestWarmPool.DeepCopy()
+	oldWarmPool.Status = *oldStatus
+
+	// Set desired status on latest warmpool
+	latestWarmPool.Status = warmPool.Status
+
+	// Diff latest against old (base)
+	patch := client.MergeFrom(oldWarmPool)
+
+	// Apply patch to the LATEST warmpool object.
+	if err := r.Status().Patch(ctx, latestWarmPool, patch); err != nil {
+		log.Error(err, "Failed to patch SandboxWarmPool status")
 		return err
 	}
 
@@ -305,7 +322,7 @@ func (r *SandboxWarmPoolReconciler) getTemplate(ctx context.Context, warmPool *e
 func (r *SandboxWarmPoolReconciler) SetupWithManager(mgr ctrl.Manager, concurrentWorkers int) error {
 	return ctrl.NewControllerManagedBy(mgr).
 		For(&extensionsv1alpha1.SandboxWarmPool{}).
-		Owns(&corev1.Pod{}).
+		Owns(&v1alpha1.Sandbox{}).
 		WithOptions(controller.Options{MaxConcurrentReconciles: concurrentWorkers}).
 		Complete(r)
 }
diff --git a/extensions/controllers/sandboxwarmpool_controller_test.go b/extensions/controllers/sandboxwarmpool_controller_test.go
index e324f7b..f51b969 100644
--- a/extensions/controllers/sandboxwarmpool_controller_test.go
+++ b/extensions/controllers/sandboxwarmpool_controller_test.go
@@ -41,27 +41,31 @@ func newTestScheme() *runtime.Scheme {
 	return scheme
 }
 
-func createPod(name, namespace, poolNameHash string) *corev1.Pod {
-	return &corev1.Pod{
+func createSandbox(name, namespace, poolNameHash string) *sandboxv1alpha1.Sandbox {
+	return &sandboxv1alpha1.Sandbox{
 		ObjectMeta: metav1.ObjectMeta{
 			Name:      name,
 			Namespace: namespace,
 			Labels:    map[string]string{poolLabel: poolNameHash},
 		},
-		Spec: corev1.PodSpec{
-			Containers: []corev1.Container{
-				{
-					Name:  "test-container",
-					Image: "test-image",
+		Spec: sandboxv1alpha1.SandboxSpec{
+			PodTemplate: sandboxv1alpha1.PodTemplate{
+				Spec: corev1.PodSpec{
+					Containers: []corev1.Container{
+						{
+							Name:  "test-container",
+							Image: "test-image",
+						},
+					},
 				},
 			},
 		},
 	}
 }
 
-func createPoolPod(poolName, namespace, poolNameHash, suffix string) *corev1.Pod {
+func createPoolSandbox(poolName, namespace, poolNameHash, suffix string) *sandboxv1alpha1.Sandbox {
 	name := poolName + suffix
-	return createPod(name, namespace, poolNameHash)
+	return createSandbox(name, namespace, poolNameHash)
 }
 
 func createTemplate(name, namespace string) *extensionsv1alpha1.SandboxTemplate {
@@ -116,26 +120,26 @@ func TestReconcilePool(t *testing.T) {
 		expectedReplicas int32
 	}{
 		{
-			name:             "creates pods when pool is empty",
+			name:             "creates sandboxes when pool is empty",
 			initialObjs:      []runtime.Object{template},
 			expectedReplicas: replicas,
 		},
 		{
-			name: "creates additional pods when under-provisioned",
+			name: "creates additional sandboxes when under-provisioned",
 			initialObjs: []runtime.Object{
 				template,
-				createPoolPod(poolName, poolNamespace, poolNameHash, "abc123"),
+				createPoolSandbox(poolName, poolNamespace, poolNameHash, "abc123"),
 			},
 			expectedReplicas: replicas,
 		},
 		{
-			name: "deletes excess pods when over-provisioned",
+			name: "deletes excess sandboxes when over-provisioned",
 			initialObjs: []runtime.Object{
 				template,
-				createPoolPod(poolName, poolNamespace, poolNameHash, "abc123"),
-				createPoolPod(poolName, poolNamespace, poolNameHash, "def456"),
-				createPoolPod(poolName, poolNamespace, poolNameHash, "ghi789"),
-				createPoolPod(poolName, poolNamespace, poolNameHash, "jkl012"),
+				createPoolSandbox(poolName, poolNamespace, poolNameHash, "abc123"),
+				createPoolSandbox(poolName, poolNamespace, poolNameHash, "def456"),
+				createPoolSandbox(poolName, poolNamespace, poolNameHash, "ghi789"),
+				createPoolSandbox(poolName, poolNamespace, poolNameHash, "jkl012"),
 			},
 			expectedReplicas: replicas,
 		},
@@ -143,9 +147,9 @@ func TestReconcilePool(t *testing.T) {
 			name: "maintains correct replica count",
 			initialObjs: []runtime.Object{
 				template,
-				createPoolPod(poolName, poolNamespace, poolNameHash, "abc123"),
-				createPoolPod(poolName, poolNamespace, poolNameHash, "def456"),
-				createPoolPod(poolName, poolNamespace, poolNameHash, "ghi789"),
+				createPoolSandbox(poolName, poolNamespace, poolNameHash, "abc123"),
+				createPoolSandbox(poolName, poolNamespace, poolNameHash, "def456"),
+				createPoolSandbox(poolName, poolNamespace, poolNameHash, "ghi789"),
 			},
 			expectedReplicas: replicas,
 		},
@@ -170,14 +174,14 @@ func TestReconcilePool(t *testing.T) {
 			require.NoError(t, err)
 
 			// Verify final state
-			list := &corev1.PodList{}
+			list := &sandboxv1alpha1.SandboxList{}
 			err = r.List(ctx, list, &client.ListOptions{Namespace: poolNamespace})
 			require.NoError(t, err)
 
-			// Count pods with correct pool label
+			// Count sandboxes with correct pool label
 			count := int32(0)
-			for _, pod := range list.Items {
-				if pod.Labels[poolLabel] == poolNameHash {
+			for _, sb := range list.Items {
+				if sb.Labels[poolLabel] == poolNameHash {
 					count++
 				}
 			}
@@ -214,10 +218,10 @@ func TestReconcilePoolControllerRef(t *testing.T) {
 	// Compute the pool name hash
 	poolNameHash := sandboxcontrollers.NameHash(poolName)
 
-	createPodWithOwner := func(name string, ownerUID string) *corev1.Pod {
-		pod := createPoolPod(poolName, poolNamespace, poolNameHash, name)
+	createSandboxWithOwner := func(name string, ownerUID string) *sandboxv1alpha1.Sandbox {
+		sb := createPoolSandbox(poolName, poolNamespace, poolNameHash, name)
 		if ownerUID != "" {
-			pod.OwnerReferences = []metav1.OwnerReference{
+			sb.OwnerReferences = []metav1.OwnerReference{
 				{
 					APIVersion: "extensions.agents.x-k8s.io/v1alpha1",
 					Kind:       "SandboxWarmPool",
@@ -227,12 +231,12 @@ func TestReconcilePoolControllerRef(t *testing.T) {
 				},
 			}
 		}
-		return pod
+		return sb
 	}
 
-	createPodWithDifferentController := func(name string) *corev1.Pod {
-		pod := createPoolPod(poolName, poolNamespace, poolNameHash, name)
-		pod.OwnerReferences = []metav1.OwnerReference{
+	createSandboxWithDifferentController := func(name string) *sandboxv1alpha1.Sandbox {
+		sb := createPoolSandbox(poolName, poolNamespace, poolNameHash, name)
+		sb.OwnerReferences = []metav1.OwnerReference{
 			{
 				APIVersion: "apps/v1",
 				Kind:       "ReplicaSet",
@@ -241,75 +245,75 @@ func TestReconcilePoolControllerRef(t *testing.T) {
 				Controller: boolPtr(true),
 			},
 		}
-		return pod
+		return sb
 	}
 
 	testCases := []struct {
 		name             string
 		initialObjs      []runtime.Object
 		expectedReplicas int32
-		expectedAdopted  int // number of pods that should be adopted
+		expectedAdopted  int // number of sandboxes that should be adopted
 	}{
 		{
-			name: "adopts orphaned pods with no controller reference",
+			name: "adopts orphaned sandboxes with no controller reference",
 			initialObjs: []runtime.Object{
 				template,
-				createPodWithOwner("abc123", ""), // No owner reference
-				createPodWithOwner("def456", ""), // No owner reference
+				createSandboxWithOwner("abc123", ""), // No owner reference
+				createSandboxWithOwner("def456", ""), // No owner reference
 			},
 			expectedReplicas: replicas,
 			expectedAdopted:  2,
 		},
 		{
-			name: "includes pods with correct controller reference",
+			name: "includes sandboxes with correct controller reference",
 			initialObjs: []runtime.Object{
 				template,
-				createPodWithOwner("abc123", "warmpool-uid-123"),
-				createPodWithOwner("def456", "warmpool-uid-123"),
+				createSandboxWithOwner("abc123", "warmpool-uid-123"),
+				createSandboxWithOwner("def456", "warmpool-uid-123"),
 			},
 			expectedReplicas: replicas,
 			expectedAdopted:  0,
 		},
 		{
-			name: "ignores pods with different controller reference",
+			name: "ignores sandboxes with different controller reference",
 			initialObjs: []runtime.Object{
 				template,
-				createPodWithDifferentController("abc123"),
-				createPodWithDifferentController("def456"),
+				createSandboxWithDifferentController("abc123"),
+				createSandboxWithDifferentController("def456"),
 			},
-			expectedReplicas: replicas, // Should create 2 new pods
+			expectedReplicas: replicas, // Should create 2 new sandboxes
 			expectedAdopted:  0,
 		},
 		{
-			name: "handles mix of owned, orphaned, and foreign pods",
+			name: "handles mix of owned, orphaned, and foreign sandboxes",
 			initialObjs: []runtime.Object{
 				template,
-				createPodWithOwner("abc123", "warmpool-uid-123"), // Owned
-				createPodWithOwner("def456", ""),                 // Orphaned - should adopt
-				createPodWithDifferentController("ghi789"),       // Foreign - should ignore
+				createSandboxWithOwner("abc123", "warmpool-uid-123"), // Owned
+				createSandboxWithOwner("def456", ""),                 // Orphaned - should adopt
+				createSandboxWithDifferentController("ghi789"),       // Foreign - should ignore
 			},
 			expectedReplicas: replicas,
 			expectedAdopted:  1,
 		},
 		{
-			name: "adopts orphan and creates additional pod when under-provisioned",
+			name: "adopts orphan and creates additional sandbox when under-provisioned",
 			initialObjs: []runtime.Object{
 				template,
-				createPodWithOwner("abc123", ""), // Orphaned - should adopt
+				createSandboxWithOwner("abc123", ""), // Orphaned - should adopt
 			},
 			expectedReplicas: replicas, // 1 adopted + 1 created
 			expectedAdopted:  1,
 		},
 		{
-			name: "deletes excess owned pods but ignores foreign pods",
+			name: "deletes excess owned sandboxes but ignores foreign sandboxes",
 			initialObjs: []runtime.Object{
 				template,
-				createPodWithOwner("abc123", "warmpool-uid-123"),
-				createPodWithOwner("def456", "warmpool-uid-123"),
-				createPodWithOwner("ghi789", "warmpool-uid-123"),
-				createPodWithDifferentController("jkl012"), // Should be ignored
+				createSandboxWithOwner("abc123", "warmpool-uid-123"),
+				createSandboxWithOwner("def456", "warmpool-uid-123"),
+				createSandboxWithOwner("ghi789", "warmpool-uid-123"),
+				createSandboxWithDifferentController("jkl012"), // Should be ignored
 			},
-			expectedReplicas: replicas, // Should delete 1 owned pod
+			expectedReplicas: replicas, // Should delete 1 owned sandbox
 			expectedAdopted:  0,
 		},
 	}
@@ -334,22 +338,22 @@ func TestReconcilePoolControllerRef(t *testing.T) {
 			require.NoError(t, err)
 
 			// Verify final state
-			list := &corev1.PodList{}
+			list := &sandboxv1alpha1.SandboxList{}
 			err = r.List(ctx, list, &client.ListOptions{Namespace: poolNamespace})
 			require.NoError(t, err)
 
-			// Count pods with correct pool label and owned by warmpool
+			// Count sandboxes with correct pool label and owned by warmpool
 			ownedCount := int32(0)
 			adoptedCount := 0
-			for _, pod := range list.Items {
-				if pod.Labels[poolLabel] == poolNameHash {
-					controllerRef := metav1.GetControllerOf(&pod)
+			for _, sb := range list.Items {
+				if sb.Labels[poolLabel] == poolNameHash {
+					controllerRef := metav1.GetControllerOf(&sb)
 					if controllerRef != nil && controllerRef.UID == warmPool.UID {
 						ownedCount++
 						// Check if this was originally an orphan (adopted)
 						for _, initialObj := range tc.initialObjs {
-							if initialPod, ok := initialObj.(*corev1.Pod); ok {
-								if initialPod.Name == pod.Name && len(initialPod.OwnerReferences) == 0 {
+							if initialSb, ok := initialObj.(*sandboxv1alpha1.Sandbox); ok {
+								if initialSb.Name == sb.Name && len(initialSb.OwnerReferences) == 0 {
 									adoptedCount++
 									break
 								}
@@ -359,7 +363,7 @@ func TestReconcilePoolControllerRef(t *testing.T) {
 				}
 			}
 
-			require.Equal(t, tc.expectedReplicas, ownedCount, "owned pod count mismatch")
+			require.Equal(t, tc.expectedReplicas, ownedCount, "owned sandbox count mismatch")
 			require.Equal(t, tc.expectedReplicas, warmPool.Status.Replicas, "status replicas mismatch")
 		})
 	}
@@ -443,30 +447,30 @@ func TestPoolLabelValueInIntegration(t *testing.T) {
 		require.NoError(t, err)
 
 		// List all pods
-		list := &corev1.PodList{}
+		list := &sandboxv1alpha1.SandboxList{}
 		err = r.List(ctx, list, &client.ListOptions{Namespace: poolNamespace})
 		require.NoError(t, err)
 		require.Len(t, list.Items, int(replicas))
 
-		// Verify each pod has the correct labels
-		for _, pod := range list.Items {
-			require.Equal(t, expectedPoolNameHash, pod.Labels[poolLabel],
-				"pod %s should have correct pool label (pool name hash)", pod.Name)
-			require.Equal(t, sandboxcontrollers.NameHash(templateName), pod.Labels[sandboxTemplateRefHash],
-				"pod %s should have correct sandbox template ref label", pod.Name)
+		// Verify each sandbox has the correct labels
+		for _, sb := range list.Items {
+			require.Equal(t, expectedPoolNameHash, sb.Labels[poolLabel],
+				"sandbox %s should have correct pool label (pool name hash)", sb.Name)
+			require.Equal(t, sandboxcontrollers.NameHash(templateName), sb.Labels[sandboxTemplateRefHash],
+				"sandbox %s should have correct sandbox template ref label", sb.Name)
 
-			// Verify labels from pod template
-			require.Equal(t, "2.0", pod.Labels["version"])
-			require.Equal(t, "from-podtemplate", pod.Labels["pod-label"])
+			// Verify labels from pod template are in Spec.PodTemplate
+			require.Equal(t, "2.0", sb.Spec.PodTemplate.ObjectMeta.Labels["version"])
+			require.Equal(t, "from-podtemplate", sb.Spec.PodTemplate.ObjectMeta.Labels["pod-label"])
 
-			// Verify sandbox template labels are not propagated
-			require.NotContains(t, pod.Labels, "app")
+			// Verify sandbox template labels are not propagated to Sandbox ObjectMeta
+			require.NotContains(t, sb.Labels, "app")
 
-			// Verify annotations from pod template
-			require.Equal(t, "from-podtemplate", pod.Annotations["pod-annotation"])
+			// Verify annotations from pod template are in Spec.PodTemplate
+			require.Equal(t, "from-podtemplate", sb.Spec.PodTemplate.ObjectMeta.Annotations["pod-annotation"])
 
-			// Verify sandbox template metadata annotations are not propagated
-			require.NotContains(t, pod.Annotations, "description")
+			// Verify sandbox template metadata annotations are not propagated to Sandbox ObjectMeta
+			require.NotContains(t, sb.Annotations, "description")
 		}
 	})
 }
@@ -496,59 +500,59 @@ func TestReconcilePoolReadyReplicas(t *testing.T) {
 	// Compute the pool name hash
 	poolNameHash := sandboxcontrollers.NameHash(poolName)
 
-	createPodWithReadyCondition := func(suffix string, ready corev1.ConditionStatus) *corev1.Pod {
-		pod := createPoolPod(poolName, poolNamespace, poolNameHash, suffix)
-		pod.Status.Conditions = []corev1.PodCondition{
+	createSandboxWithReadyCondition := func(suffix string, ready metav1.ConditionStatus) *sandboxv1alpha1.Sandbox {
+		sb := createPoolSandbox(poolName, poolNamespace, poolNameHash, suffix)
+		sb.Status.Conditions = []metav1.Condition{
 			{
-				Type:   corev1.PodReady,
+				Type:   string(sandboxv1alpha1.SandboxConditionReady),
 				Status: ready,
 			},
 		}
-		return pod
+		return sb
 	}
 
 	testCases := []struct {
 		name                  string
-		initialPods           []runtime.Object
+		initialSandboxes      []runtime.Object
 		expectedReadyReplicas int32
 	}{
 		{
-			name: "no pods ready",
-			initialPods: []runtime.Object{
+			name: "no sandboxes ready",
+			initialSandboxes: []runtime.Object{
 				template,
-				createPodWithReadyCondition("abc123", corev1.ConditionFalse),
-				createPodWithReadyCondition("def456", corev1.ConditionUnknown),
-				createPodWithReadyCondition("ghi789", corev1.ConditionFalse),
+				createSandboxWithReadyCondition("abc123", metav1.ConditionFalse),
+				createSandboxWithReadyCondition("def456", metav1.ConditionUnknown),
+				createSandboxWithReadyCondition("ghi789", metav1.ConditionFalse),
 			},
 			expectedReadyReplicas: 0,
 		},
 		{
-			name: "some pods ready",
-			initialPods: []runtime.Object{
+			name: "some sandboxes ready",
+			initialSandboxes: []runtime.Object{
 				template,
-				createPodWithReadyCondition("abc123", corev1.ConditionTrue),
-				createPodWithReadyCondition("def456", corev1.ConditionFalse),
-				createPodWithReadyCondition("ghi789", corev1.ConditionTrue),
+				createSandboxWithReadyCondition("abc123", metav1.ConditionTrue),
+				createSandboxWithReadyCondition("def456", metav1.ConditionFalse),
+				createSandboxWithReadyCondition("ghi789", metav1.ConditionTrue),
 			},
 			expectedReadyReplicas: 2,
 		},
 		{
-			name: "all pods ready",
-			initialPods: []runtime.Object{
+			name: "all sandboxes ready",
+			initialSandboxes: []runtime.Object{
 				template,
-				createPodWithReadyCondition("abc123", corev1.ConditionTrue),
-				createPodWithReadyCondition("def456", corev1.ConditionTrue),
-				createPodWithReadyCondition("ghi789", corev1.ConditionTrue),
+				createSandboxWithReadyCondition("abc123", metav1.ConditionTrue),
+				createSandboxWithReadyCondition("def456", metav1.ConditionTrue),
+				createSandboxWithReadyCondition("ghi789", metav1.ConditionTrue),
 			},
 			expectedReadyReplicas: 3,
 		},
 		{
-			name: "pods with no ready condition",
-			initialPods: []runtime.Object{
+			name: "sandboxes with no ready condition",
+			initialSandboxes: []runtime.Object{
 				template,
-				createPoolPod(poolName, poolNamespace, poolNameHash, "abc123"),
-				createPoolPod(poolName, poolNamespace, poolNameHash, "def456"),
-				createPodWithReadyCondition("ghi789", corev1.ConditionTrue),
+				createPoolSandbox(poolName, poolNamespace, poolNameHash, "abc123"),
+				createPoolSandbox(poolName, poolNamespace, poolNameHash, "def456"),
+				createSandboxWithReadyCondition("ghi789", metav1.ConditionTrue),
 			},
 			expectedReadyReplicas: 1,
 		},
@@ -559,7 +563,7 @@ func TestReconcilePoolReadyReplicas(t *testing.T) {
 			r := SandboxWarmPoolReconciler{
 				Client: fake.NewClientBuilder().
 					WithScheme(newTestScheme()).
-					WithRuntimeObjects(tc.initialPods...).
+					WithRuntimeObjects(tc.initialSandboxes...).
 					Build(),
 			}
 
diff --git a/internal/metrics/metrics.go b/internal/metrics/metrics.go
index 5124fe0..ffac185 100644
--- a/internal/metrics/metrics.go
+++ b/internal/metrics/metrics.go
@@ -42,11 +42,25 @@ var (
 		},
 		[]string{"launch_type", "sandbox_template"},
 	)
+
+	// SandboxClaimReadyLatency measures the time from SandboxClaim creation to SandboxClaim Ready state.
+	// Labels:
+	// - namespace: the namespace of the claim
+	SandboxClaimReadyLatency = prometheus.NewHistogramVec(
+		prometheus.HistogramOpts{
+			Name:    "sandbox_claim_ready_latency_seconds",
+			Help:    "Latency from claim creation to readiness in seconds",
+			Buckets: prometheus.DefBuckets, // Uses default buckets which are suitable for typical sub-second/seconds latencies.
+		},
+		[]string{"namespace"},
+	)
 )
 
+
 // Init registers custom metrics with the global controller-runtime registry.
 func init() {
 	metrics.Registry.MustRegister(ClaimStartupLatency)
+	metrics.Registry.MustRegister(SandboxClaimReadyLatency)
 }
 
 // RecordClaimStartupLatency records the duration since the provided start time.
diff --git a/k8s/controller.yaml b/k8s/controller.yaml
deleted file mode 100644
index ac1cd0f..0000000
--- a/k8s/controller.yaml
+++ /dev/null
@@ -1,81 +0,0 @@
----
-kind: Namespace
-apiVersion: v1
-metadata:
-  name: agent-sandbox-system
-
----
-
-kind: ServiceAccount
-apiVersion: v1
-metadata:
-  name: agent-sandbox-controller
-  namespace: agent-sandbox-system
-  labels:
-    app: agent-sandbox-controller
-
----
-
-apiVersion: rbac.authorization.k8s.io/v1
-kind: ClusterRoleBinding
-metadata:
-  name: agent-sandbox-controller
-subjects:
-- kind: ServiceAccount
-  name: agent-sandbox-controller
-  namespace: agent-sandbox-system
-roleRef:
-  kind: ClusterRole
-  name: agent-sandbox-controller
-  apiGroup: rbac.authorization.k8s.io
-
----
-
-kind: Service
-apiVersion: v1
-metadata:
-  name: agent-sandbox-controller
-  namespace: agent-sandbox-system
-  labels:
-    app: agent-sandbox-controller
-spec:
-  selector:
-    app: agent-sandbox-controller
-  ports:
-  - name: metrics
-    port: 8080
-    targetPort: metrics
-    protocol: TCP
-
----
-
-kind: Deployment
-apiVersion: apps/v1
-metadata:
-  name: agent-sandbox-controller
-  namespace: agent-sandbox-system
-  labels:
-    app: agent-sandbox-controller
-spec:
-  replicas: 1
-  selector:
-    matchLabels:
-      app: agent-sandbox-controller
-  template:
-    metadata:
-      labels:
-        app: agent-sandbox-controller
-    spec:
-      serviceAccountName: agent-sandbox-controller
-      containers:
-      - name: agent-sandbox-controller
-        image: ko://sigs.k8s.io/agent-sandbox/cmd/agent-sandbox-controller # placeholder value, replaced by deployment scripts
-        args:
-        - --leader-elect=true
-        ports:
-        - name: metrics
-          containerPort: 8080
-          protocol: TCP
-        - name: healthz
-          containerPort: 8081
-          protocol: TCP
diff --git a/k8s/extensions-rbac.generated.yaml b/k8s/extensions-rbac.generated.yaml
index 07cec36..8a79426 100644
--- a/k8s/extensions-rbac.generated.yaml
+++ b/k8s/extensions-rbac.generated.yaml
@@ -35,6 +35,14 @@ rules:
   - patch
   - update
   - watch
+- apiGroups:
+  - agents.x-k8s.io
+  resources:
+  - sandboxes/status
+  verbs:
+  - get
+  - patch
+  - update
 - apiGroups:
   - coordination.k8s.io
   resources:
diff --git a/k8s/extensions.controller.yaml b/k8s/extensions.controller.yaml
index 3d8d9ec..e13882a 100644
--- a/k8s/extensions.controller.yaml
+++ b/k8s/extensions.controller.yaml
@@ -19,10 +19,15 @@ spec:
       serviceAccountName: agent-sandbox-controller
       containers:
       - name: agent-sandbox-controller
-        image: ko://sigs.k8s.io/agent-sandbox/cmd/agent-sandbox-controller # placeholder value, replaced by deployment scripts
+        image: agent-sandbox-controller:latest
         args:
         - "--leader-elect=true"
         - "--extensions"
+        - "--sandbox-concurrent-workers=300"
+        - "--sandbox-claim-concurrent-workers=300"
+        - "--sandbox-warm-pool-concurrent-workers=300"
+        - "--kube-api-qps=300"
+        - "--kube-api-burst=450"
         ports:
         - name: metrics
           containerPort: 8080
