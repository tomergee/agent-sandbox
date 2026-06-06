# Sandbox Lifecycle Daemon & Wakeup Proxy Plan

To solve the problem of client-driven sandbox lifecycle management leaking resources, we propose a server-side architecture comprising a **Lifecycle Daemon** (Instance Manager) and a **Lightweight Proxy**.

This design is fully aligned with Agent Sandbox's core `v1beta1` primitives (such as `operatingMode: Suspended`) and extensions (`SandboxClaim`, `SandboxWarmPool` leasing).

---

## 1. Sandbox Lifecycle Daemon (Instance Manager)

The Lifecycle Daemon is a namespace-scoped controller that acts as the authoritative server-side supervisor for active Sandbox resources.

### Key Responsibilities
1. **Enforcing TTL & Max Idle Time**:
   - Reads Sandbox metadata annotations (`agents.x-k8s.io/ttl` or `agents.x-k8s.io/max-idle-time`).
   - Automatically initiates a clean shutdown/suspend sequence if a Sandbox is idle past its maximum allowed idle time.
2. **Enforcing Administrative Suspension**:
   - To suspend a sandbox, the Daemon updates the Sandbox CRD:
     ```yaml
     spec:
       operatingMode: Suspended
     ```
   - The core Sandbox controller terminates the underlying Pod while preserving Sandbox metadata.
3. **Snapshot Orchestrator**:
   - Prior to setting `operatingMode: Suspended`, the Daemon triggers a memory and volume snapshot (see [checkpoint_restore_workflow.md](checkpoint_restore_workflow.md)).
4. **Wakeup & Warm Lease Orchestrator**:
   - When waking up a sandbox, the Daemon leverages the `SandboxClaim` lease flow:
     - It submits a `SandboxClaim` targeting the `SandboxWarmPool`.
     - It performs optimistic locking by setting the annotation:
       ```yaml
       metadata:
         annotations:
           agents.x-k8s.io/sandbox-name: "openclaw-sandbox-user1"
       ```
     - Once the claim reconciles and completes adoption, the Daemon updates the Sandbox to `operatingMode: Running`.

---

## 2. Lightweight Wakeup Proxy (Traffic Broker)

WebSocket and HTTP traffic must wake the Sandbox when scaled to zero. A lightweight proxy acts as an always-on frontend broker.

```
Client Connection (HTTP/WS)
          │
          ▼
┌──────────────────────────────────────────────────┐
│            Lightweight Traffic Proxy             │
│                                                  │
│  1. Buffers HTTP Request / WS Handshake          │
│  2. Sends Wake Signal to Lifecycle Daemon        │
│  3. Polls Sandbox readiness (Ready condition)    │
└────────────────────────┬─────────────────────────┘
                         │
                         ▼ (Pod becomes Ready)
┌──────────────────────────────────────────────────┐
│         OpenClaw Gateway (Sandbox Pod)          │
│  Fulfills buffered request / handles WS stream   │
└──────────────────────────────────────────────────┘
```

### How it Works
1. **Client Traffic**: The client attempts to connect to `openclaw-sandbox-user1.cluster.local`.
2. **Buffering**: The proxy holds the HTTP connection open or buffers the incoming WebSocket handshake request.
3. **Wakeup Call**: The proxy triggers the Lifecycle Daemon to transition `operatingMode` to `Running`.
4. **Readiness Polling**: The proxy polls the Sandbox status conditions, waiting for `Ready: True` on port `18789`.
5. **Replay/Proxy**: The proxy forwards the buffered connection to the OpenClaw container, allowing transparent communication.

---

## 3. Wakeup Time Feature Implementation

The Sandbox controller itself will support scheduled warmup:
1. **Spec Addition**: Sandbox lifecycle specs support a scheduled expiration/shutdown via `shutdownTime`. We will add a corresponding `spec.lifecycle.wakeupTime` timestamp.
2. **Warm Pre-Warming**: If `now >= wakeupTime`, the Lifecycle Daemon automatically adopts a warm instance from the pool and transitions `operatingMode` to `Running`, ensuring the container is restored and warm before scheduled cron or user executions begin.
