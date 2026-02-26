# Agent Sandbox Load Testing Plan

## Prerequisites Configuration
1. **GCP Project**: Set active gcloud project to `gke-ai-eco-dev`.
2. **GKE Cluster**: A new standard GKE cluster with `e2-standard-8` nodes.
3. **Load Testing Tool**: `kubernetes/perf-tests` cloned as a sibling to `agent-sandbox` for `clusterloader2`.

## Execution Steps
1. **Set Project**: Run `gcloud config set project gke-ai-eco-dev`.
2. **Create Cluster**: Run `gcloud container clusters create agent-sandbox-load-test-cluster --project=gke-ai-eco-dev --machine-type=e2-standard-8 --num-nodes=3 --zone=us-central1-c`.
3. **Get Credentials**: Run `gcloud container clusters get-credentials agent-sandbox-load-test-cluster --zone=us-central1-c --project=gke-ai-eco-dev`.
4. **Deploy Agent Sandbox**: Apply the controller to the cluster from `/usr/local/google/home/glottman/dev/jetski_main/agent-sandbox` (e.g. `make deploy IMG=ghcr.io/kubernetes-sigs/agent-sandbox:latest` or via kustomize).
5. **Clone Perf-Tests**: Clone `https://github.com/kubernetes/perf-tests.git` into `/usr/local/google/home/glottman/dev/jetski_main/perf-tests`.
6. **Build Clusterloader2**: Enter `perf-tests/clusterloader2` and run `go build -o clusterloader2 ./cmd/clusterloader.go`.
7. **Run Load Test**: Execute the load test against the load-test config:
   ```bash
   ./clusterloader2 \
     --testconfig=../../agent-sandbox/dev/load-test/agent-sandbox-load-test.yaml \
     --kubeconfig=$HOME/.kube/config \
     --provider=gke
   ```
8. **Save Artifacts**: Copy `junit.xml` to `/usr/local/google/home/glottman/dev/jetski_main/agent-sandbox/tomer_local` along with any logs.

## Sandbox Claim Only Test
This category of tests will evaluate the full controller and extension lifecycle by deploying `SandboxClaim` resources instead of bare `Sandbox` resources.
**Specifications**:
- Must install the sandbox extensions to the cluster.
- Load test must request `SandboxClaim` objects instead of `Sandbox`.
- Must measure the `PodStartupLatency` (when the underlying Pod reaches Running state).
- Must measure the `SandboxClaimReadyLatency` (when the `SandboxClaim` reaches the Ready state).
- Must measure the latency difference between the two to evaluate extension controller overhead.
