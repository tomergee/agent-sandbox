# Walkthrough: CD Gradual Rollouts for Agent Sandbox

I have implemented and verified the plan for CD gradual rollouts in the `agent-sandbox` project.

## Changes Made

### CRD Schema Updates
*   Updated `[sandboxwarmpool_types.go](file:///usr/local/google/home/glottman/dev/agent-sandbox-argo/extensions/api/v1alpha1/sandboxwarmpool_types.go)` to include `CanarySpec` and `CanaryStatus`.
*   Ran `go generate ./...` to update generated clientsets and deepcopy functions.

### Controller Modifications
*   **WarmPool Controller**: Updated `[sandboxwarmpool_controller.go](file:///usr/local/google/home/glottman/dev/agent-sandbox-argo/extensions/controllers/sandboxwarmpool_controller.go)` to support proportional replenishment and target enforcement based on the canary percentage. Extracted `deleteExcessSandboxes` helper.
*   **Claim Controller**: Updated `[sandboxclaim_controller.go](file:///usr/local/google/home/glottman/dev/agent-sandbox-argo/extensions/controllers/sandboxclaim_controller.go)` to support randomized routing to canary sandboxes and loosened validation to allow binding V2 sandboxes when V1 was requested (Option A).

## Verification Results

### Unit Tests
*   All unit tests passed (220 passed). Fixed a build error in tests caused by signature changes.

### Cluster Verification (barkland-test)
1.  **Baseline**: Created a pool of 5 sandboxes with `v1` template. All became ready.
2.  **Canary Setup**: Created `v2` template and patched pool to 20% canary. The controller correctly deleted 1 V1 sandbox and created 1 V2 sandbox (`python-pool-27zdp`).
3.  **Claim Routing at 100%**: Patched pool to 100% canary. Created `python-claim-8` requesting `v1` template. The controller correctly bound it to the V2 sandbox `python-pool-27zdp`, verifying both the routing and the loosened validation.

## Conclusion
The system now supports gradual rollouts of sandbox templates driven by updates to the `SandboxWarmPool` CRD, fulfilling the goal of supporting CD workflows natively.

## PR Proposal

### Title
`feat: Support CD gradual rollouts in SandboxWarmPool and SandboxClaim`

### Description
This PR implements native support for gradual rollouts of sandbox templates. It allows platform engineers to define a canary template and a target percentage in the `SandboxWarmPool` CRD. The `SandboxWarmPool` controller maintains the pool with the correct ratio of primary and canary sandboxes. The `SandboxClaim` controller (Claim Allocator) routes incoming claims to the canary sandboxes based on the specified percentage, enabling transparent canary testing for agents.

Key changes:
*   Added `Canary` spec and status to `SandboxWarmPool` CRD in `extensions/api/v1alpha1/sandboxwarmpool_types.go`.
*   Updated `SandboxWarmPool` controller in `extensions/controllers/sandboxwarmpool_controller.go` for proportional replenishment and target enforcement.
*   Updated `SandboxClaim` controller in `extensions/controllers/sandboxclaim_controller.go` for weighted random routing and loosened validation.
*   Added Fast-Path fallback to ensure no latency regression when canary is not used.

### Motivation
To support Continuous Delivery (CD) workflows for AI agents, where new runtime environments need to be tested safely with a small percentage of traffic before full rollout.

### Testing Done
*   Unit tests passed (220 passed).
*   Manual verification on `barkland-test` cluster simulating rollouts via CRD patching.
