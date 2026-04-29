# Canary Deployment Example for Agent Sandbox

This folder contains an example scenario for testing and demonstrating Canary deployments (gradual rollouts) using `SandboxWarmPool` and `SandboxClaim`.

## Scenario Overview

In this scenario, we simulate a platform operator rolling out a new version of a Python runtime sandbox (`v2`) while the stable version (`v1`) is already serving users.
We use the `SandboxWarmPool` to manage the ratio of pre-warmed sandboxes for both versions, and the `SandboxClaim` controller (Claim Allocator) to route claims to the appropriate version based on the configured percentage.

## Files Included

*   `sandbox-python-template-v1.yaml`: Defines the baseline `SandboxTemplate` (V1) using image `gcr.io/gke-ai-eco-dev/sandbox-runtime:latest`.
*   `sandbox-python-template-v2.yaml`: Defines the new `SandboxTemplate` (V2) using image `gcr.io/gke-ai-eco-dev/sandbox-runtime:v2`.
*   `sandbox-python-warmpool.yaml`: Defines the `SandboxWarmPool` that initially points to V1.
*   `sandbox-python-claims.yaml`: A file containing multiple claims to test distribution.
*   `tester.py`: A Python script to automate creating claims and verifying the distribution of V1 vs V2 sandboxes.

## Prerequisites

1.  The `agent-sandbox` controller with Canary support must be deployed in the cluster.
2.  The images `sandbox-runtime:latest` and `sandbox-runtime:v2` must be available in the registry accessible by the cluster.

## How to Run the Example

### 1. Setup Baseline (V1)

Apply the V1 template and the warm pool:

```bash
kubectl apply -f sandbox-python-template-v1.yaml
kubectl apply -f sandbox-python-warmpool.yaml
```

Verify that the pool is created and filled with V1 sandboxes:

```bash
kubectl get sandbox
```

### 2. Introduce V2 (Canary)

Apply the V2 template:

```bash
kubectl apply -f sandbox-python-template-v2.yaml
```

Patch the warm pool to set a canary percentage (e.g., 20%):

```bash
kubectl patch sandboxwarmpool python-pool --type=merge -p '{"spec":{"canary":{"sandboxTemplateRef":{"name":"sandbox-python-template-v2"},"percentage":20}}}'
```

Verify that the controller creates a canary sandbox and maintains the pool size.

### 3. Test Routing

You can use the provided `tester.py` script to create multiple claims and check which version they receive.

```bash
python3 tester.py
```

The script will output the distribution of V1 and V2 sandboxes assigned to the claims.

## Cleanup

To cleanup the resources created in this example:

```bash
kubectl delete sandboxwarmpool python-pool
kubectl delete sandboxtemplate sandbox-python-template-v1 sandbox-python-template-v2
kubectl delete sandboxclaim -l app=canary-test
```
