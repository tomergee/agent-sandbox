# Load Test Summary (Test 2: WP=75, QPS=200)
**Date:** 2026-02-24
**Run:** 13 (Warm Pool=75, QPS=200 Variant)
**Goal:** Verify if doubling the `SandboxClaim` creation rate to 200 QPS further degrades the readiness delay.

## Configuration
- **Cluster Strategy:** `agent-sandbox-warmpool-load-test.yaml` 
- **Warm Pool Replicas:** 75
- **Sandbox Claim Replicas:** 100 
- **QPS:** 200
- **Node Pools:** 120 `e2-standard-8` general-purpose worker nodes (`load-test-pool-50`).
- **Load Generator Profile:** 24-core Intel(R) Xeon(R) CPU @ 2.20GHz, 94GiB RAM

## Results

### Phase Durations (`junit.xml`)
- **Wait for Warm Pool Pods to be Ready (75):** 35.368 seconds
- **Create Sandbox Claims (100 @ 200 QPS):** 0.653 seconds
- **Wait for Sandbox Claims to be Ready (100):** 10.976 seconds

### Latency Summary

| Metric | P50 | P90 | P95/P99 | Wait Phase Duration |
| :--- | :--- | :--- | :--- | :--- |
| **SandboxClaim Created** | 4596.000ms | 8888.000ms | 9444.000ms (P95) | - |
| **Pod Startup Latency** | 0.000ms | 2399.206ms | 2679.212ms (P99) | 35368.583ms (Warm Pool Prep) |
| **SandboxClaim Readiness** | 10119.000ms | 10804.000ms | 10890.000ms (P95) | 10976.251ms |

*\*The **Pod Startup Latency** metric reflects the overall target of 100 Pods: the first 75 pods were instantly adopted from the Warm Pool (0.000ms). The 1926.453ms P50 of the remaining 25 new pods shifts the global P50 down to 0ms. The global P90 and P99 reflect the new pods spun up on-demand.*
*\*The exact average time for a SandboxClaim Created and SandboxClaim Readiness (from creation in the API to Ready status) during this 200 QPS test was calculated via Prometheus hook run deltas as **4641.330 milliseconds** and **10047.620 milliseconds** respectively.*

## Analysis
Doubling the QPS load from 100 to 200 cut the initial creation burst phase in half (1.16s -> 0.65s). However, the average readiness time basically remained identical (9.7s vs 10.0s). Pumping the queue twice as fast does not bypass the strict single-threaded `MaxConcurrentReconciles: 1` pipeline inside the controller.
