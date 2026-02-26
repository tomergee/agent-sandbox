# Sandbox Warm Pool Load Test Plan

## Goal Description
To test the performance improvements of using the `SandboxWarmPool` controller to pre-provision sandboxes vs creating them entirely from scratch on-demand via `SandboxClaim`.

## Implementation Details

### Configuration
1. The test uses a new clusterloader configuration: `agent-sandbox-warmpool-load-test.yaml`.
2. It first creates a `SandboxTemplate`.
3. It then creates a `SandboxWarmPool` targeting that template with a size matching the total number of claims we intend to make (100).
4. The test waits for the Warm Pool pods to become `Ready`.
5. It then blasts 100 `SandboxClaim` resources into the cluster.
6. We measure the exact same metrics: `PodStartupLatency` and our custom Prometheus `sandbox_claim_ready_latency_seconds`.

## Exact Execution Runbook

### 1. Build and Deploy Controller
Ensure your controller is running with the `--extensions=true` flag so the Warm Pool controller is active.

```bash
cd /usr/local/google/home/glottman/dev/jetski_main/agent-sandbox
kubectl apply -f release_assets/manifest.yaml
```

### 2. Run the Scale Test
Run the new performance test configuration:

```bash
cd /usr/local/google/home/glottman/dev/jetski_main/perf-tests/clusterloader2
kubectl delete namespace -l e2e-framework=clusterloader2 --wait=true

./clusterloader2 \
  --testconfig=../../agent-sandbox/dev/load-test/agent-sandbox-warmpool-load-test.yaml \
  --kubeconfig=$HOME/.kube/config \
  --provider=gke \
  2>&1 | tee clusterloader2-warmpool-run.log
```

### 3. Fetch the Metrics
```bash
# Run in background to access the endpoint (if not already running)
kubectl port-forward -n agent-sandbox-system statefulset/agent-sandbox-controller 8080:8080 > /dev/null 2>&1 &

# Scrape the metrics
curl -s http://localhost:8080/metrics | grep sandbox_claim > /usr/local/google/home/glottman/dev/jetski_main/agent-sandbox/tomer_local/logs/prom-metrics-warmpool.txt

# Save clusterloader artifacts
mv /usr/local/google/home/glottman/dev/jetski_main/perf-tests/clusterloader2/clusterloader2-warmpool-run.log /usr/local/google/home/glottman/dev/jetski_main/agent-sandbox/tomer_local/logs/
mv /usr/local/google/home/glottman/dev/jetski_main/perf-tests/clusterloader2/junit.xml /usr/local/google/home/glottman/dev/jetski_main/agent-sandbox/tomer_local/logs/junit-warmpool.xml
```
