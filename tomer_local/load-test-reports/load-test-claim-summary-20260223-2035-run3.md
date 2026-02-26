# Agent Sandbox Load Test Summary ("Sandbox Claim Only")
Date: 2026-02-23 20:35 UTC
Run: 3

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

## Execution
The load test successfully created the `SandboxTemplate` and executed a burst creation of 20 `SandboxClaim` resources. Wait constraints were placed independently on Sandboxes (`WaitForRunningPods` using label selectors) and Sandbox claims (`WaitForGenericK8sObjects`).

### Phase Durations (`junit.xml`)
- **Create Sandbox Claims:** 2.02 seconds
- **Wait for Sandboxes to be Ready:** 5.30 seconds
- **Wait for Sandbox Claims to be Ready:** 30.40 seconds

### Latency Summary

| Metric | P50 | P90 | P99 | Wait Phase Duration |
| :--- | :--- | :--- | :--- | :--- |
| **Pod Startup Latency** | ~1.63s | ~1.84s | ~2.50s | 5.30s |
| **SandboxClaim Readiness** | - | - | - | 30.40s |

## Analysis
The test results highlight a structural quirk in how `clusterloader2` gathers measurements. The "Wait for Sandbox Claims to be Ready" constraint took 30.40 seconds, which seemingly indicates a ~25 second overhead compared to Pod readiness. However, auditing the `clusterloader2`'s `wait_for_generic_k8s_object.go` plugin reveals a hardcoded `defaultWaitForGenericK8sObjectsInterval = 30 * time.Second`. 

Because the interval is larger than the ~5s it took the pods to start, the first polling cycle missed the readiness, executing again roughly 30 seconds into the test. Thus, the real controller latency overhead from standard pod startup to `SandboxClaim` Ready phase is largely overshadowed by the polling frequency. For sub-second precision, future tests must either configure a custom `refreshInterval` or migrate entirely to `GenericPrometheusQuery` metric collection using proper controller telemetry.
