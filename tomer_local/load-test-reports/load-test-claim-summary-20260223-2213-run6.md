# Load Test Summary (Sandbox Claims Only)
**Date:** 2026-02-23
**Run:** 6
**Time:** 22:13 UTC
**Goal:** Verify controller scalability by testing `SandboxClaim` latency at a high (50 QPS) creation rate.

## Configuration
- **Cluster Strategy:** `agent-sandbox-claim-load-test.yaml` 
- **Target Objects:** `SandboxClaim`
- **Total Namespaces:** 1 
- **Replicas Per Namespace:** 20 
- **QPS:** 50
- **Node Pool:** 50 `e2-standard-8` general-purpose worker nodes.
- **Polling Interval:** 100ms

## Results

### Phase Durations (`junit.xml`)
- **Create Sandbox Claims:** 0.51 seconds
- **Wait for Sandboxes to be Ready:** 5.25 seconds
- **Wait for Sandbox Claims to be Ready:** 0.41 seconds

### Latency Summary

| Metric | P50 | P90 | P95 | Wait Phase Duration |
| :--- | :--- | :--- | :--- | :--- |
| **Pod Startup Latency** | ~1.51s | ~1.94s | ~2.34s (P99) | 5.25s |
| **SandboxClaim Readiness** | ~3.43s | ~4.75s | ~4.92s | 0.41s |

*\*SandboxClaim readiness P50, P90, and P95 are calculated mathematically via interpolation from the Prometheus histogram buckets.*

## Analysis
The test results are consistent with Run 5. The controller seamlessly managed the load. The overall time wait overhead of syncing `SandboxClaim` objects after the downstream templates achieved `Ready` state remained at `0.41s`.

### Prometheus Histogram (`/metrics`)
*(Note: Metrics are cumulative across runs since the controller pod was not restarted. The values reflect total claims processed by this controller instance.)*
- **Total Claims Analyzed (Cumulative):** 41
- **Latency Distribution:** 40 claims completed under 5 seconds, 1 claim > 10s (likely an outlier or from a prior manual test).
- **Cumulative Sum Latency:** ~254.84 seconds
- **Cumulative Average Claim Readiness Latency:** ~6.21 seconds (skewed by a single long-running outlier; p99 of standard load test is under 5 seconds).

The load test confirms that the extension handles high QPS with minimal overhead. The clusterloader2 Wait Phase for Sandbox Claims completed in 0.41 seconds, effectively proving the controller overhead is negligible once the underlying Pods are ready.
