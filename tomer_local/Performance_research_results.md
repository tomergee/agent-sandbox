# Performance Research Results

## Load Test Conclusion: Controller Scale Overhead
During Run 9 testing of the `agent-sandbox` controller's ability to handle an extreme burst of 100 `SandboxClaim` creations concurrently, an artificial latency gap of ~5 seconds was observed between the underlying `Pod` becoming ready and the `SandboxClaim` subsequently reaching the Ready state.

## Root Cause Analysis
The bottleneck was identified to be within the Go controller software process itself, rather than the Kubernetes scheduler or API server.

1. **The Event Queue:** `clusterloader2` drops 100 `SandboxClaim` objects into the cluster almost instantaneously (creating them took just 1.163 seconds).
2. **The Concurrency Default:** By default, the `controller-runtime` library initializes controllers with a `MaxConcurrentReconciles` limit of exactly `1`.
3. **The Bottleneck:** When 100 Sandboxes are created, and 100 Pods suddenly all become `Ready` at the exact same time, 100 update events hit the controller at once.
4. Because it's single-threaded, the `Sandbox` controller churns through them sequentially, strictly one-by-one, to update the `Sandbox` status.
5. Then, the `SandboxClaim` controller does the exact same single-threaded churn to update the 100 `SandboxClaim` statuses.

Because the controllers process events sequentially (and are further constrained by standard Kubernetes client rate limits of 20 QPS), an artificial backlog forms solely inside the Go controller process, injecting ~5 seconds of pure queue overhead before the final statuses are persisted to the API server.

## Remediation / Next Steps
To speed up the processing pipeline under high load and bypass this artificial single-threaded bottleneck, the `MaxConcurrentReconciles` configuration can be explicitly injected into the builder chain using `controller.Options` inside `SetupWithManager` for each controller. For example:

```go
import "sigs.k8s.io/controller-runtime/pkg/controller"

// ... inside SetupWithManager ...
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{MaxConcurrentReconciles: 20}).
		For(&sandboxv1alpha1.Sandbox{}).
		Owns(&corev1.Pod{}, builder.WithPredicates(labelSelectorPredicate)).
		Owns(&corev1.Service{}, builder.WithPredicates(labelSelectorPredicate)).
		Complete(r)
```

### 2. SandboxClaim Warm Pool Adoption: The Thundering Herd Conflict
During load testing of the `SandboxWarmPool` feature, an adoption bottleneck was identified when 100 `SandboxClaim` resources were created concurrently. 

**The Bug:** The controller deterministically sorted the available Warm Pool pods using `podutils.ByLogging` and always attempted to adopt the first pod (`candidates[0]`). Under heavy concurrent load:
1. All workers grabbed the same sorted list.
2. All workers attempted to update the exact same Pod object API simultaneously.
3. One succeeded; 99 failed with Kubernetes Conflict errors ("the object has been modified").
4. The remaining 99 claims continuously requeued, creating a thundering herd serialized contention loop that caused massive timeouts.

**The Fix:** 
The deterministic sorting was replaced with randomized selection (`rand.Intn()`) for pod adoption. This spreads the concurrent adoption requests evenly across all available pods in the Warm Pool, immediately eliminating the API conflicts and allowing massive parallel adoptions to succeed.

```go
// Instead of deterministic sorting which causes thundering herd conflicts when scaling,
// we randomly select a candidate.
randomIndex := rand.Intn(len(candidates))
pod := candidates[randomIndex]
```

### 3. SandboxClaim Readiness: The Single-Threaded API Queue
During the 120-node cluster scaled tests (Run 12, 13, and 14), pushing `SandboxClaim` creation rates out to **300 QPS** mathematically proved that the >10s delay in readiness is isolated entirely within the controller software queue—not the physical Kubernetes Nodes.

The compound bottleneck is created by the controller architecture:
1. **Strict Single-Threaded Queue (`MaxConcurrentReconciles: 1`)**: By default, the `controller-runtime` framework processes events exactly one at a time. 100 concurrent claims are placed in a single-file line.
2. **Heavy Synchronous API Server Communication**: For each claim in that line, the `SandboxClaim` reconciler makes at least 6 round-trip network calls to the K8s API server (GET template, CREATE/UPDATE network policy, LIST pool pods, UPDATE adopted pod, CREATE Sandbox, UPDATE claim status). At ~15ms each, one reconciliation loop takes ~100ms. 100 claims * 100ms = 10 seconds of pure queue waiting.
3. **Controller Client-Go Rate Limiting**: The `client-go` library used by the controller has a hardcoded default safety limit of **20 QPS**. Because 100 claims require ~600 API calls, the controller starts internally throttling itself to avoid breaking the 20 QPS speed limit, slowing down the single thread even further.
4. **Double Sequential Queueing**: Once the `SandboxClaim` controller creates the `Sandbox` object, the core `Sandbox` controller (which is *also* single-threaded) has to pick it up, inspect the adopted pod, and mark the `Sandbox` as `Ready`. Then the `SandboxClaim` reconciler runs a third time to finally mark the Claim as `Ready`.

Even at an instantaneous burst of 300 QPS over 120 nodes, the controller acts like a single supermarket cashier ringing up 100 customers simultaneously, completely ignoring the available compute capacity.

## Remediation: Bypassing the Single-Threaded API Queue
To speed up the processing pipeline under extreme `SandboxClaim` bursts, two critical controller-runtime bottlenecks must be removed simultaneously:

### 1. Removing the Event Queue Bottleneck (`MaxConcurrentReconciles`)
By default, controllers use a `MaxConcurrentReconciles` limit of exactly `1`. This must be overridden via `controller.Options` inside `SetupWithManager` to allow parallel event handling.

```go
import "sigs.k8s.io/controller-runtime/pkg/controller"

// ... inside SetupWithManager ...
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{MaxConcurrentReconciles: 50}).
		For(&sandboxv1alpha1.Sandbox{}).
		Complete(r)
```

### 2. Removing the API Network Bottleneck (`client-go` QPS)
Simply adding parallel threads isn't enough; the underlying `client-go` library has a hardcoded safety limit of **20 QPS**. The controller configuration (`rest.Config`) passed to the Manager must be intercepted and boosted:

```go
	// In cmd/agent-sandbox-controller/main.go
	cfg := ctrl.GetConfigOrDie()
	cfg.QPS = 200
	cfg.Burst = 300

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{...})
```

## Results After Tuning `MaxConcurrentReconciles` and `client-go` limits
After applying the `MaxConcurrentReconciles=50` and QPS=200 configuration changes to both the `Sandbox` and `SandboxClaim` controllers, the load test (Test 4: WP=75, QPS=200) was re-run to validate the fix.

### Massive Latency Improvement
The tuning successfully resolved the artificial single-threaded queueing. The overall Readiness Latency for a burst of 100 `SandboxClaim` objects dropped dramatically:

| Metric | Before Tuning (QPS=200) | After Tuning (QPS=200) | Improvement |
| :--- | :--- | :--- | :--- |
| **Wait for Sandbox Claims to be Ready** | 10.976 seconds | 4.934 seconds | **55% Faster** |
| **Average SandboxClaim Readiness Latency** (Prometheus) | 10.047 seconds | 2.710 seconds | **73% Faster** |
| **Average SandboxClaim Creation Latency** (Prometheus) | 4.641 seconds | 1.577 seconds | **66% Faster** |

### Conclusion
By overriding the single-threaded default (`MaxConcurrentReconciles`) and boosting the Kubernetes API client rate limits (`rest.Config.QPS`), the `agent-sandbox` controller can now process concurrent bursts of `Sandbox` and `SandboxClaim` resources in parallel. This prevents the controller itself from becoming a bottleneck during high-throughput agent sandbox leasing architectures like Moltbot.

## Extreme Scale Split-Burst Test (480 Nodes)
A massive scale test was conducted on a 480-node GKE cluster to simulate real-world RL agent training workloads (like Tunix), which require large Warm Pools and bursty SandboxClaim allocation over hundreds of nodes.

### Test Configuration
- **Cluster Size**: 480 `e2-standard-8` nodes
- **Warm Pool**: 200 `Sandbox` pods
- **Load Pattern (Split-Burst)**:
  - Burst 1: 100 `SandboxClaims` at 100 QPS
  - Pause: 20 seconds
  - Burst 2: 100 `SandboxClaims` at 100 QPS

### Results
The tuned `agent-sandbox` controller (with `MaxConcurrentReconciles=50`, `QPS=200`, `Burst=300`) handled the extreme scale flawlessly, instantly adopting from the 200-pod Warm Pool without any API "thundering herd" conflicts.

| Phase | Metric | Latency |
| :--- | :--- | :--- |
| **Warmup** | Wait for 200 Warm Pool Pods to be Ready | **10.65 seconds** |
| **Burst 1** | Wait for first 100 SandboxClaims to be Ready | **2.67 seconds** |
| **Burst 2** | Wait for second 100 SandboxClaims to be Ready | **4.65 seconds** |

### Conclusion
The final load testing confirms that the `agent-sandbox` controller is highly scalable. By eliminating the default `controller-runtime` single-threaded bottlenecks and adopting a randomized Warm Pool selection strategy, the controller comfortably supports massive RL training workloads with negligible API overhead (processing bursts of 100 claims in under 5 seconds at 480-node scale).
