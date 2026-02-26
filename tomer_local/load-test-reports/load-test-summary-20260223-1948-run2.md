# Agent Sandbox Load Test Summary (Run 2) - Sandbox Only

## Cluster Configuration
- **Cluster Name**: `agent-sandbox-load`
- **Provider**: Google Kubernetes Engine (GKE)
- **Node Configuration**: Scaled from 3 to 53 Nodes (Added 50 `e2-standard-8` nodes). This gives a total of 424 vCPUs and 1696 GB memory.
- **Zone**: `us-central1-c`
- **Kubernetes Client Version**: `v1.34.3`
- **Kubernetes Server Version**: `v1.34.3-gke.1245000`

## Workload Specification
The load test spawned 20 concurrent `Sandbox` custom resources. To purely test the `agent-sandbox` orchestration latency rather than cluster scheduling limits, a very lightweight pod spec was requested:
- **Image**: `alpine`
- **Command**: `["/bin/sh", "-c", "echo 'Hello from the Agent Sandbox!'; sleep 300"]`
- **QoS Class**: `BestEffort` (No explicit CPU or memory resource requests/limits were defined)

## Deployed Components
- **Agent Sandbox Controller Version**: `v0.1.1` (Image: `ghcr.io/kubernetes-sigs/agent-sandbox:latest`)
- **Extensions Installed**: Yes (`SandboxTemplate`, `SandboxClaim`, `SandboxWarmPool`)

## Test Execution Details
The test was executed using `clusterloader2` with the `agent-sandbox-load-test.yaml` test configuration.

**Command Run**:
```bash
./clusterloader2 \
  --testconfig=../../agent-sandbox/dev/load-test/agent-sandbox-load-test.yaml \
  --kubeconfig=$HOME/.kube/config \
  --provider=gke \
  2>&1 | tee clusterloader2-run2.log
```

## Results Summary
The test successfully ran through all phases without any failures or errors.

- **Total Execution Time**: 61.26 seconds
- **Test Results**: 0 Failures, 0 Errors

### Performance Metrics (from `junit.xml` and logs)
| Step | Phase Name | Execution Time |
|------|------------|----------------|
| 01 | Start Startup Measurement | 0.51s |
| 02 | Create Sandboxes | 2.09s |
| 03 | Wait for Sandboxes ready | 5.36s |
| 04 | Gather Results | 0.42s |
| 05 | Delete Sandboxes | 2.09s |

#### SandboxStartupLatency Distribution
The observed latency between Pod creation and the `Running` state during the load test (burst of 20 replicas):
- **P50 (Median)**: ~2.614s
- **P90**: ~3.097s
- **P99**: ~4.004s

> [!NOTE]
> This measures downstream Pod latency, rather than full end-to-end `Sandbox` controller lifecycle overhead.

The `agent-sandbox` controller performed efficiently, specifically spinning up the requested sandboxes and reaching a Ready state very quickly (~5.36s total wait time).
