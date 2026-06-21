# agent-sandbox-rl

Generic, **multi-cluster** batch orchestration for running SWE-bench-style RL and
evaluation workloads on [Agent Sandbox](https://agent-sandbox.sigs.k8s.io/).

It builds on [`k8s-agent-sandbox`](../../../../clients/python/agentic-sandbox-client)
and turns the full run lifecycle into a small, framework-agnostic API:

> **load images → configure cluster(s) → compute replicas → preflight → warm
> pools → claim a sandbox per task (hostname/endpoint per sandbox) → run → tear
> down.**

It plugs into any RL stack (R2E-Gym, tunix, TorchRL, SkyRL): the integration
point is a `SandboxHandle` (stable hostname, endpoint, router-free `exec`). Sync
**and** async; low-level **primitives** *and* a managed **runner**; one cluster
or many. Targets the **v1beta1 ("beta")** Agent Sandbox API.

- Design: [`../../plans/agent-sandbox-rl-design.md`](../../plans/agent-sandbox-rl-design.md)
- Architecture & lifecycle: [`docs/architecture.md`](docs/architecture.md)
- RL-framework integration: [`examples/rl_integration.md`](examples/rl_integration.md)

## Why

`k8s-agent-sandbox` is single-sandbox / single-cluster and has no
SandboxTemplate/WarmPool CRUD, sizing, preflight, pre-pull, or batching — every
consumer re-implements those. `agent-sandbox-rl` provides them once, generically,
across clusters.

## Install

```bash
pip install -e clients/python/agentic-sandbox-client \
            -e 'examples/rl-tunix-swebench/clients/python/agent-sandbox-rl[swebench]'
```

Extras: `swebench` (HF `datasets` for `SweBenchSource`), `test` (pytest +
pytest-asyncio). Requires Python ≥ 3.10, a kube context, and — on GKE — the
`gke-gcloud-auth-plugin` on `PATH`.

## Quickstart

### Managed runner (simplest)

```python
from agent_sandbox_rl import SandboxFleet, FleetConfig, ClusterConfig, SweBenchSource, swebench_probe

fleet = SandboxFleet(FleetConfig(
    clusters=[ClusterConfig(name="rl", namespace="rl-tunix-swebench")],
    max_concurrent=8, max_warmpool_size=32, placement="image-affinity"))
fleet.load_tasks(SweBenchSource(limit=8))

# strategy: none | naive | sliding   (concurrency defaults to max_concurrent)
results = fleet.run(swebench_probe, strategy="sliding", concurrency=8)
```

### Primitives (RL loop owns the schedule)

```python
fleet.load_tasks([{"id": "t1", "image": "busybox:1.36"}])
fleet.setup()                         # preflight → plan → warm pools
for task in fleet.tasks:
    h = fleet.acquire(task)           # claim a pre-warmed sandbox
    try:
        print(h.hostname, h.endpoint())
        print(h.exec(["sh", "-c", "echo hi $(hostname)"]))   # router-free
    finally:
        fleet.release(h)
fleet.teardown()
# or: `with fleet: ...`  (setup on enter, teardown on exit)
```

### Async

```python
from agent_sandbox_rl import AsyncSandboxFleet
fleet = AsyncSandboxFleet(cfg); fleet.load_tasks(src)
results = await fleet.run(async_or_sync_process_fn, strategy="naive", concurrency=64)
# or: async with fleet: h = await fleet.acquire(task); ...
```

### CLI example

```bash
cd examples
WARMPOOL_STRATEGY=sliding TASKS_LIMIT=4 MAX_CONCURRENT=4 NAMESPACE=rl-tunix-swebench \
NODE_SELECTOR_KEY=cloud.google.com/gke-nodepool NODE_SELECTOR_VAL=e2-pool \
python run_swebench_fleet.py
```

## Concepts

| Concept | What it is |
| :--- | :--- |
| **Task** | `id` + container `image` + opaque `metadata`. Generic unit of work. |
| **TaskSource** | Produces tasks: `ListSource`, `JsonlSource`, `SweBenchSource` (HF). |
| **FleetConfig** | Clusters + orchestration knobs (concurrency, sizing, placement, template). |
| **ClusterConfig** | One target cluster (context/kubeconfig, namespace, node selector, runtime class, pull secret, weight, capacity). |
| **SandboxFleet** / **AsyncSandboxFleet** | The orchestrator (sync / async). |
| **SandboxHandle** | A claimed sandbox: `hostname`, `pod_name`, `pod_ip`, `endpoint(port)`, `exec(cmd)`, `release()`. |
| **Placement** | Which cluster serves an image: `round-robin`, `least-loaded`, `capacity-weighted`, `image-affinity`. |
| **Strategy** | *When* pools exist: `none`, `naive`, `sliding`. |

## Warm-pool strategies

| Strategy | Behavior | Footprint |
| :--- | :--- | :--- |
| `naive` | Pre-warm every image up front; process all (parallel); tear down. | Highest (all pools at once). |
| `sliding` | Keep only a window of image pools warm, rolling forward. | Bounded (~`window`); window auto-sizes to `max_concurrent`. |
| `none` | One size-1 pool per image on demand, torn down after. | Lowest (cold-start per image). |

## Replica sizing

Pool depth is the image's share of the concurrency budget, not its task count:

```
replicas_image = clamp(round(MAX_CONCURRENT × tasks_image / tasks_total),
                       1, min(tasks_image, MAX_WARMPOOL_SIZE))
```

`MAX_CONCURRENT` is the one knob that both **sizes pools** and **parallelizes
claim+exec**. (`python -m agent_sandbox_rl.sizing` shows old-vs-new footprints.)

## Multi-cluster

Pass several `ClusterConfig`s (different `context`/`kubeconfig`) + a `placement`;
the fleet builds a per-context client for each, distributes pools/claims, and each
`SandboxHandle` carries its owning cluster. Cross-cluster reachability is the
caller's concern (see the integration guide).

## Configuration reference

**FleetConfig:** `clusters`, `placement`, `max_concurrent` (1), `max_warmpool_size`
(8), `window_size` (None=auto), `ready_timeout` (900), `template` (`TemplateSpec`),
`template_name_prefix` (`r2e-img-`), `labels`.

**ClusterConfig:** `name`, `kubeconfig`, `context`, `in_cluster`, `namespace`,
`node_selector`, `runtime_class`, `image_pull_secret`, `weight`, `max_replicas`.

**TemplateSpec:** `resources` (cpu/memory), `keepalive_command` (`sleep infinity`),
`runtime_class`, `node_selector`, `image_pull_secret`, `extra_pod_spec`.

## Operational features

- **Preflight** (`fleet.preflight()`): per-cluster reachability, v1beta1 CRD
  versions, controller, namespace, and (if configured) runtime class + pull
  secret. Hard failures raise `PreflightError`; soft issues are warnings.
- **Pre-pull** (`fleet.prepull()` / `setup(prepull=True)`): a DaemonSet caches
  task images on every node so warm pools skip the multi-GB pull.
- **Cleanup**: everything created is labeled `app=agent-sandbox-rl`; `teardown`
  sweeps claims → pools → templates (defensive against stray claims).

## Troubleshooting

| Symptom | Cause / fix |
| :--- | :--- |
| `PreflightError: ... crd:* not found` | Agent Sandbox extensions not installed — apply the controller + extensions. |
| Claims never resolve / pods `Pending` | Node selector unsatisfiable, or `runtimeClassName` (e.g. gvisor) with no matching nodes. |
| Docker Hub `429` on image pulls | Set `image_pull_secret`, or mirror images to a registry / use pre-pull. |
| `'NoneType' object has no attribute 'decode'` on parallel exec | Handled: `SandboxHandle.exec` builds a fresh `ApiClient` per call (kubernetes `stream()` isn't thread-safe across a shared client). |
| Async `process_fn` calls `handle.exec` | `exec` is blocking — in async code do `await asyncio.to_thread(h.exec, ...)`, or pass a sync `process_fn` (run in a worker thread automatically). |

## Testing

```bash
pytest examples/rl-tunix-swebench/clients/python/agent-sandbox-rl   # mocked, no cluster
```

## Status

Phases 1–7 implemented and live-verified on GKE (agent-sandbox `v0.5.0rc1`):
config/sizing, multi-cluster, template/warm-pool CRUD, sources/placement/handles,
fleet primitives, strategies + parallel execution, preflight, pre-pull, async,
and the SWE-bench adapter + example. See [`docs/architecture.md`](docs/architecture.md)
and [`CHANGELOG.md`](CHANGELOG.md).
