# Load Test Summary (100-Node 6,000 Burst with 1000 Warm Pool)
**Date:** 2026-02-26
**Run ID:** 20260226-045307 (run20)
**Goal:** Evaluate the `agent-sandbox` controller's scalability using a massive multi-burst pattern (6,000 SandboxClaims staggered in 20 bursts) with 1,000 warm pool replicas, running on tuned client-go QPS limits.

## Configuration
- **Project:** `gke-ai-eco-dev`
- **Cluster Name:** `agent-sandbox-burst`
- **Target Objects:** `SandboxWarmPool` & `SandboxClaim`
- **Total Namespaces:** 1 
- **Cluster Size:** 103 Nodes (3 default + 100 load-test nodes)
- **Warm Pool Replicas:** 1,000
- **Total Load Pattern:** 20 Bursts
- **Burst Size:** 300 `SandboxClaims` per burst
- **Burst Pause Time:** 20 seconds
- **Controller Configuration:**
    *   `MaxConcurrentReconciles`: 300
    *   `client-go` QPS: 1000
    *   `client-go` Burst: 2000

## Results
- **Overall Execution Time:** ~12 minutes (707 seconds)

### Phase Durations (`junit.xml`)
- **Wait for Warm Pool Pods to be Ready (1,000):** ~55.37 seconds
- **Create Burst of 300 Sandbox Claims:** ~1.35 seconds (avg)
- **Wait for 300 Sandbox Claims to be Ready:** ~2.5 seconds (avg)
  - _Peak variance:_ Occurred at Burst 8, with a readines wait time of 10.08 seconds.

### Latency Summary
| Metric | P50 | P90 | P99 | Phase Description |
| :--- | :--- | :--- | :--- | :--- |
| **Pod Startup Latency** | ~1.92s | ~2.48s | ~2.80s | Run to Watch (CL2 observed thresholds) |
| **SandboxClaim Created** | ~1.02s | ~2.20s | ~2.45s | API Creation to Controller Observed |
| **SandboxClaim Readiness**| ~1.50s | ~4.00s | ~4.80s | API Creation to Readiness |

### Prometheus Back-End Latency (`/metrics`)
Analyzing the `sandbox_claim_ready_latency_seconds` histogram across the test run confirms low latency adoption:
- Virtually all 6,000 claims successfully transitioned to Ready well within the `2.5s` and `5s` buckets.

*\*The **Pod Startup Latency** represents the time from pod scheduled/running to reachable.*
*\*The **SandboxClaim Readiness** latency at ~4.80s P99 proves the controller successfully processed a massive macro-burst of 6,000 claims.*

## Conclusion
The controller successfully digested a staggered payload of 6,000 sandbox claims at a rate of 300 claims every 20 seconds. Despite the significant load, the expanded client-go QPS configuration (1000/2000) allowed the controller to instantly drain its work queue. Wait times remained steady at approximately 2.5 seconds per burst, proving the multi-threaded optimizations hold up effectively against compounding scaling events.
