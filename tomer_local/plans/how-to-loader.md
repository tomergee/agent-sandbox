# How to Run ClusterLoader2 Tests

This document clearly outlines the end-to-end process of executing load tests on the Agent Sandbox controller using `clusterloader2`.

## 1. Prerequisites
Before executing any scripts, ensure the following are prepared:
- **ClusterLoader2 Built**: `clusterloader2` binary must be compiled and accessible in `../../perf-tests/clusterloader2/`.
- **Active GKE Cluster**: A Kubernetes cluster (e.g., GKE) must be running, with your `KUBECONFIG` correctly pointing to it.
- **Controller Deployed**: The `agent-sandbox` controller must be running in the cluster in the `agent-sandbox-system` namespace.

## 2. Test Execution Scripts
Load testing is orchestrated by wrapper scripts located in the `agent-sandbox/tomer_local/scripts` directory. The primary scripts and their purposes are:

- `run-load-test.sh [QPS] [REPLICAS]`: The standard, configurable test script for testing standard scale-up latency.
- `run-5k-load-test.sh [QPS]`: Tailored explicitly for 5,000 Sandbox rapid burst testing.
- `run-10k-rapid-test.sh` & `run-20k-load-test.sh`: Specific scripts for massive-scale load tests.
- `run-split-load-test.sh`: Designed for split or layered scale testing.

## 3. The Test Lifecycle Details
When you execute one of the `.sh` load test wrappers (like `run-load-test.sh`), the script automates the following sequence:

1. **Override Generation**: It generates a `testoverrides.json` file inside `tomer_local/scripts/` appending configurations like `CL2_QPS` and `CL2_REPLICAS`. This sets the runtime parameters for `clusterloader2`.
2. **Metrics Connection**: establishes a background `kubectl port-forward` to the `agent-sandbox-controller` pod on port `8080`. This local connection is necessary for scraping metrics later.
3. **Execution Monitoring**: Most scripts trigger a background monitoring loop via `kubectl get sandboxclaims`. It tracks the progression to `Ready` status and captures raw timestamps (Creation vs. Ready validation) to a `sandbox-startup-*.log` log file for debugging outlier latencies.
4. **ClusterLoader2 Execution**: `clusterloader2` CLI is called, pointing to test manifests located in the `agent-sandbox/dev/load-test/` directory (such as `agent-sandbox-warmpool-load-test.yaml`).
5. **Prometheus Scraping**: Upon test completion, `curl` targets `http://localhost:8080/metrics` to export the structured Prometheus metrics (like `sandbox_claim_ready_latency_seconds`).

## 4. Artifacts and Metrics Storage
All test artifacts, raw logs, and scraped metrics are saved securely without committing them to git up into the `tomer_local/logs/` directory.

Specific files you can expect to find post-test include:
- **`clusterloader2-[QPS]qps-[RUN_ID].log`**: The stdout text from the ClusterLoader2 runner itself.
- **`prom-metrics-[RUN_ID].txt`**: The scraped Prometheus metrics from the controller containing aggregated histogram data of creation latencies.
- **`sandbox-startup-[RUN_ID].log`**: Hand-collected pod-by-pod timelines.
- **`junit-[RUN_ID].xml`**: Standard XML file noting passing/failing statuses of clusterloader validations.
