# Load Test Summary (Test 1: WP=75, QPS=100)
**Date:** 2026-02-24
**Run:** 12 (Warm Pool=75 Variant)
**Goal:** Verify if increasing the `SandboxWarmPool` from 50 to 75 reduces the claim readiness latency on the scaled 120-node cluster.

## Configuration
- **Cluster Strategy:** `agent-sandbox-warmpool-load-test.yaml` 
- **Warm Pool Replicas:** 75
- **Sandbox Claim Replicas:** 100 
- **QPS:** 100
- **Node Pools:** 120 `e2-standard-8` general-purpose worker nodes (`load-test-pool-50`).
- **Load Generator Profile:** 24-core Intel(R) Xeon(R) CPU @ 2.20GHz, 94GiB RAM

## Results

### Phase Durations (`junit.xml`)
- **Wait for Warm Pool Pods to be Ready (75):** 35.273 seconds
- **Create Sandbox Claims (100 @ 100 QPS):** 1.160 seconds
- **Wait for Sandbox Claims to be Ready (100):** 11.478 seconds

### Latency Summary

| Metric | P50 | P90 | P95/P99 | Wait Phase Duration |
| :--- | :--- | :--- | :--- | :--- |
| **SandboxClaim Created** | 4516.910ms (Avg) | - | - | - |
| **Pod Startup Latency** | 0.000ms | 2341.734ms | 2772.505ms (P99) | 35273.535ms (Warm Pool Prep) |
| **SandboxClaim Readiness** | 9763.290ms (Avg) | - | - | 11478.983ms |

*\*The **Pod Startup Latency** metric reflects the overall target of 100 Pods: the first 75 pods were instantly adopted from the Warm Pool (0.000ms). The 1923.597ms P50 of the remaining 25 new pods shifts the global P50 down to 0ms. The global P90 and P99 reflect the new pods spun up on-demand.*
*\*The exact average time for a SandboxClaim Created and SandboxClaim Readiness (from creation in the API to Ready status) during this 100 QPS test was calculated via Prometheus hook run deltas as **4516.910 milliseconds** and **9763.290 milliseconds** respectively.*

## Analysis
Increasing the pool to 75 reduced the final readiness wait time from ~12.9s down to `11.478s` (and the Prometheus average fell from ~11.9s to `9.76s`). However, the controller is still a bottleneck capping performance.
