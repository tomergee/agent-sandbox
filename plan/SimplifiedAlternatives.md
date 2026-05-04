# Simplified Canary Deployment Alternatives

This document outlines simplified methods for supporting Canary (gradual) rollouts for isolated sandbox environments **without modifying a single line of code in the core `agent-sandbox` controllers** (`SandboxClaim` and `SandboxWarmPool` Go reconcilers).

By leaving the core controllers alone, we shift the rollout and routing logic to the boundaries of the system: either the **Kubernetes API Server Interception Layer** (Webhooks) or the **Application/Client Layer** (SDK).

---

## Approach A: Mutating Admission Webhook (The Kubernetes-Native Approach)

Instead of having a single WarmPool that manages two different templates internally, this strategy utilizes two completely separate, independent `SandboxWarmPool` objects. The decision of which pool to route a claim to is made by intercepting the `SandboxClaim` creation request before it is persisted.

### How it Works

1.  **Deploy Two Pools**: The platform runs two concurrent WarmPools:
    *   `SandboxWarmPool`: `python-pool-v1` (Points to `template-v1`, stable version)
    *   `SandboxWarmPool`: `python-pool-v2` (Points to `template-v2`, canary version)
2.  **Deploy a Mutating Webhook**: A lightweight Mutating Admission Webhook is configured to listen for `CREATE` events of `SandboxClaim` resources.
3.  **Intercept & Mutate**: When a user sends a claim targeting `python-pool-v1`, the webhook intercepts the request:
    *   Rolls a dice based on the current rollout percentage (e.g., 20%).
    *   If selected for canary, the webhook patches the claim payload to point to `python-pool-v2`.
    *   If not selected, it forwards the claim unmodified.
4.  **Controller Processing**: The existing `SandboxClaim` controller receives the request (either mutated or original) and fulfills it against the requested pool using its existing high-performance, statically bound logic.

### Pros & Cons

**Pros:**
*   **Zero Core Controller Changes**: No code changes or deployments required for the `agent-sandbox` reconcilers.
*   **Transparent to Client**: The client (AI agents or backend applications) continues to ask for the stable pool without knowing a canary is taking place.
*   **Kubernetes Idiomatic**: Utilizes standard extension patterns provided by Kubernetes.

**Cons:**
*   **Infrastructure Overhead**: Requires building and running a small Webhook server and registering a `MutatingWebhookConfiguration`.
*   **Multi-Pool Management**: Operators must explicitly manage the lifecycle and counts of two separate WarmPools.

---

## Approach B: Client-Side / SDK Routing (The Application-Layer Approach)

This approach avoids all server-side infrastructure additions (no webhooks, no controller edits) by shifting the rollout decision directly into the application or the Python SDK creating the claims.

### How it Works

1.  **Deploy Two Pools**: Like Approach A, deploy two separate WarmPools (`python-pool-v1` and `python-pool-v2`).
2.  **SDK Wrapper**: Wrap the claim creation call in the Python client or application logic with a simple stochastic routing function:
    ```python
    import random

    def acquire_sandbox(client, canary_percentage=0.20):
        if random.random() < canary_percentage:
            # Route to canary version
            return client.create_sandbox(warmpool="python-pool-v2")
        else:
            # Route to stable version
            return client.create_sandbox(warmpool="python-pool-v1")
    ```
3.  **Test Execution**: The application natively targets the appropriate sandbox pool.

### Pros & Cons

**Pros:**
*   **Dead Simple**: Can be written in a few minutes in Python.
*   **Zero K8s Infrastructure work**: No new controllers, no webhooks, no CRD changes.

**Cons:**
*   **Leaky Abstraction**: Infrastructure routing concerns leak into application/agent code.
*   **Client Fragmentation**: Requires all consuming clients/services to update their SDK wrappers to participate in the canary rollout. If an application doesn't update, it won't participate.
