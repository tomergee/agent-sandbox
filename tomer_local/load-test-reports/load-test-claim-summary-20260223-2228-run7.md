# Load Test Summary (Sandbox Claims Only)
**Date:** 2026-02-23
**Run:** 7
**Time:** 22:28 UTC
**Goal:** Verify controller scalability by testing `SandboxClaim` latency at a high (100 QPS) creation rate with 50 concurrent replicas.

## Configuration
- **Cluster Strategy:** `agent-sandbox-claim-load-test.yaml` 
- **Target Objects:** `SandboxClaim`
- **Total Namespaces:** 1 
- **Replicas Per Namespace:** 50 
- **QPS:** 100
- **Node Pool:** 50 `e2-standard-8` general-purpose worker nodes.
- **Polling Interval:** 100ms

## Results

### Phase Durations (`junit-run7.xml`)
- **Create Sandbox Claims:** 0.63 seconds
- **Wait for Sandboxes to be Ready:** 10.25 seconds
- **Wait for Sandbox Claims to be Ready:** 0.41 seconds

### Latency Summary

| Metric | P50 | P90 | P95 | Wait Phase Duration |
| :--- | :--- | :--- | :--- | :--- |
| **Pod Startup Latency** | ~1.60s | ~2.09s | ~2.81s (P99) | 10.25s |
| **SandboxClaim Readiness** | ~6.52s | ~9.30s | ~9.65s | 0.41s |

*\*SandboxClaim readiness P50, P90, and P95 are calculated mathematically via interpolation from the Prometheus histogram buckets.*

## Analysis
Run 7 pushed the load to **100 QPS** with 50 replicas. The raw Pod Startup Latency (the time it takes a Pod to spin up once scheduled) remained fast, measuring `~2.81s` at the 99th percentile. However, the total "Wait for Sandboxes to be Ready" phase increased to `10.25s`. 

This indicates that under a 100 QPS burst, there is queuing either in the Kubernetes API server or the cluster scheduler as it processes the Sandbox templates. The end-to-end `SandboxClaim` readiness metrics directly map to this queuing, slowing down to an average of `5.41s` and a P95 of `~9.65s` from the moment the 50 claims were submitted. 

Despite the underlying platform delays, the `agent-sandbox-controller` maintained complete efficiency. The clusterloader2 Wait Phase for Sandbox Claims finished in `0.41s` -- fundamentally demonstrating that the extension controller reacts and syncs changes nearly instantaneously once the underlying Kubernetes pods successfully transition to the `Ready` state.

### Prometheus Histogram (`/metrics`)
- **Total Claims Analyzed:** 50
- **Latency Distribution:** 14 claims completed under 5 seconds, 36 claims completed between 5 and 10 seconds.
- **Sum Latency:** ~270.62 seconds
- **Average Claim Readiness Latency:** ~5.41 seconds
