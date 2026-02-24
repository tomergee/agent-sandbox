# Concurrent Load Test Implementation Plan

## Objective
This document outlines the methodology established during the most recent extreme-scale concurrent load tests (e.g., the 6k Rapid Pulse Burst and the 20k Continuous Burst test runs). The goal is to provide a structured approach for safely operating and measuring the tuned `agent-sandbox-controller` under massive parallel demand without bottlenecking the Kubernetes API or triggering catastrophic thundering herd events.

## Methodology

### 1. Procedural YAML Manifest Generation
ClusterLoader2 does not natively support continuous `for` loop constructs within its phase execution logic. To simulate an unrelenting barrage of sandboxes:
- Create a Python generator script (e.g., `generate-20k-burst-yaml.py`) to systematically write sequential burst phases into a custom ClusterLoader2 YAML manifest.
- **Example Pattern**: Generate 60 to 200 sequential phases, where each phase drops 100 `SandboxClaim` objects and then forcibly pauses for an interval (e.g., 5 to 20 seconds).

### 2. Extreme Cluster Scaling Strategy
Before initiating concurrent spikes, the testing environment must be physically capable of buffering the scheduling queue.
- Pre-scale the GKE cluster node pool (e.g., `load-test-pool-50`) up to **480 worker nodes**, approaching standard Google Cloud regional limits.
- The 480-node capacity provides the theoretical runway to schedule ~50,000 lightweight pods before hitting pure capacity starvation.

### 3. Controller Tuning & Optimization
The baseline controller suffers from severe latency under highly concurrent bursts. To resolve these bottlenecks:
- **Unlock Multi-Threading**: Modify `controller-runtime` by wrapping `SetupWithManager` with `MaxConcurrentReconciles: 100` for all three core sub-controllers (`Sandbox`, `SandboxClaim`, and `SandboxWarmPool`) to expand the single-threaded event processing queues.
- **Boost API Throughput**: Adjust the Kubernetes client config in `cmd/agent-sandbox-controller/main.go` by setting `cfg.QPS = 200` and `cfg.Burst = 300` upon initialization.
- **Randomize Pod Adoption**: Enable randomized Warm Pool pod selection logic in the controller to natively bypass deterministic Kubernetes `ResourceVersion` update conflicts (thundering herd cache collisions) when adopting hundreds of identical pods simultaneously.

### 4. Warm Pool Pre-Initialization & Buffering
Massive concurrency requires aggressive, pre-provisioned cache states to offset basic GCP container spin-up latencies.
- Ensure the ClusterLoader2 file explicitly configures an oversized `SandboxWarmPool` (e.g., 500 pods for a 6k total test, or 1000 pods for a 20k test) during the initial `Wait` phase.
- **Timeout Protection:** Because provisioning thousands of Warm Pool pods simultaneously forces a massive Kubernetes scheduler backlog, hardcoded runner timeouts inside ClusterLoader2 must be increased to at least `60m` to prevent silent aborts while GKE catches up.

### 5. Execution and Analysis Model
Data collection during a massive continuous load test requires specific segregation logic since standard Prometheus histograms are aggregated incrementally over time without natural reset points.
- **The "Golden Phase" (Cache-Hit):** Track the first sequence of bursts that perfectly map into the initial pre-initialized Warm Pool (e.g., Bursts 1-10 mapping into the 1,000-pod Warmpool). These should yield pure controller latency (~1s readiness).
- **The "Catch-Up Phase" (Stressed Instantiation):** Track the subsequent bursts where the controller must process new incoming `SandboxClaims` *while simultaneously* generating brand new Warm Pool pods to replace the depleted cache. Expect latency degradation as the underlying Kubernetes scheduling plane hits saturation limits.
- Extract precise `junit.xml` XML outputs across isolated test windows to manually separate out tainted Prometheus data leftover from previously aborted timeouts. Calculate clean P50, P90, and P99 percentiles from individual chronological bursts.
