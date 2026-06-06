# Aligning OpenClaw Internal Mechanisms for Scale-to-Zero

To allow OpenClaw to be scaled down to zero when idle, we must reconfigure its active, self-triggering components. Otherwise, background heartbeats and scheduler tasks will artificially prevent idleness or cause fatal failures when the container is scaled down.

---

## 1. Heartbeat Configuration & Idleness Preservation

By default, OpenClaw regularly wakes models to check on agent state. This prevents the pod from staying idle.

### Implementation Steps
1. **Disable Default Heartbeats**:
   Set the following configuration parameter in OpenClaw's configuration schema (e.g., `openclaw.json`):
   ```json
   {
     "agents": {
       "defaults": {
         "heartbeat": {
           "every": "0m"
         }
       }
     }
   }
   ```
2. **Empty Heartbeat Template**:
   Ensure the `HEARTBEAT.md` file template in the workspace configuration remains empty. This suppresses the underlying agent framework from triggering periodic LLM model cycles.

---

## 2. Inner Cron Integration & Dynamic Pre-Wakeup Jobs

To preserve OpenClaw's internal cron schedulers without running the container 24/7 or duplicating cron logic, we leave the inner cron enabled (`gateway.cron.enabled: true`) and delegate wake-up triggers to GKE.

### Implementation Steps

1. **Keep Internal Scheduler Enabled**:
   OpenClaw Gateway maintains its internal SQLite database or config files defining scheduled tasks.
2. **Expose Next Scheduled Run**:
   Expose an API route `GET /v1/cron/next` on the OpenClaw Gateway server. This endpoint returns the timestamp of the next cron job that needs to run:
   ```json
   {
     "nextRunTime": "2026-06-06T12:00:00Z"
   }
   ```
3. **Register Pre-Wakeup Alarm**:
   When OpenClaw goes idle and calls `/v1/sandbox/suspend` on the Sandbox Lifecycle Daemon:
   - The daemon first queries `/v1/cron/next` on OpenClaw.
   - It calculates a wake-up timestamp exactly **2 minutes prior** (e.g., `11:58:00Z`).
   - It annotates the `Sandbox` resource: `agents.x-k8s.io/next-wakeup: "2026-06-06T11:58:00Z"`.
   - The daemon then suspends the Sandbox (evicting the Pod).
4. **Cluster-level Wake-up Scheduler**:
   A central cluster manager watches all `next-wakeup` annotations and schedules a dynamic timer (or temporary Job) in Kubernetes to call the `/resume` endpoint at the pre-wake time (`11:58:00Z`).
5. **Natural Cron Trigger**:
   The Sandbox starts up and is fully operational by `11:59 AM`. At `12:00 PM`, OpenClaw's internal cron fires naturally. When execution completes, OpenClaw suspends itself again.

---

## 3. Exposing Idle State Metric (`getPendingCount`)

A Kubernetes Auto-scaler or the custom Sandbox Lifecycle Daemon needs to query whether OpenClaw has pending operations before deciding to suspend it.

### Implementation Steps
1. **Expose HTTP Status Endpoint**:
   We will add an HTTP endpoint `/v1/health/idle` to the OpenClaw server runtime (`src/gateway/server.impl.ts`).
2. **Pending Operations Callback**:
   The endpoint will invoke `getPreRestartDeferralCheck` callback (or compute the total active queue size, pending replies, active embedded runs, and active task count):
   ```typescript
   // Expose inside server.impl.ts HTTP router
   router.get("/v1/health/idle", (req, res) => {
     const pendingCount = 
       getTotalQueueSize() +
       getTotalPendingReplies() +
       getActiveEmbeddedRunCount() +
       getActiveTaskCount();

     res.json({
       idle: pendingCount === 0,
       pendingCount: pendingCount,
       details: {
         queueSize: getTotalQueueSize(),
         pendingReplies: getTotalPendingReplies(),
         activeEmbeddedRuns: getActiveEmbeddedRunCount(),
         activeTasks: getActiveTaskCount()
       }
     });
   });
   ```
3. **Auto-Scaler Polling**:
   The Lifecycle Daemon polls `GET /v1/health/idle`. If `idle` is `true` for longer than `max-idle-time` (e.g., 15 minutes), the Daemon proceeds with the graceful scale-to-zero sequence.
