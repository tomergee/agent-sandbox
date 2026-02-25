# Supporting Concurrency in Controllers

## Background
During recent scale-testing and production-readiness efforts, we have transitioned the `agent-sandbox` to support massive-scale deployments, specifically targeting bursty workloads where thousands of sandboxes are launched simultaneously. These high-pressure scenarios revealed critical limitations in the default Kubernetes controller scaling, necessitating the deep optimizations detailed in this document to handle the "thundering herd" effect and ensure low-latency sandbox delivery.

## The Problem
In high-throughput environments—such as scaling the `agent-sandbox` to process bursts of 5,000 to 10,000 `SandboxClaim` and `Sandbox` objects simultaneously—the default configurations of the Kubernetes `controller-runtime` framework introduce severe processing bottlenecks. 

By default:
1. **Single-Threaded Workqueue:** Controllers are initialized with a `MaxConcurrentReconciles` limit of exactly `1`. This forces the controller to process incoming events sequentially, one at a time, entirely defeating the purpose of a highly parallel distributed system.
2. **API Rate Limiting:** The default `client-go` REST configuration imposes extremely strict API rate limits (typically 20 QPS and 30 Burst).

When a massive load of objects is applied to the cluster, this single-threaded reconciliation loop and restrictive API rate limit cause a profound backlog. The controller falls behind the cluster state, resulting in long delays when adopting warmpool pods or provisioning new sandboxes, which ultimately leads to load test timeouts.

## Immediate Steps Taken
To bypass these artificial bottlenecks and unlock parallel event handling, we explicitly injected configuration overrides into the controller builder chain during initialization. 

**Controller Configuration:**
*   `MaxConcurrentReconciles`: 100 (Configured via `controller.Options` in `SetupWithManager` within `controllers/sandbox_controller.go`, `extensions/controllers/sandboxclaim_controller.go`, and `extensions/controllers/sandboxwarmpool_controller.go`)
*   `client-go` QPS: 200 (Configured in `cmd/agent-sandbox-controller/main.go`)
*   `client-go` Burst: 300 (Configured in `cmd/agent-sandbox-controller/main.go`)
*   **Randomized Pod Selection:** To mitigate "thundering herd" collisions during high-concurrency bursts, we refactored `tryAdoptPodFromPool` in [extensions/controllers/sandboxclaim_controller.go](file:///usr/local/google/home/glottman/dev/jetski_main/agent-sandbox/extensions/controllers/sandboxclaim_controller.go) to use `rand.Intn` for candidate selection instead of deterministic sorting. This spreads the load across the available warmpool pods more effectively.
    ```go
    // extensions/controllers/sandboxclaim_controller.go
    
    // Instead of deterministic sorting which causes thundering herd conflicts when scaling,
    // we randomly select a candidate.
    randomIndex := rand.Intn(len(candidates))
    pod := candidates[randomIndex]
    ```

These overrides allowed the `agent-sandbox` manager to spin up 100 parallel worker goroutines per controller. Coupled with the boosted API throughput, this immediately alleviated the queue buildup and allowed the controller to seamlessly handle 200+ API requests per second during rapid-burst load testing.

## Additional Suggestions for Future Refactoring

To achieve even greater concurrency, stability, and throughput as we scale towards enterprise-grade production, we should consider implementing the following enhancements across all our core controllers—specifically the `Sandbox`, `SandboxClaim`, and `SandboxWarmPool` controllers:

1. **Optimistic Locking and Conflict Retries:** With 100 concurrent workers constantly modifying paired resources (e.g., updating the `.status` of a `SandboxClaim` matching a `SandboxWarmPool` pod), we will inevitably encounter Kubernetes API `Conflict` errors (HTTP 409). We should refactor status updates to utilize `retry.RetryOnConflict` from the `client-go/util/retry` package. This allows the local thread to automatically re-fetch and re-apply state changes instantly, rather than returning the error and forcing the entire event to drop to the back of the workqueue.

## Deep Dive: SandboxClaim Controller Optimization

The `sandboxclaim_controller.go` is the primary entry point for users requesting resources. Under a 5,000-claim burst, this controller faces intense competition for the "warm" resources managed by the Pool controller.

### Concurrency Recommendations:

**`tryAdoptPodFromPool`**
*   **Prevent Race Conditions:** Currently, multiple concurrent `SandboxClaim` workers might list the same "Ready" pods from the `SandboxWarmPool` and attempt to adopt them simultaneously. While random selection (`rand.Intn`) reduces collisions, it doesn't eliminate them.
    *   **Fix:** Wrap the `r.Update(ctx, pod)` call (which removes pool labels and sets owner refs) in a `RetryOnConflict` loop. If an update fails because another claim worker grabbed the pod first, the retry logic should re-list and pick a *different* candidate immediately.

**`reconcileNetworkPolicy`**
*   **Idempotency with Patching:** The current `CreateOrUpdate` logic performs a full `Get` followed by a potential `Update`. 
    *   **Fix:** Switch to a Server-Side Apply (SSA) or a simple `Patch` operation to ensure that the NetworkPolicy is applied efficiently without requiring a complete read-modify-write cycle, which is prone to conflicts if the claim is updated via other status changes simultaneously.

**`updateStatus`**
*   **Status Conflict Avoidance:** Like the WarmPool controller, the `SandboxClaim` status is high-traffic. 
    *   **Fix:** Use `RetryOnConflict` for all `r.Status().Update` calls. This is especially vital when high-frequency metrics (like `SandboxClaimReadyLatency`) are being recorded based on state transitions that might happen across parallel reconciles.

## Deep Dive: Sandbox Controller Optimization

The core `Sandbox` controller handles the lifecycle of the underlying Pod, Service, and PVCs. While smaller in scope than a 200-pod WarmPool, its reliability is critical when processing thousands of individual sandboxes simultaneously.

### Concurrency Recommendations:

**`reconcileChildResources` & `reconcilePVCs`**
*   **Sequential I/O:** Currently, the controller reconciles PVCs, then the Pod, then the Service in order. If `VolumeClaimTemplates` contains multiple PVCs, they are also created one-by-one.
    *   **Fix:** Use `errgroup` to parallelize the reconciliation of independent child resources. PVC creation and Service creation can happen concurrently with the Pod lookup/creation logic.

**`updateStatus`**
*   **Conflict Resilience:** The Sandbox status is frequently updated by both this controller and potentially external components (like observability tools or the `SandboxClaim` controller listing it).
    *   **Fix:** Apply `RetryOnConflict` to `r.Status().Update`.

**`reconcilePod`**
*   **Update vs. Patch:** The logic at line 431 uses a full `r.Update(ctx, pod)` to fix labels and owner references. 
    *   **Fix:** Transition to `r.Patch` where possible, or wrap the `r.Update` in a `RetryOnConflict` loop to handle cases where the Pod's status (e.g., Phase changes) is updated by the Kubelet or other agents simultaneously.

## Deep Dive: SandboxWarmPool Controller Optimization

Based on a review of the `sandboxwarmpool_controller.go` code, the controller is highly susceptible to sequential blocking. While we bumped `MaxConcurrentReconciles` to allow 100 different WarmPools to be processed at once, a single WarmPool scaling to 200 replicas still performs its operations entirely synchronously. 

Here are the specific, function-level refactoring recommendations to make the WarmPool controller insanely fast:

### Global Optimizations
*   **Parallelize I/O with `errgroup`:** The controller relies heavily on sequential `for` loops to process Pods. We should introduce `golang.org/x/sync/errgroup` to execute independent `Delete`, `Create`, and `Update` API calls concurrently within the same Reconcile iteration, bounded by a reasonable batch size (e.g., 50 concurrent operations).

### Function-Level Refactoring

**`reconcilePool`**
*   **Template Fetching Bottleneck:** Currently, the loop calls `createPoolPod`, which inherently calls `r.getTemplate(ctx, warmPool)` for *every single pod* it creates. If the pool is scaling up by 200 pods, it fetches the exact same `SandboxTemplate` 200 times sequentially. 
    *   **Fix:** Fetch the `SandboxTemplate` **exactly once** at the beginning of `reconcilePool`, and pass the populated template struct down to the creation logic.
*   **Sequential Pod Creation:** The `for i := int32(0); i < podsToCreate; i++` loop executes `r.createPoolPod` synchronously. This means 200 round-trips to the API server in a single file line.
    *   **Fix:** Dispatch these pod creations in parallel using an `errgroup`.
*   **Sequential Pod Deletion & Adoption:** Similar to creation, the logic for finding orphaned pods (`r.adoptPod`) and trimming excess pods (`r.Delete`) operates sequentially. 
    *   **Fix:** Parallelize these operations so that adopting 50 orphans or deleting 50 old pods occurs concurrently rather than waiting for each API call to return.

**`createPoolPod`**
*   **Remove Redundant Lookups:** As mentioned above, remove the internal `r.getTemplate` call entirely. Update the function signature to accept `template *extensionsv1alpha1.SandboxTemplate` directly.

**`adoptPod` & `updateStatus`**
*   **Conflict Resilience:** Both functions fire `r.Update` or `r.Status().Update`. If a WarmPool is being heavily interacted with by multiple controllers (e.g. `SandboxClaim` adopting pods out of the pool), these raw updates will frequently throw `HTTP 409 Conflict` errors.
    *   **Fix:** Wrap the inner update calls with `retry.RetryOnConflict(retry.DefaultRetry, func() error { ... })`. Inside the closure, re-fetch the latest copy of the `Pod` or `SandboxWarmPool` using `r.Get` to ensure the update applies cleanly over the most recent resource version.
2. **Explicit Field Indexing (Caching):** Under extreme concurrency, `List` operations querying for child objects can severely lag if they hit the live API server. We should explicitly configure Field Indexers on startup (via `mgr.GetFieldIndexer().IndexField`) for frequent lookups—for example, indexing `Sandbox` objects by their `OwnerReference` to the `SandboxWarmPool`. This guarantees that high-frequency `List` calls resolve instantly from the local Informer cache.
3. **Decouple Blocking I/O:** Since the Reconcile loop is tightly managed by the worker pool, any slow network request, I/O operation, or long-running script blocks that worker thread entirely. Any blocking operations should be handed off to an asynchronous processor or managed via state phases (e.g., marking a claim as `Provisioning` and requeuing with a small timeout) rather than sleeping or blocking inline.
4. **Custom Workqueue Rate Limiters:** `controller-runtime` utilizes an exponential backoff rate limiter. Under extreme edge cases (like a mass 5,000-pod `CrashLoopBackOff` failure), this queue can still overflow or cycle excessively. We can tune the `RateLimiter` interface within `controller.Options` using a customized `workqueue.NewItemExponentialFailureRateLimiter` to granularly control the minimum and maximum retry pacing under heavy conflict load.
5. **Enable `pprof` Profiling:** As we ramp up goroutines, memory and GC pauses become the next bottleneck horizon. Enabling `pprof` on the controller's runtime will allow us to actively profile CPU and memory allocations mid-burst to identify inefficient algorithms or massive memory allocations in the Reconcile loops.

## Implementation Plan: SandboxWarmPool Controller

To implement the suggested performance and concurrency improvements, we will execute the following steps in `extensions/controllers/sandboxwarmpool_controller.go`:

### 1. Import Dependencies
*   Add `"k8s.io/client-go/util/retry"` to the imports.
*   Add `"golang.org/x/sync/errgroup"` for parallel API operations.

### 2. Refactor `reconcilePool`
*   **Template Hoisting:** Move `r.getTemplate` to the top of the function. Store the result and pass it to downstream creation logic.
*   **Parallel Pod Creation:** Replace the synchronous `for` loop for pod creation with an `errgroup.Group`. Dispatch `r.createPoolPod` calls concurrently.
*   **Parallel Deletion/Adoption:** Similarly, parallelize `r.adoptPod` and `r.Delete` calls for orphaned and excess pods.

### 3. Refactor `createPoolPod`
*   Modify the signature to accept `template *extensionsv1alpha1.SandboxTemplate`.
*   Remove the internal `r.getTemplate` lookup call.

### 4. Enhance Conflict Resilience
*   **`adoptPod`**: Wrap `r.Update(ctx, pod)` in `retry.RetryOnConflict`. Inside the retry loop, perform a fresh `r.Get` to ensure the resource version is current before applying the owner reference.
*   **`updateStatus`**: Wrap `r.Status().Update(ctx, warmPool)` in `retry.RetryOnConflict`. Re-fetch the `SandboxWarmPool` instance within the retry closure to handle simultaneous status updates from other controllers.

## Implementation Plan: SandboxClaim Controller

To implement the suggested performance and concurrency improvements, we will execute the following steps in `extensions/controllers/sandboxclaim_controller.go`:

### 1. Import Dependencies
*   Add `"k8s.io/client-go/util/retry"` to the imports.

### 2. Refactor `tryAdoptPodFromPool`
*   **Conflict Resilience:** Wrap the `r.Update(ctx, pod)` call in `retry.RetryOnConflict`. 
*   **Retry Logic:** If a conflict occurs, the closure should return the error. The outer loop should then re-trigger the reconcile or we can implement a internal loop to re-list candidates and try again immediately to minimize latency.

### 3. Refactor `reconcileNetworkPolicy`
*   **SSA/Patching:** Replace `controllerutil.CreateOrUpdate` with a `Patch` operation using `client.MergeFrom`. This avoids the need for a full `Get` and reduces the chance of conflicts on the NetworkPolicy object.

### 4. Enhance Status Updates
*   **`updateStatus`**: Wrap `r.Status().Update(ctx, claim)` in `retry.RetryOnConflict`. Ensure a fresh `r.Get` is performed inside the retry function to capture any changes to the claim (like labels or annotations) made by other concurrent workers.

## Implementation Plan: Sandbox Controller

To implement the suggested performance and concurrency improvements, we will execute the following steps in `controllers/sandbox_controller.go`:

### 1. Import Dependencies
*   Add `"k8s.io/client-go/util/retry"` to the imports.
*   Add `"golang.org/x/sync/errgroup"` to the imports.

### 2. Refactor `reconcileChildResources`
*   **Parallel Execution:** Use an `errgroup` to call `reconcilePVCs`, `reconcilePod`, and `reconcileService` concurrently. This ensures that a slow PVC provisioning doesn't block the Service or Pod checks.

### 3. Refactor `reconcilePVCs`
*   **Parallel PVC Creation:** Parallelize the loop over `sandbox.Spec.VolumeClaimTemplates` using an `errgroup` to allow multiple PVCs to be created and tracked simultaneously.

### 4. Enhance Conflict Resilience
*   **`updateStatus`**: Wrap the `r.Status().Update(ctx, sandbox)` call in `retry.RetryOnConflict`.
*   **`reconcilePod`**: Wrap the `r.Update(ctx, pod)` call (line 431) in `retry.RetryOnConflict` and perform a fresh `r.Get` within the closure to capture status updates from other controllers or the Kubelet.
