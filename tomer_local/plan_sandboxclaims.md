# Sandbox Claim Prometheus Metrics Implementation Plan

## Goal Description
To accurately measure `SandboxClaim` creation to ready latency without relying on `clusterloader2`'s `WaitForGenericK8sObjects` polling intervals, we will expose a standard Prometheus text metric on the controller's `:8080/metrics` endpoint. 

## Proposed Changes

### [NEW] `internal/metrics/prometheus.go`
- Define a Prometheus `HistogramVec` (e.g. `sandbox_claim_ready_latency_seconds`) registered with the `controller-runtime/pkg/metrics` global registry.

### [MODIFY] `extensions/controllers/sandboxclaim_controller.go`
- In `Reconcile`, track when a `SandboxClaim`'s `Status.Conditions` becomes `Ready=True` for the first time.
- Calculate the duration between `claim.CreationTimestamp.Time` and `time.Now()` (or `condition.LastTransitionTime`).
- Record the duration into the `sandbox_claim_ready_latency_seconds` histogram metric.
- Also track when the underlying `Sandbox` is provisioned successfully by the reconciler logic.
- Record the duration into the `sandbox_claim_created_latency_seconds` histogram metric to capture pure controller overhead without the pod scheduling delay.

## Verification Plan
1. Compile the controller locally `make build`.
2. Stop the remote controller `kubectl scale statefulset agent-sandbox-controller -n agent-sandbox-system --replicas=0`.
3. Run the local binary `./bin/manager --extensions=true`.
4. Apply a valid `SandboxTemplate` and `SandboxClaim`.
5. `curl localhost:8080/metrics | grep sandbox_claim_ready_latency_seconds` and verify the histogram buckets populate.
