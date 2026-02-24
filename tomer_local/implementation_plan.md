# 20,000 Sandbox Burst Load Test Plan

## Objective
Execute an extreme-scale load test that continuously bursts 100 `SandboxClaim` objects every 20 seconds, reaching a final state of 20,000 concurrent sandboxes in the cluster. The Warm Pool will be initialized with a baseline capacity of 1,000.

## Proposed Changes

1. **Load Test Configuration**: 
   Since ClusterLoader2 requires explicitly defined phases in its YAML configuration, I will write a simple Python script (`generate-20k-burst-yaml.py`) to procedurally generate a new manifest: `dev/load-test/agent-sandbox-20k-burst-load-test.yaml`.
   - **Warm Pool Size**: 1000
   - **Burst Pattern**: 200 sequential phases 
     - *Phase N Create*: Create 100 claims at 100 QPS (Cumulative total: N*100)
     - *Phase N Wait*: Wait for N*100 claims to be Ready.
     - *Phase N Pause*: Wait 20 seconds.

2. **Wrapper Script**:
   Create a new wrapper script `tomer_local/run-20k-load-test.sh` that sets the proper overrides `CL2_WARMPOOL_SIZE=1000` and points to the newly generated 20k burst YAML file.

3. **Report Generation**:
   After the run completes, I will extract the Prometheus metrics and the `junit.xml` XML outputs to calculate the `P50`, `P90`, and `P99` percentiles. I will write a custom Python parser to split the results of all 200 bursts and render them efficiently into the final Markdown report tables.

## User Review Required

> [!CAUTION]
> Creating 20,000 sandboxes means scheduling **20,000 pods** across the 480 nodes. At ~41 pods per node, this comfortably fits within the 110-pod limit of standard GKE clusters, assuming these are lightweight Alpine containers without CPU/Memory limits. Are you ready to proceed with generating the files and running this test?
