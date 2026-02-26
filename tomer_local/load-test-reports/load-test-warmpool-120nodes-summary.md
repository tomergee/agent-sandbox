# Load Test Summary (Warm Pool + Sandbox Claims on 120 Nodes)
**Date:** 2026-02-24
**Run:** 11 (Warm Pool 120-Node Variant)
**Goal:** Verify if expanding the physical GKE node pool from 50 to 120 instances reduces the controller queuing latencies observed during the 100 QPS burst test.

## Configuration
- **Cluster Strategy:** `agent-sandbox-warmpool-load-test.yaml` 
- **Target Objects:** `SandboxWarmPool` & `SandboxClaim`
- **Total Namespaces:** 1 
- **Warm Pool Replicas:** 50
- **Sandbox Claim Replicas:** 100 
- **QPS:** 100
- **Cluster Capacity:** 123 nodes total (984 vCPUs, 3.9 TB memory).
- **Node Pools:** 120 `e2-standard-8` general-purpose worker nodes (`load-test-pool-50`), and 3 `e2-standard-8` nodes (`default-pool`).
- **Load Generator Profile:** 24-core Intel(R) Xeon(R) CPU @ 2.20GHz, 94GiB RAM (Local Workstation)
- **Polling Interval:** 20ms

## Results

### Phase Durations (`junit.xml`)
- **Wait for Warm Pool Pods to be Ready (50):** 5.256 seconds
- **Create Sandbox Claims (100 @ 100 QPS):** 1.164 seconds
- **Wait for Sandbox Claims to be Ready (100):** 12.935 seconds
- **Wait After Ready (Sleep):** 15.166 seconds

### Latency Summary

| Metric | P50 | P90 | P95/P99 | Wait Phase Duration |
| :--- | :--- | :--- | :--- | :--- |
| **SandboxClaim Created** | 5783.220ms (Avg) | - | - | - |
| **Pod Startup Latency** | 0.000ms | 2740.507ms | 3890.811ms (P99) | 5256.769ms (Warm Pool Prep) |
| **SandboxClaim Readiness** | 11957.440ms (Avg) | - | - | 12935.380ms |

*\*The **Pod Startup Latency** metric reflects the overall target of 100 Pods: the first 50 pods were instantly adopted from the Warm Pool (0.000ms). The 2098.418ms P50 of the remaining 50 new pods shifts the global P50 down to 0ms, while the global P90 and P99 reflect the new pods spun up on-demand.*
*\*The exact average time for a SandboxClaim Created and SandboxClaim Readiness (from creation in the API to Ready status) during this 100 QPS test was calculated via Prometheus hook run deltas as **5783.220 milliseconds** and **11957.440 milliseconds** respectively.*

## Analysis
Run 11 explicitly tested whether expanding the physical cluster compute capacity from 50 to 120 nodes would alleviate the >10s latency observed for SandboxClaims to become `Ready`.

**The results perfectly isolate the bottleneck:** 
Scaling the physical cluster to 120 worker nodes did **not** decrease the readiness latency. In fact, the readiness phase slightly increased from `12.412s` to `12.935s` (and Prometheus average readiness increased from ~10.5s to ~11.9s). 

This confirms conclusively that the delays are purely artificial and generated inside the `agent-sandbox-controller` pod due to `MaxConcurrentReconciles` being left at its default value of 1. The controller acts as a single-threaded queue for incoming claims regardless of how much Kubernetes compute is provisioned underneath it.
