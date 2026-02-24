# Sandbox Claim Load Test Implementation Plan & Runbook

## Goal Description
The goal is to load test the `agent-sandbox` controller by creating `SandboxClaim` resources on a remote GKE cluster. The procedure measures both the standard `PodStartupLatency` metric using `clusterloader2` and the exact `SandboxClaimReadyLatency` using a custom Prometheus metric exported by the controller tracking the duration until `status.conditions.Ready=True`.

## Implementation Details

### Prometheus Metrics
- Two key Prometheus `HistogramVec` metrics are registered tracking latency:
  1. `sandbox_claim_ready_latency_seconds`: The duration until `status.conditions.Ready=True`.
  2. `sandbox_claim_created_latency_seconds`: The duration from `CreationTimestamp` to when the controller actually provisions the underlying `Sandbox` object.
- The `SandboxClaim` reconciler records these durations natively, bypassing `clusterloader2`'s polling overhead.
- Note: High latency here during massive bursts (e.g. 100 QPS) is often due to the `controller-runtime` default `MaxConcurrentReconciles=1` queue limit.

## Exact Execution Runbook for AI Agents
To execute this scale test from scratch, follow these exact terminal commands.

### 1. Build and Deploy Controller
Ensure your `gcloud` project is set accurately (e.g. `gke-ai-eco-dev`) to allow docker pushes to GCR.

```bash
# Set project context if necessary
gcloud config set project gke-ai-eco-dev

# Build and push the controller image with metrics support
cd /usr/local/google/home/glottman/dev/jetski_main/agent-sandbox
export IMG=gcr.io/gke-ai-eco-dev/agent-sandbox-controller:v0.0.1-perf
make docker-build docker-push IMG=$IMG
```

### 2. Configure the Manifest
Ensure the `agent-sandbox-controller` pod in `/usr/local/google/home/glottman/dev/jetski_main/agent-sandbox/release_assets/manifest.yaml` contains:
1. The pushed test image `gcr.io/gke-ai-eco-dev/agent-sandbox-controller:v0.0.1-perf`.
2. The argument `--extensions=true` (CRITICAL for SandboxClaims to process properly; clusterloader test will time out with "context deadline exceeded" otherwise).
3. The metrics port mapping for port `8080`.

```bash
cd /usr/local/google/home/glottman/dev/jetski_main/agent-sandbox
kubectl apply -f release_assets/manifest.yaml
```

### 3. Run the Scale Test (ClusterLoader2)
Run the performance test with the dedicated 50 QPS configuration file. Make sure any previous load test namespaces are cleaned up.

```bash
cd /usr/local/google/home/glottman/dev/jetski_main/perf-tests/clusterloader2
kubectl delete namespace -l e2e-framework=clusterloader2 --wait=true

./clusterloader2 \
  --testconfig=../../agent-sandbox/dev/load-test/agent-sandbox-claim-load-test.yaml \
  --kubeconfig=$HOME/.kube/config \
  --provider=gke \
  2>&1 | tee clusterloader2-50qps-prom-run.log
```
The test will generate JUnit results in `/usr/local/google/home/glottman/dev/jetski_main/perf-tests/clusterloader2/junit.xml`. The `PodStartupLatency` metrics will be printed natively in the console log output stream.

### 4. Fetch the Prometheus Latency Data and Save Logs
After the load test completes, port-forward the controller locally to curl the custom metrics:

```bash
# Run in background to access the endpoint
kubectl port-forward -n agent-sandbox-system statefulset/agent-sandbox-controller 8080:8080 > /dev/null 2>&1 &

# Scrape the metrics specific to the SandboxClaim readiness threshold
mkdir -p /usr/local/google/home/glottman/dev/jetski_main/agent-sandbox/tomer_local/logs
curl -s http://localhost:8080/metrics | grep sandbox_claim_ready_latency_seconds > /usr/local/google/home/glottman/dev/jetski_main/agent-sandbox/tomer_local/logs/prom-metrics.txt

# Move the clusterloader artifacts into the logs directory
mv /usr/local/google/home/glottman/dev/jetski_main/perf-tests/clusterloader2/clusterloader2-50qps-prom-run.log /usr/local/google/home/glottman/dev/jetski_main/agent-sandbox/tomer_local/logs/
mv /usr/local/google/home/glottman/dev/jetski_main/perf-tests/clusterloader2/junit.xml /usr/local/google/home/glottman/dev/jetski_main/agent-sandbox/tomer_local/logs/
```

Use the `sandbox_claim_ready_latency_seconds_sum` and `sandbox_claim_ready_latency_seconds_count` fields from the output to determine the exact average controller processing latency in seconds mathematically: average = sum / count.
