# PR Description: Native Canary Gradual Rollouts for Sandbox Environments

## Summary
This Pull Request introduces native support for Continuous Delivery (CD) gradual rollouts (Canaries) for isolated AI agent sandbox environments. Traditional Kubernetes canary tools rely on routing L4/L7 network traffic across service endpoints, which fails for stateful, single-user sandbox execution environments. 

This enhancement shifts the "routing" paradigm to the **allocation layer**. By enhancing the `SandboxWarmPool` and `SandboxClaim` controllers, we enable platform engineers to declaratively shift a percentage of users to a new canary runtime without requiring the user to modify their code or request parameters.

## Architectural Deep Dive

### Existing Foundation
Prior to this change, the `agent-sandbox` framework used `templateRef` as a 1:1 statically-bound field:
1.  **`SandboxWarmPool.spec.sandboxTemplateRef`**: Instructed the warmpool exactly which template to pre-warm.
2.  **`SandboxClaim.spec.sandboxTemplateRef`**: Specified the exact template a user needed.
3.  **Controllers**: Strictly enforced equality between these two fields, ensuring a claim only ever received a sandbox spawned from the exact requested template.

### What was Added (The Canary Architecture)

To enable non-disruptive concurrent rollout testing, we introduced the capability to support multiple concurrent template streams within a single conceptual Pool.

#### 1. CRD Schema Updates (`SandboxWarmPool`)
*   **Added `spec.canary`**: Introduces a conditional block allowing a second `sandboxTemplateRef` (e.g., `v2`) running alongside the primary, paired with a mandatory `percentage` integer field (0-100).
*   **Added `status.canary`**: Introduces sub-resource fields to track real-time telemetry including current rollout `State`, error segmentations, and historical `Conditions` allowing external GitOps engines (like Argo Rollouts) to query the health of the transition.

#### 2. WarmPool Controller Modifications
*   **Proportional Replenishment**: The controller logic was refactored from a single "size count" loop to a dual-channel algorithm. It resolves the mathematical floor/ceiling of `desiredReplicas * percentage` for the canary, maintaining that specific concurrency separate from the stable replicas.
*   **Target Enforcement**: Active enforcement monitors pool churn. If the percentage decreases (or an emergency rollback removes the canary block), the controller actively garbage collects *available* (unclaimed) excess canary sandboxes, returning total concurrency to the primary pool.
*   **Functional Reusability**: Refactored `fetchTemplateAndHash` and `createPoolSandbox` to be parameterized by template name instead of static references.

#### 3. Claim Allocator (`SandboxClaim` Controller) Modifications
This is the "Smart Router" that makes the rollout possible.
*   **Loosened Validation (Transparency)**: Modified `verifySandboxCandidate` to decouple the validation from the *requested* claim template. It now validates the candidate sandbox against the *effective* selected hash rather than the user-requested hash. This means a user requesting `v1` can transparently adopt a `v2` without breaking K8s admission.
*   **Weighted Stochastic Routing**: When a user requests an active rollout pool, the Claim Allocator calculates a deterministic weighted roll (`rand.Intn(100) < targetPercentage`). If triggered, the controller dynamically overrides the internal hash lookup, extracting an idle canary sandbox from the queue.
*   **Fast-Path Fallback**: Crucially, if no `spec.canary` block is present on the WarmPool, the controller instantly short-circuits the fetch logic, hitting the highly-optimized legacy direct lookup cache, guaranteeing absolutely zero added latency during non-rollout hours.

---

## New Operational Workflow: How to do Canary Rollouts Now

Now that this feature is active, operators roll out new environments using the following declarative lifecycle:

### Step 1: Establish Immutable Base
Maintain your stable pool normally.
```yaml
apiVersion: extensions.agents.x-k8s.io/v1alpha1
kind: SandboxWarmPool
metadata:
  name: standard-py-pool
spec:
  sandboxTemplateRef:
    name: py-runtime-v1
  replicas: 50
```

### Step 2: Deploy the New Candidate
Create a new, immutable `SandboxTemplate` (e.g., built by your CI pipeline). **Do not touch the V1 template.**
```yaml
kind: SandboxTemplate
metadata:
  name: py-runtime-v2
spec:
  podTemplate:
    spec:
      containers:
        - image: my-registry.io/sandbox:v2 # The new version
```

### Step 3: Initialize Canary Split
Patch the existing pool resource. The WarmPool controller will background-provision the new ratio.
```yaml
spec:
  sandboxTemplateRef:
    name: py-runtime-v1
  canary:
    sandboxTemplateRef:
      name: py-runtime-v2
    percentage: 10 # Begin serving 10% of new claims to V2
```

### Step 4: Progressive Advancement
Advance the percentage incrementally (manually or automated via Argo Rollout hooks).
```bash
kubectl patch sandboxwarmpool standard-py-pool --type=merge -p '{"spec":{"canary":{"percentage":50}}}'
```

### Step 5: Promotion / Cutover
Once confidence is achieved, complete the operation by making V2 the primary and deleting the canary block.
```yaml
spec:
  sandboxTemplateRef:
    name: py-runtime-v2 # Primary is now V2
  # Canary block removed
```

### Emergency Rollback
If health metrics fail, immediately remove the `canary` block via GitOps. The system immediately redirects 100% of new user claims back to the remaining V1 warm pool without any downtime.
