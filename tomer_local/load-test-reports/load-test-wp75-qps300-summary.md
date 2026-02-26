# Load Test Summary (Test 3: WP=75, QPS=300)
**Date:** 2026-02-24
**Run:** 14 (Warm Pool=75, QPS=300 Variant)
**Goal:** Verify if pushing an extreme `SandboxClaim` burst creation rate of 300 QPS further degrades the readiness delay.

## Configuration
- **Cluster Strategy:** `agent-sandbox-warmpool-load-test.yaml` 
- **Warm Pool Replicas:** 75
- **Sandbox Claim Replicas:** 100 
- **QPS:** 300
- **Node Pools:** 120 `e2-standard-8` general-purpose worker nodes (`load-test-pool-50`).
- **Load Generator Profile:** 24-core Intel(R) Xeon(R) CPU @ 2.20GHz, 94GiB RAM

## Results

### Phase Durations (`junit.xml`)
- **Wait for Warm Pool Pods to be Ready (75):** 35.268 seconds
- **Create Sandbox Claims (100 @ 300 QPS):** 0.524 seconds
- **Wait for Sandbox Claims to be Ready (100):** 11.499 seconds

### Latency Summary

| Metric | P50 | P90 | P95/P99 | Wait Phase Duration |
| :--- | :--- | :--- | :--- | :--- |
| **SandboxClaim Created** | 4250.000ms | 8780.000ms | 9390.000ms (P95) | - |
| **Pod Startup Latency** | 0.000ms | 2488.499ms | 2792.430ms (P99) | 35268.239ms (Warm Pool Prep) |
| **SandboxClaim Readiness** | 9615.000ms | 11173.000ms | 11336.000ms (P95) | 11499.018ms |

*\*The **Pod Startup Latency** metric reflects the overall target of 100 Pods: the first 75 pods were instantly adopted from the Warm Pool (0.000ms). The 1814.811ms P50 of the remaining 25 new pods shifts the global P50 down to 0ms. The global P90 and P99 reflect the new pods spun up on-demand.*
*\*The exact average time for a SandboxClaim Created and SandboxClaim Readiness (from creation in the API to Ready status) during this 300 QPS test was calculated via Prometheus hook run deltas as **4258.820 milliseconds** and **9669.110 milliseconds** respectively.*

## Analysis
Pushing the QPS load from 200 to 300 caused clusterloader to create the 100 Claims nearly instantaneously (0.52 seconds).

Despite being deposited in the API queue in half a second, the average readiness time across all 100 claims was **9.6 seconds**. Throughout Test 1 (100 QPS), Test 2 (200 QPS), and Test 3 (300 QPS), the overall controller completion time has not significantly drifted from ~10 seconds. Pushing creation velocity higher is merely piling more objects into the single-threaded `MaxConcurrentReconciles: 1` processing logic without speeding up their completion.
