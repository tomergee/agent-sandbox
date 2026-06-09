# Master Plan: Generic Claw Agent Sandbox Integration

This document outlines the master architecture and implementation roadmap to integrate any **claw-type agent manager** (e.g., OpenClaw, Hermes, NemoClaw) with **Agent Sandbox** using Kubernetes-managed Suspend/Resume states and dynamic cron pre-wakeups.

To ensure stable code quality and clean reviews, the implementation is broken down into **5 separate PR-sized components (L to XXL)**.

---

## 🏗️ Architecture Design (Generic Abstract Interface)

```
        Traffic (HTTP/WS) / Scheduled Crons
                      │
                      ▼
┌──────────────────────────────────────────┐
│      Lightweight Traffic Proxy           │ (Always-on Proxy)
│  - Intercepts and buffers connection     │
│  - Sends resume signal to Daemon         │
└─────────────────────┬────────────────────┘
                      │
                      ▼ (Resume Webhook)
┌──────────────────────────────────────────┐
│       Sandbox Lifecycle Daemon           │ (Go Daemon)
│  - Patches Sandbox Spec.OperatingMode    │
│  - Queries Agent Manager for Idle/Cron   │
└─────────────────────┬────────────────────┘
                      │
                      ▼ (K8s API)
┌──────────────────────────────────────────┐
│       GKE Sandbox Controller             │ (Go Controller)
│  - Scales Pod to 0 (Suspended)           │
│  - Restores Pod + PVC (Running)          │
└─────────────────────┬────────────────────┘
                      │
                      ▼ (Restored Pod)
┌──────────────────────────────────────────┐
│  Generic Agent Manager Container         │ (OpenClaw / Hermes)
│  - Node/Python Gateway                   │
│  - Persistent /root/.openclaw PVC       │
└──────────────────────────────────────────┘
```

## 📅 PR Breakdown & Scope

### PR 1: Propagate OperatingMode to SandboxClaim [Size: L]
* **Objective**: Add `operatingMode` control to the `SandboxClaim` CRD and update the claim controller to mirror it to the adopted Sandbox.
* **Deliverables**:
  - Add `spec.operatingMode` (Running, Suspended) to the `SandboxClaimSpec` in `extensions/api/v1beta1/sandboxclaim_types.go`.
  - Update `extensions/controllers/sandboxclaim_controller.go` to watch the claim's `spec.operatingMode` and automatically patch the underlying adopted `Sandbox` resource with the matching operatingMode.
  - Regenerate client deepcopy, listers, and typed Go clientsets (`clients/k8s/`).

> [!NOTE]
> **Architectural Rationale**: In a multi-tenant GKE cluster, client applications and SDKs only have RBAC access to namespaced tenant resources like `SandboxClaim`. They do not (and should not) have permissions to read or modify the underlying GKE-managed `Sandbox` resources directly. By exposing `spec.operatingMode` on the `SandboxClaim`, we ensure that tenants can trigger suspend/resume lifecycle states directly through their claimed namespace boundary.

---

### PR 2: Generic Lifecycle Daemon (Go/gRPC/HTTP Server) [Size: XL]
* **Objective**: Build the server-side namespace supervisor that maps lifecycle HTTP calls to Kubernetes client-go patches.
* **Deliverables**:
  - Implement the daemon binary under `cmd/sandbox-lifecycle-daemon/main.go`.
  - Expose API endpoints:
    - `POST /v1/sandbox/suspend`: Patches `spec.operatingMode = "Suspended"` on the target sandbox.
    - `POST /v1/sandbox/resume`: Patches `spec.operatingMode = "Running"` (or leases a pod from the WarmPool).
    - `GET /v1/sandbox/status`: Checks Pod status and reports whether the sandbox is ready, suspended, or provisioning.
  - **Generic Back-Channel Webhook Interface**: 
    - Queries the agent manager container's health endpoint (e.g. `GET /api/v1/lifecycle/status`) to check if there are running tasks or active WebSocket sessions before suspending.
  - **Wakeup Scheduler Loop (Scheduled Cron Wakeup)**:
    - Runs a background goroutine loop checking all suspended sandboxes.
    - If `agents.x-k8s.io/next-wakeup` annotation exists and its RFC3339 time is reached, patches the sandbox back to `Running` (waking it up) and clears the annotation.

---

### PR 3: Wake-on-Traffic Buffering in Sandbox Router [Size: L]
* **Objective**: Enhance the existing `sandbox-router` component to support buffering of connection requests and automated wakeup triggering for suspended Sandboxes.
* **Deliverables**:
  - Update the `sandbox-router` codebase (or add a middleware layer) to detect when a target Sandbox is suspended.
  - **Buffering & Replay Engine**: 
    - Upon receiving a client HTTP request or WebSocket connection request for an offline/suspended Sandbox, intercept the request and hold it open.
    - Send an asynchronous wake-up HTTP POST call to the **Sandbox Lifecycle Daemon** (`POST /v1/sandbox/resume`).
    - Monitor GKE (using informers/watchers or a status query loop) until the Sandbox status Ready condition becomes `True`.
    - Once ready, forward the buffered request and establish TCP streaming to the restored pod.

> [!TIP]
> **Implementation Alternative (Go Rewrite)**: While the existing `sandbox-router` is written in Python, we should consider implementing/rewriting a Go-based version of the router. This aligns directly with the official repository roadmap (`roadmap.md`: "Support the sandbox-router as a first-class citizen within the project written in Go"), provides better performance and lower resource overhead for request proxying/buffering, and enables sharing client-go informers and controllers packages directly.

---

### PR 4: Dynamic Pre-Wakeup Cron Trigger Controller (Go) [Size: L]
* **Objective**: Implement a cluster-wide scheduler controller that handles dynamic wakeup alarms for suspended containers.
* **Deliverables**:
  - Build a custom controller in the controller manager that watches `Sandbox` resources.
  - **Trigger Calculation**:
    - Queries OpenClaw's `/api/v1/cron/next` endpoint right before suspension.
    - Saves the timestamp exactly **2 minutes prior** to the next scheduled run in the annotation `agents.x-k8s.io/next-wakeup`.
  - **Alarm Scheduler**:
    - The controller monitors this annotation and schedules a lightweight, temporary Kubernetes Job or GKE timer.
    - When the alarm fires, the Job calls `/resume` to ensure the container is fully running and warm when the internal cron trigger fires.

---

### PR 5: `k8s-agent-sandbox-js` (Universal JS/TS Client SDK) [Size: XXL]
* **Objective**: Provide a production-ready, hand-written TypeScript client SDK to align Node.js-based agent managers with GKE Sandboxes.
* **Deliverables**:
  - Build connection tunneling, command execution, and port-forwarding modules using Node.js stream multiplexing.
  - Expose standard client APIs matching Go and Python SDK interfaces.
  - Provide a lifecycle adapter hook to expose the agent manager's state (`idle` status, active runs count, next cron scheduled runs) to the Lifecycle Daemon.
