# Load Test Summary (100-Node 300 QPS Rapid Burst)
**Date:** 2026-02-25
**Time:** 22:57 UTC
**Goal:** Evaluate the `agent-sandbox` controller's scalability and latency performance on a 100-node cluster using a rapid burst pattern (300 SandboxClaims) with tuned QPS (300) and concurrency (300).

## Configuration
- **Project:** `gke-ai-eco-dev`
- **Cluster Name:** `agent-sandbox-burst`
- **Cluster Strategy:** `agent-sandbox-warmpool-load-test.yaml`
- **Target Objects:** `SandboxWarmPool` & `SandboxClaim`
- **Total Namespaces:** 1 
- **Cluster Size:** 103 Nodes (3 default + 100 load-test nodes)
- **Warm Pool Replicas:** 300
- **Total Load Pattern:** 1 Burst
- **Burst Size:** 300 `SandboxClaims`
- **Controller Configuration:**
    *   `MaxConcurrentReconciles`: 300 (Sandbox, SandboxClaim, SandboxWarmPool)
    *   `client-go` QPS: 300
    *   `client-go` Burst: 450

## Results

### Phase Durations (`junit.xml`)
- **Wait for Warm Pool Pods to be Ready (300):** 40.276 seconds
- **Create Sandbox Claims:** 1.339 seconds
- **Wait for Sandbox Claims to be Ready:** 30.645 seconds
- **Pause for Data Collection:** 15.105 seconds

### Latency Summary

| Metric | P50 | P90 | P99 | Phase Description |
| :--- | :--- | :--- | :--- | :--- |
| **Pod Startup Latency** | 1.10s | 1.65s | 1.97s | Run to Watch (CL2 observed) |
| **SandboxClaim Created** | ~1.54s | ~2.03s | ~2.65s | API Creation to Controller Observed |
| **SandboxClaim Readiness**| ~2.07s | ~2.74s | ~3.36s | API Creation to Readiness |

*\*The **Pod Startup Latency** represents the time from pod scheduled/running to reachable. At 1.97s P99, it is well within the 5s threshold.*
*\*The **SandboxClaim Readiness** latency improved significantly, hitting 3.36s at P99 under 300 QPS load on the regional cluster.*

## Analysis
The test completed successfully with zero failures and all latency targets for pod startup respected. The transition to a regional cluster (`agent-sandbox-burst`) combined with controller tuning (300 QPS/Concurrency) demonstrated phenomenal improvements.

1. **Pod Startup Stability**: With 300 SandboxClaims arriving simultaneously, the warm pool pods became reachable within 2 seconds for 99% of cases.
2. **Controller Responsiveness**: The P99 of 2.65s for "Claim Created" (API to Controller) shows that the API server and the controller's watch/reflector loop are keeping up exceptionally well with the 300 burst, avoiding the 11-second queuing delays seen in earlier tests.
3. **Readiness Efficiency**: The delta from Created to Ready is under 1 second (2.65s -> 3.36s), confirming that the parallel reconciliation (`MaxConcurrentReconciles: 300`) is highly efficient at processing the workqueue and leasing pods without stalling.
4. **Regional Scalability**: The extra capacity of the 3-master Node regional control plane (`e2-standard-32` across 102 nodes) provided the necessary API throughput to accommodate the controller's increased `client-go` rate limits.
- Correlate specific "late" claims with controller logs to see if they were stuck in the workqueue.
