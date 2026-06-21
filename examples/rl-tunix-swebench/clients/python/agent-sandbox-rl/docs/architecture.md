# Architecture & lifecycle

`agent-sandbox-rl` is a thin orchestration layer over the `k8s-agent-sandbox`
SDK. It owns the *batch / fleet* concerns the SDK doesn't: resource CRUD,
sizing, placement across clusters, strategies, preflight, pre-pull, and parallel
execution.

## Layers

```
            your RL framework / CLI
                     │
        SandboxFleet / AsyncSandboxFleet        (orchestrator)
        ├── sources      Task / *Source         (what to run)
        ├── placement    image → Cluster        (where)
        ├── sizing       replicas per image      (how many)
        ├── strategies   none / naive / sliding  (when pools exist)
        ├── preflight    per-cluster checks
        ├── prepull      DaemonSet image cache
        └── ClusterRegistry → Cluster(s)
                              ├── Resources      Template/WarmPool CRUD (v1beta1)
                              └── k8s_agent_sandbox SDK   (claim/exec/terminate)
                     │
              Agent Sandbox controller (CRDs) on each cluster
```

Only `Resources` (SandboxTemplate/SandboxWarmPool CRUD) and the per-cluster
wiring are genuinely new k8s code; **claiming reuses the SDK** (`SandboxClient`,
`K8sHelper`) — one set per cluster, pointed at its context by attribute
injection (no SDK fork).

## Components

- **`config.py`** — `FleetConfig`, `ClusterConfig`, `TemplateSpec`,
  `ResourceSpec` (pydantic). `FleetConfig.template_name(image)` → `r2e-img-<md5>`.
- **`cluster.py`** — `Cluster` (own `ApiClient` per context →
  `CustomObjectsApi`/`CoreV1Api`/`AppsV1Api` + `Resources`; lazy injected
  `K8sHelper`/`SandboxClient`; placement/capacity bookkeeping) and
  `ClusterRegistry`.
- **`resources.py`** — `Resources`: `ensure_template`, `create_warmpool`,
  `wait_for_pool_ready`, `delete_*`, `list_*` (label-scoped). The missing SDK
  piece.
- **`sizing.py`** — `compute_replicas`, `recommend_window`, `plan`.
- **`sources.py`** — `Task`, `TaskSource`, `ListSource`, `JsonlSource`,
  `to_tasks`. **`adapters/swebench.py`** — `SweBenchSource` + `swebench_probe`.
- **`placement.py`** — `RoundRobin`, `LeastLoaded`, `CapacityWeighted`,
  `ImageAffinity` (capacity-aware) + `get_placement`.
- **`handles.py`** — `SandboxHandle` (`hostname`, `pod_name`, `pod_ip`,
  `endpoint`, `exec`, `release`); `exec` builds a fresh `ApiClient` per call
  (thread-safe).
- **`preflight.py`** — `preflight_cluster` → `PreflightReport`.
- **`prepull.py`** — DaemonSet pre-pull (`prepull` / `prepull_delete`).
- **`fleet.py`** — `SandboxFleet`; **`strategies.py`** — `process_parallel` +
  the three strategies; **`async_fleet.py`** — `AsyncSandboxFleet`.

## Lifecycle

```
load_tasks ─▶ preflight ─▶ plan ─▶ [prepull] ─▶ start_warmpools ─▶ acquire* ─▶ release* ─▶ teardown
                                   (per-cluster)   (sized pools)    (claim+    (delete    (sweep claims→
                                                                     hostname)  claim)     pools→templates)
```

- **plan**: each unique image → a cluster (placement); replicas sized per
  `(cluster, image)` via `sizing`.
- **acquire**: SDK `create_sandbox(warmpool=…)` → resolve sandbox → `get_pod_name`
  → `SandboxHandle`. Claims are labeled for sweepable cleanup.
- **strategies**: `naive` warms all then runs; `sliding` warms a rolling window;
  `none` warms one size-1 pool per image. All run claim+exec in parallel up to
  `concurrency` (threads sync, `asyncio.gather` async).

## Connection model

A Sandbox has a **stable in-cluster DNS name** = `sandbox_id` = `handle.hostname`.
In-cluster learners connect via `handle.endpoint(port)` (`<hostname>.<ns>:<port>`)
or run commands with `handle.exec` (router-free, via the pod exec API — the SDK
Sandbox Router is *not* required). Out-of-cluster / cross-cluster learners need
per-cluster routable endpoints (Gateway/LoadBalancer).

## Design notes

- **Multi-cluster via attribute injection.** Each `Cluster` builds its own
  `ApiClient` (`new_client_from_config(context=…)`) and points the SDK's
  `K8sHelper`/`SandboxClient` at it. No SDK changes required; a native
  `K8sHelper(api_client=…)` param is a candidate upstream improvement.
- **Async is a thread-backed wrapper.** `AsyncSandboxFleet` reuses the tested
  sync core via `asyncio.to_thread` with real concurrency
  (`gather` + `Semaphore`). The API is fully awaitable; a native
  `kubernetes_asyncio` backend can replace the internals later.
- **Thread-safety.** Fleet bookkeeping is lock-guarded; `handle.exec` uses a
  per-call `ApiClient` because kubernetes `stream()` (websocket) isn't safe
  across a shared client.
- **Cleanup safety.** Everything is labeled `app=agent-sandbox-rl`; teardown
  sweeps stray claims before pools/templates so a leaked claim can't keep its
  adopted sandbox alive.

## Optimization findings (from the rl-tunix-swebench example)

The strategies/sizing/prepull here encode measured results — see the example's
[`plans/optimizations.md`](../../../plans/optimizations.md),
[`plans/performance.md`](../../../plans/performance.md), and
[`plans/image-analysis.md`](../../../plans/image-analysis.md): warm-pool claims
are sub-second; image **layer sharing** makes pre-pull pay off per repo-family;
concurrency-aware sizing slashes idle footprint; parallel claim+exec scales the
task region ~linearly; the SWE-Bench-Verified set is 500 images / 12 families
(django ≈ 46%).
