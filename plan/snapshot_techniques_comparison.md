# Sandbox State Persistence: Techniques Comparison

To implement scale-to-zero and suspend/resume mechanics for the **OpenClaw** sandbox workloads, the **Agent Sandbox** architecture offers three distinct persistence techniques. Selecting the right technique depends on whether you need to preserve raw disk files, running process memory, or speed up cold-start provisioning.

---

## Overview of the Three Persistence Techniques

| Feature / Metric | 1. GKE Pod Snapshot Controller (CRIU) | 2. Disk PD Attach / Reattach | 3. Clean Scale-to-Zero (Warm Pools) |
| :--- | :--- | :--- | :--- |
| **Primary Objective** | Restores running process memory & socket states. | Retains files, configurations, and workspace data. | Minimizes infrastructure cost by destroying idle pods. |
| **Underlying Tech** | **gVisor (CRIU)** & GKE Pod Snapshots. | **K8s PVCs** (StatefulSet-like semantics). | Core K8s Pod scheduling / `SandboxWarmPool`. |
| **Storage Backend** | Google Cloud Storage (GCS) bucket. | Persistent Disk (GCE Persistent Disk / CSI). | Ephemeral disk (`emptyDir` or standard nodes). |
| **Restore Latency** | **Extremely Low** (milliseconds to resume memory). | **Medium** (time to mount disk + boot application). | **High** (must pull image, boot app, onboard). |
| **Infrastructure Cost** | Higher (Snapshot storage, GCS IOPS). | Medium (Persistent Disk block storage costs). | **Extremely Low** (zero resources when scaled to 0). |
| **Best Suited For** | Active user sessions, real-time WebSocket loops. | Downloaded models, packages, session databases. | Long-duration background task workers. |

---

## 1. GKE Pod Snapshot Controller (gVisor / CRIU)

This technique captures a running "snapshot" of the live container runtime environment.

```
Active Pod (gVisor) ──[PodSnapshotManualTrigger]──> Checkpoint Image ──> GCS Bucket
                                                                             │
Active Pod (Resumed) <──[Scale up & Restore]─────────────────────────────────┘
```

### How it Works
1. When a suspend request is triggered, the Lifecycle Daemon creates a GKE `PodSnapshotManualTrigger` Custom Resource.
2. The GKE gVisor runtime freezes the container processes, checkpoints their memory states using **CRIU**, and streams the compressed image to a GCS bucket.
3. The Sandbox `spec.operatingMode` transitions to `Suspended`, deleting the active Pod.
4. On resume, the Warm Pool provisions a Pod, GKE pulls the memory checkpoint, and CRIU restores the processes. 
5. **Verification**: The controller verifies restoration by watching the Pod status for `type="PodRestored"` with status `status="True"`.

---

## 2. PD Attach / Reattach (Volume Claims)

This technique relies on Persistent Volume Claims (PVCs) dynamically provisioned via `volumeClaimTemplates` within the `SandboxTemplate`.

```
Sandbox Pod (Active) ──[Mounts]──> Persistent Volume Claim (PVC) ──> Persistent Disk
       │ (Delete Pod / Suspend)
       ▼
Sandbox Pod (Deleted)  [Volume remains detached, but safe in K8s]
       │ (Recreate Pod / Resume)
       ▼
Sandbox Pod (Active) ──[Re-attaches]──> Same Persistent Volume Claim (PVC)
```

### How it Works
1. The `SandboxTemplate` defines a `volumeClaimTemplates` spec (similar to a Kubernetes `StatefulSet`).
2. The Sandbox controller dynamically creates a dedicated PVC for the sandbox container (e.g., mounted at `/home/node/.clawdbot` and `/home/node/clawd`).
3. When the sandbox is suspended (`operatingMode: Suspended`), the backing Pod is destroyed, but the PVC **remains untouched** in the cluster.
4. When the sandbox is resumed (`operatingMode: Running`), the controller recreates the Pod and **reattaches the exact same PVC** to the container.
5. OpenClaw boots up and immediately accesses its persistent configuration and workspace files.

---

## 3. Clean Scale-to-Zero (Warm Pools)

A standard lifecycle pattern where the container state is ephemeral, but warm container instances are kept ready in a pool to speed up cold starts.

### How it Works
1. When idle, the Sandbox Pod is deleted, and any un-mounted file storage is wiped.
2. On wakeup, the Lifecycle Daemon claims a pre-warmed container shell from the `SandboxWarmPool`.
3. Adoption is executed using an **optimistic lock** via the annotation:
   ```yaml
   agents.x-k8s.io/sandbox-name: "openclaw-sandbox-user1"
   ```
4. The adopted Sandbox is re-parented to the claim, and OpenClaw performs its initial onboarding sequence.

---

## Recommended Integration Strategy for OpenClaw

To achieve a premium developer experience for OpenClaw gateways, we recommend a **hybrid architecture** combining all three techniques:

1. **Volume Claims (PD Attach/Reattach)** for persistent workspace storage:
   - Configure `volumeClaimTemplates` for OpenClaw's configuration (`.clawdbot`) and workspace data (`clawd`).
   - This guarantees that regardless of whether the container is suspended gracefully or suffers an abrupt eviction, no agent history, API tokens, or workspace code files are ever lost.
2. **GKE Pod Snapshot Controller** for active WebSocket sessions:
   - Use gVisor Pod Snapshots to preserve the active memory state when scaling down dynamically.
   - This allows real-time client interfaces (like the OpenClaw web UI) to resume instantly without forcing users to log back in or trigger full agent workspace refreshes.
3. **Sandbox Warm Pools** for rapid allocation:
   - Keep a pool of pre-warmed, generic sandboxes ready for instant leasing when users request new gateways, falling back to a cold-start only when custom environment variables require a rebuild.

---

## Process & Timer State Behavior during Suspension

When planning scheduled tasks (e.g. *"in 4 minutes list my files"*), the type of suspension chosen changes the behavior of active background processes and timers significantly:

| State Type / Component | PVC-Only Suspend (MVP) | GKE Pod Snapshot (CRIU) |
| :--- | :--- | :--- |
| **Active bash commands** (e.g., `sleep 240`) | **Killed**. Active shell processes are terminated immediately upon pod eviction. | **Frozen & Resumed**. Process tree and PIDs are frozen and resume execution right where they left off. |
| **Node.js/Python Timers** (`setTimeout`, `setInterval`) | **Lost**. In-memory event loop is wiped when the container is terminated. | **Preserved**. Event loop state is saved; timers continue from their remaining duration. |
| **Workspace Code Files & Configs** | **Preserved**. Safely saved to the persistent volume mount (`workspaces-pvc`). | **Preserved**. Safe on GCS/CSI backing snapshots. |
| **SQLite / File Databases** (`.clawdbot`, `.jsonl` history) | **Preserved**. Automatically saved as files on the persistent volume. | **Preserved**. Backing storage remains consistent. |
| **Active TCP/WebSocket Connections** | **Disconnected**. Client must reconnect when the new pod boots up. | **Buffered / Restored**. Proxy holds/re-establishes session states. |
| **Scheduled Tasks / Cron Strategy** | **Requires External wakeup** (using annotation-based pre-wakeup controller to trigger GKE resume). | **Internal sleep continues** upon GKE memory restoration. |
