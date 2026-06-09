# Massive Scaling of OpenClaw Sandboxes

This document evaluates the architectural design and cost/performance savings of transitioning from individual Persistent Volume Claims (PVC) per sandbox to a centralized, multi-tenant database for storing OpenClaw agent state, memory, and task queues at high scale (e.g., 1,000+ parallel sandboxes).

---

## 1. Core Comparison: Individual PVCs vs. Centralized Database

When running 1,000+ sandboxes, the architecture chosen for database state storage (SQLite vs. Central PostgreSQL/Spanner) impacts costs, startup times, and system reliability:

| Feature / Metric | Individual PVCs (Local SQLite) | Centralized Multi-Tenant Database |
| :--- | :--- | :--- |
| **Storage Provisioning Cost** | **High Overhead**. Pay for minimum disk sizes (e.g., 10GB per PVC). | **Low Cost**. Pay only for the raw compiled database size of all tenants. |
| **Resume Startup Latency** | **Slow (10s - 20s)** due to physical block device detach/mount steps. | **Fast (1s - 2s)**. Pods boot instantly without disk attachments. |
| **Kubernetes Controller Load** | **High**. Reattaching 1,000 PVs triggers API server bottlenecks. | **Zero**. Standard stateless container scheduling. |
| **Race Conditions** | **Frequent** PV mount lock errors on rapid suspend/resume. | **None**. Connections are standard TCP/TLS database sockets. |
| **Backup and Recovery** | Complex. Must coordinate 1,000 distinct volume snapshots. | Simple. Standard point-in-time recovery (PITR) of a single DB. |

---

## 2. Infrastructure Cost Analysis (for 1,000 Sandboxes)

* **Local PVC Model (SQLite)**:
  - Standard cloud block storage (like GCP Persistent Disk) has a minimum size constraint of **10 GB per PVC**.
  - **1,000 Sandboxes × 10 GB = 10,000 GB (10 TB)** of provisioned block storage.
  - Estimated Monthly Cost: **$400 to $1,200/month** (depending on HDD/SSD standard tiers).
* **Centralized Database Model**:
  - SQLite data (history, logs, and cron tasks) for an average agent rarely exceeds **10 MB**.
  - **1,000 Sandboxes × 10 MB = 10,000 MB (10 GB)** of actual raw database storage.
  - Estimated Monthly Cost: **$1.50 to $10/month** for a managed PostgreSQL instance.
  - **Net Savings: ~99% reduction in database storage costs.**

---

## 3. Cold-Start Latency Savings (Resume/Wakeup Speed)

In a scale-to-zero model, the agent pod is destroyed when idle. When a new user request arrives, the pod must be provisioned and resumed:
1. **With PVCs**: The GKE volume attachment manager must detach the Persistent Disk from the previous worker node and attach/mount it to the new node. This serial step takes **5 to 15 seconds** of blocking overhead.
2. **With a Centralized Database**: Sandboxes run **diskless** (leveraging local ephemeral storage or standard `emptyDir`). Pod scheduling and container startup complete in **less than 2 seconds**.

---

## 4. State Division: What remains in the Sandbox?

Moving the SQLite databases to a central server removes the database state, but the following files must still be managed inside the Sandbox filesystem:

```
┌────────────────────────────────────────────────────────┐
│               EPHEMERAL SANDBOX POD                    │
│                                                        │
│  ┌──────────────────────┐    ┌──────────────────────┐  │
│  │   Workspace Files    │    │  Installed Packages  │  │
│  │ (Code, Cloned Repos) │    │  (node_modules, pip) │  │
│  └──────────────────────┘    └──────────────────────┘  │
│  ┌──────────────────────┐    ┌──────────────────────┐  │
│  │   Secrets & Keys     │    │  Cached Models / OS  │  │
│  │  (.env, SSH, GCP)    │    │ (HuggingFace, Temp)  │  │
│  └──────────────────────┘    └──────────────────────┘  │
└───────────┬────────────────────────────────────────────┘
            │ (Remote Connections)
            ▼
┌────────────────────────────────────────────────────────┐
│             CENTRAL MANAGED DATABASE                   │
│  - SQLite Memory Index (main.sqlite)                   │
│  - Main State Flow Registry (openclaw.sqlite)          │
│  - Task Runs & Schedules (runs.sqlite)                 │
└────────────────────────────────────────────────────────┘
```

### Files remaining in the Sandbox:
1. **Workspace Files**: Source code, scripts, outputs, and local text files created by the agent (stored at `/root/.openclaw/workspace/`).
2. **Dynamic Dependencies**: Node/Python packages installed at runtime (`node_modules/`, Python virtual environments).
3. **Environment Credentials**: `.env` configuration profiles, private SSH keys, and service account keys.
4. **Cache Stores**: Downloaded model weights (e.g., HuggingFace cache), tokenizers, and NPM caches.

### Recommended Diskless Workspace Solutions:
To keep Sandboxes completely diskless (avoiding PV attachment latency):
- **Git Push Deferral**: Force the agent to commit and push all workspace file edits to a remote GitHub repository before going idle/suspending.
- **Cloud Storage FUSE Mount**: Mount a shared Google Cloud Storage (GCS) bucket to the agent's `/workspace` folder using Cloud Storage FUSE. GCS FUSE provides fast, stateless mounts without block storage attachment latency.
