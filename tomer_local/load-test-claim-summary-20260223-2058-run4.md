# Agent Sandbox Load Test Summary ("High Accuracy Sandbox Claim")
Date: 2026-02-23 20:58 UTC
Run: 4

## Cluster Configuration
- **Provider:** GKE
- **Project:** `gke-ai-eco-dev`
- **Node Type:** `e2-standard-8`
- **Total Nodes:** 53 (3 default + 50 `load-test-pool-50`)

## Test Configuration
- **Test File:** `agent-sandbox-claim-load-test.yaml`
- **Namespaces:** 1 (`agent-sandbox-1`)
- **QPS:** 10
- **Sandbox Claims:** 20 (mapped to 1 SandboxTemplate)
- **Measurement Method:**
  - `PodStartupLatency` (built-in pod startup timing)
  - `WaitForGenericK8sObjects` (time until SandboxClaims hit `Ready=True`)
  - **Polling Interval:** Custom configured to **100ms** (default 30s)

## Execution
The load test successfully created the `SandboxTemplate` and executed a burst creation of 20 `SandboxClaim` resources. Wait constraints were placed independently on Sandboxes (`WaitForRunningPods` using label selectors) and Sandbox claims (`WaitForGenericK8sObjects`).

### Phase Durations (`junit.xml`)
- **Create Sandbox Claims:** 2.03 seconds
- **Wait for Sandboxes to be Ready:** 5.30 seconds
- **Wait for Sandbox Claims to be Ready:** 0.51 seconds

### Latency Summary

| Metric | P50 | P90 | P99 | Wait Phase Duration |
| :--- | :--- | :--- | :--- | :--- |
| **Pod Startup Latency** | ~1.49s | ~2.20s | ~2.38s | 5.30s |
| **SandboxClaim Readiness** | - | - | - | 0.51s |

## Analysis
By modifying the `clusterloader2`'s `WaitForGenericK8sObjects` constraint polling interval to `100ms`, the true lifecycle controller overhead of processing `SandboxClaim` objects becomes clearly visible. 

The test reveals an overhead of roughly **~0.51 seconds** between the time all 20 underlying Pods (and Sandboxes) achieve `Running`/`Ready` and the time the dependent 20 `SandboxClaim` objects propagate this state and enter the `Ready` phase themselves. This confirms the controller's lightweight and efficient operation on the cluster, easily scaling within the limits of sub-second operational constraints.
