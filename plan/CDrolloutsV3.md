# CD Gradual Rollouts for Agent Sandbox - CDrolloutsV3

## Suggested Strategy

To support CD gradual rollouts natively, the following strategy is suggested:

### 1. Pre-built Immutable Templates
By building custom sandbox templates containing base images, dependencies, and pre-running processes captured in a snapshot, your CI pipeline should automatically package new agent releases into immutable `SandboxTemplate` definitions. This ensures that when a sandbox is claimed, the environment is fully configured, pre-warmed, and ready for immediate execution.

### 2. Declarative Rollout Configuration via Argo Rollouts
Instead of manually tweaking infrastructure, Platform Engineers configure the rollout strategy declaratively using Argo Rollout and `AnalysisTemplate` manifests.
*   **Progressive Steps**: You define the traffic shifting steps (e.g., 5%, then 10%, 50%, and 100%) and the pause durations between them.
*   **Health Telemetry**: You define an `AnalysisTemplate` that queries Prometheus metrics. GKE provides default metrics like `sandbox_crash_rate`, `sandbox_claim_failure_rate`, and `sandbox_oom_kill_rate`, but you can also integrate custom metrics (e.g., hallucination rates) to continuously monitor the rollout's health.

### 3. Native "Smart Routing" via the Claim Allocator
This is the core innovation of the PRD that solves the challenge of scaling singleton, stateful workloads. Standard Kubernetes load balancers route network traffic across ReplicaSets, which does not work for isolated sandboxes.
*   Instead, the GKE Claim Allocator acts as a smart router in the background.
*   When an environment is requested, developers do not need to hardcode routing logic.
*   The Claim Allocator natively understands the template revisions and intercepts the claims, distributing the actual sandbox execution environments based on the weights defined in your Argo Rollout strategy. If the rollout is at the 5% step, exactly 5% of all new claims will be handed a sandbox from the new `SandboxTemplate`, while 95% receive the stable version.

### 4. Automated and Manual Rollbacks
Because AI code execution is unpredictable, safety mechanisms are built directly into this workflow:
*   **Auto-Rollback**: If the new sandbox version breaches the failure threshold defined in your `AnalysisTemplate` (e.g., the crash rate exceeds 10%), Argo Rollouts automatically aborts the update. The Claim Allocator instantly shifts 100% of incoming claims back to the stable sandbox version, ensuring zero downtime.

**Summary**: By integrating Argo Rollouts directly with the agent-sandbox Claim Allocator, you achieve a fully platform-managed CD pipeline. The developer simply commits a new agent image, the CI pipeline generates a new `SandboxTemplate`, and the platform seamlessly handles the progressive shifting of sandbox claims, health monitoring, and rollbacks without any manual infrastructure wrangling.

## Technical Proposal

### 1. CRD Schema Updates (`SandboxWarmPool`)
The `SandboxWarmPool` Custom Resource Definition must be extended to support a secondary canary template and track its telemetry.
*   **`spec.canary` Configuration**: A new block must be added to define the canary state, including a `sandboxTemplateRef` pointing to the new version (e.g., `template-v2`) and a `percentage` field dictating the traffic split (e.g., 5%).
*   **`status.canary` Telemetry**: The status subresource must be updated to track the rollout's health. This includes the current state (e.g., `Replenishing` or `AtTargetPercentage`), specific `failureRates` segmented by `PreClaim` and `PostClaim` events, and `canaryConditions` with timestamps to capture state change events.

### 2. WarmPool Controller Behavior Changes
The WarmPool controller, which previously reconciled a pool of sandboxes against a single template, must be modified to act as a proportional replenisher:
*   **Proportional Replenishment**: When a canary is configured, the controller must calculate replenishment batches based on the defined percentages. For example, if 100 sandboxes are needed and the canary is set to 5%, the controller will create 95 sandboxes from `template-v1` and 5 from `template-v2`.
*   **Target Enforcement**: The controller must continuously evaluate if the number of running canary sandboxes exceeds the target percentage. If it does, the controller is responsible for terminating the excess canary sandboxes to maintain the desired ratio.

### 3. Claim Allocator (`SandboxClaim` Controller) Changes
The Claim Allocator logic determines which pre-warmed sandbox is handed to an incoming `SandboxClaim`.
*   **Randomized Assignment**: Currently, claims are typically processed using a First-In-First-Out (FIFO) approach. To ensure canary sandboxes receive a proportional share of traffic for validation, the allocator must change to a randomized or round-robin assignment strategy. This guarantees that if 5% of the pool is running `template-v2`, approximately 5% of all new agent claims will be routed to those specific environments.

### 4. Operational Workflow & Platform Integration
By implementing these changes directly in the extension controllers, the operational burden on Agent Engineers is eliminated. The responsibility shifts to the platform or an automated GitOps tool (like Argo Rollouts):
*   **Monitoring and Adjustment**: The platform operator or Argo Rollouts controller monitors the `status.failureRates` (and external metrics like `sandbox_crash_rate`).
*   **Progressive Scaling**: To advance the rollout (e.g., from 5% to 10%), the platform simply patches the `SandboxWarmPool` CRD to increase the `canary.percentage`. The WarmPool controller will automatically reconcile the difference.
*   **Promotion**: Once the canary percentage reaches a safe threshold (e.g., 100%), the pool is patched to make `template-v2` the primary `spec.sandboxTemplateRef`, and the canary block is removed.

### 5. WarmPool Dynamics During Rollout

To understand how the warm pool behaves during a gradual rollout from `SandboxTemplateV1` to `SandboxTemplateV2` driven by Argo Rollouts:

1.  **Steady State (V1)**: Initially, the `SandboxWarmPool` is configured with `SandboxTemplateV1`. The pool is full of pre-warmed sandboxes running V1.
2.  **Rollout Initialization (e.g., 5% Canary)**:
    *   Argo Rollouts updates the `SandboxWarmPool` CRD, setting `spec.canary.sandboxTemplateRef` to `SandboxTemplateV2` and `spec.canary.percentage` to 5.
    *   The WarmPool controller calculates that it needs to maintain a 5% ratio of V2 sandboxes in the available pool.
    *   It immediately spins up new sandboxes using `SandboxTemplateV2` to reach the 5% target.
3.  **Traffic Shifting via Claims**:
    *   As clients claim sandboxes, the Claim Allocator routes approximately 5% of these claims to the V2 sandboxes (using the randomized or round-robin strategy).
    *   When a V1 sandbox is claimed and consumed, the WarmPool controller replenishes it. To maintain the ratio, it will replenish with V2 until the 5% target is met, and then with V1.
4.  **Progressive Steps (e.g., 50% Canary)**:
    *   As Argo Rollouts advances the step to 50%, the controller adjusts its replenishment logic to maintain a 50/50 split of *available* sandboxes.
    *   More V2 sandboxes are created as V1s are consumed.
5.  **Completion (100%)**:
    *   When the rollout reaches 100%, all new sandboxes are created from `SandboxTemplateV2`.
    *   Any remaining V1 sandboxes are eventually claimed or can be garbage collected by the controller if target enforcement is applied to clear out the old version.

## Example Workflow: Gradual Rollout and Verification

This section demonstrates how a rollout works in practice using the Python runtime example and how to verify it using Argo Rollouts.

### Phase 1: Baseline (V1)
You have an existing `SandboxTemplate` named `sandbox-python-template-v1`.
The `SandboxWarmPool` is configured to use this template:

```yaml
apiVersion: extensions.agents.x-k8s.io/v1alpha1
kind: SandboxWarmPool
metadata:
  name: python-pool
spec:
  sandboxTemplateRef: sandbox-python-template-v1
  size: 100
```

### Phase 2: Introducing V2 (Canary)
You want to deploy a new version with a new image.
1.  **Create Template V2**: You create a new `SandboxTemplate` resource with a unique name:
    ```yaml
    apiVersion: extensions.agents.x-k8s.io/v1alpha1
    kind: SandboxTemplate
    metadata:
      name: sandbox-python-template-v2
    spec:
      podTemplate:
        metadata:
          labels:
            sandbox: my-python-sandbox
        spec:
          containers:
          - name: python-sandbox
            image: sandbox-runtime:v2 # New version
            ports:
            - containerPort: 8888
    ```
2.  **Configure Rollout**: Argo Rollouts patches the `SandboxWarmPool` to start the canary rollout at 5%:
    ```yaml
    spec:
      sandboxTemplateRef: sandbox-python-template-v1
      canary:
        sandboxTemplateRef: sandbox-python-template-v2
        percentage: 5
    ```

### Phase 3: Verification via Argo Rollouts
To verify that the rollout is proceeding correctly, users should use the specialized Argo Rollouts tools:

*   **Argo Rollouts CLI Plugin**: Run the following command to see a real-time, interactive tree view of the rollout:
    ```bash
    kubectl argo rollouts get rollout <rollout-name>
    ```
*   **Argo Rollouts Dashboard**: To view it in a web browser with a graphical interface, run:
    ```bash
    kubectl argo rollouts dashboard
    ```
*   **Verifying the Warm Pool (Standard kubectl)**: You can verify that the WarmPool controller is maintaining the correct ratio of sandboxes by inspecting the custom resource directly:
    ```bash
    kubectl get sandboxwarmpool python-pool -o yaml
    ```
    The status will show the actual counts for both primary and canary sandboxes, confirming that ~5% are running V2.

## Implementation Plan

This section outlines the code changes required to implement the CD gradual rollouts.

### Step 1: CRD Schema Updates
**File**: `[sandboxwarmpool_types.go](file:///usr/local/google/home/glottman/dev/agent-sandbox-argo/extensions/api/v1alpha1/sandboxwarmpool_types.go)`
*   **Add `Canary` spec**: Define `CanarySpec` struct with `SandboxTemplateRef` and `Percentage` fields. Add it to `SandboxWarmPoolSpec`.
*   **Add `Canary` status**: Define `CanaryStatus` struct with `State`, `FailureRates`, and `Conditions`. Add it to `SandboxWarmPoolStatus`.
*   Run `make manifests` to regenerate CRD YAMLs.

### Step 2: WarmPool Controller Modifications
**File**: `[sandboxwarmpool_controller.go](file:///usr/local/google/home/glottman/dev/agent-sandbox-argo/extensions/controllers/sandboxwarmpool_controller.go)`
*   **Reconciliation Loop**: Update the reconciliation logic to read the `canary` spec.
*   **Proportional Replenishment**: Modify the logic that creates new sandboxes to maintain the ratio of primary vs canary sandboxes based on the target percentage.
*   **Target Enforcement**: Add logic to terminate excess *available* canary sandboxes if the percentage decreases (ensuring we don't terminate claimed sandboxes).

### Step 3: Claim Allocator (`SandboxClaim` Controller) Modifications
**File**: `[sandboxclaim_controller.go](file:///usr/local/google/home/glottman/dev/agent-sandbox-argo/extensions/controllers/sandboxclaim_controller.go)`
*   **Routing Logic**: Update the logic that selects a sandbox from the warm pool.
*   **Randomized/Round-Robin Assignment**: Implement a strategy to distribute claims between primary and canary sandboxes according to the weights.
*   **Loosened Validation (Option A)**: Ensure the controller allows binding a canary sandbox even if the claim requested the primary template, as long as they belong to the same rollout pool.
*   **Fast-Path Fallback**: If `spec.canary` is not set in the `SandboxWarmPool`, the controller must bypass all canary routing logic and use the existing high-performance allocation path to ensure zero latency regression for normal operations.

### Step 4: Verification and Testing
*   Create unit tests in `sandboxwarmpool_controller_test.go` and `sandboxclaim_controller_test.go` to verify the new logic.
*   Create an E2E test scenario simulating a rollout and verifying traffic split.

## Testing Plan: Simulating Argo Rollouts

To verify the system behavior without requiring a full Argo Rollouts installation in automated tests, we can simulate the Argo Rollouts controller by manually patching the `SandboxWarmPool` CRD.

### Scenario 1: Proportional Replenishment and Routing
1.  **Setup**: Create a `SandboxWarmPool` with size 10 and `SandboxTemplateV1`. Wait for it to be full (10 sandboxes of V1).
2.  **Simulate Canary Start (10%)**: Patch the pool to add `v2` at 10%:
    ```bash
    kubectl patch sandboxwarmpool python-pool --type=merge -p '{"spec":{"canary":{"sandboxTemplateRef":"sandbox-python-template-v2","percentage":10}}}'
    ```
3.  **Verify WarmPool**: Check that the controller creates 1 sandbox of V2 (10% of 10).
4.  **Verify Routing**: Create 10 `SandboxClaim`s. Verify that approximately 1 of them gets bound to the V2 sandbox.
5.  **Simulate Rollout Advance (50%)**: Patch the pool to 50%:
    ```bash
    kubectl patch sandboxwarmpool python-pool --type=merge -p '{"spec":{"canary":{"percentage":50}}}'
    ```
6.  **Verify WarmPool**: Check that the controller replenishes the pool to maintain a 50/50 split as claims are consumed.

### Scenario 2: Rollback Simulation
1.  **Setup**: Start with a 50/50 split.
2.  **Simulate Abort/Rollback**: Argo Rollouts would abort by removing the canary or setting percentage to 0. Patch the pool to remove canary:
    ```bash
    kubectl patch sandboxwarmpool python-pool --type=json -p '[{"op": "remove", "path": "/spec/canary"}]'
    ```
3.  **Verify**: Check that the Claim Allocator immediately stops routing to V2 sandboxes and only uses V1. Check that the WarmPool controller starts replacing V2 sandboxes with V1 as they are consumed.

### Scenario 3: Fast-Path Validation
1.  **Setup**: Pool with no `canary` configured.
2.  **Test**: Run a high-concurrency load test creating claims.
3.  **Verify**: Measure latency and ensure it matches baseline performance before these changes were introduced.
