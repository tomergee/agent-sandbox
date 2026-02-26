# Load Test: 5,000 Rapid Bursts (20s Intervals)

## Objective
Verify the controller's ability to handle a sustained, long-duration barrage of continuous burst traffic while managing a pre-warmed sandbox buffer.

## Configuration
*   **Target Total Claims**: 5,000
*   **Burst Pattern**: 50 Claims every 20 Seconds
*   **Warmpool Size**: 200 Replicas
*   **Cluster Size**: 100 Nodes (`load-test-pool-50`)
*   **Controller Settings**:
    *   `MaxConcurrentReconciles`: 100 (for Sandbox, SandboxClaim, and SandboxWarmPool)
    *   `client-go` QPS: 200
    *   `client-go` Burst: 300
    *   *Randomized claim selection enabled to prevent thundering herd deadlocks on scale-up.*

## Methodology
To accomplish this, we generated a procedural `clusterloader2` YAML that queues 100 individual phases.
Each phase drops exactly 50 `SandboxClaim` objects matching the Warmpool and then sleeps for 20 seconds. 

The test is actively executing on the cluster using the tuning configurations implemented earlier today. Preliminary checks confirmed zero `ImagePullBackOff` issues with the newly deployed `agent-sandbox-controller:tuned-v2` tag, and the pacing cleanly stepped up to 1,000 consecutive running pods without controller throttling.

## Preliminary Findings
*   The API Server connection is successfully handling the concurrency limits.
*   The randomized selection logic in `SandboxClaim` reconciliation ensures that 100 concurrent threads don't attempt to adopt the exact same Warmpool template.
*   We'll compile complete end-to-end latency metrics (Prometheus scraping) once the 5,000 mark is successfully processed (Est: ~35 mins execution time).
