# Changelog

All notable changes to `agent-sandbox-rl`. Format loosely follows
[Keep a Changelog](https://keepachangelog.com/); versions are dev-stage.

## [0.1.0.dev0] — unreleased

Initial implementation (design phases 1–7), live-verified on GKE against Agent
Sandbox `v0.5.0rc1` (v1beta1).

### Added
- **Config** (`config.py`): `FleetConfig`, `ClusterConfig`, `TemplateSpec`,
  `ResourceSpec`; deterministic `template_name()`.
- **Sizing** (`sizing.py`): concurrency-aware `compute_replicas`,
  `recommend_window`, `plan` (+ `python -m agent_sandbox_rl.sizing` demo).
- **Constants/exceptions**: v1beta1 groups/versions/plurals incl. the
  SandboxTemplate/WarmPool ones the SDK lacks; `FleetError` hierarchy.
- **Multi-cluster** (`cluster.py`): `Cluster` (per-context `ApiClient`, lazy
  SDK `K8sHelper`/`SandboxClient` via attribute injection) + `ClusterRegistry`.
- **Resource CRUD** (`resources.py`): SandboxTemplate/SandboxWarmPool create/
  delete/list + `wait_for_pool_ready` + claim sweep helpers. `wait_for_pool_ready`
  uses a Kubernetes **watch** (event-driven, near-exact readiness timing, no fixed
  poll grid; reconnects + falls back to a short re-check on drops).
- **Environment in RunReport**: `SandboxFleet.describe_environment()` collects
  per-cluster context/namespace/k8s-version/nodes/node-pools/instance-types/region;
  `run()` attaches it to `report.environment` (rendered in `summary()`/`to_dict()`).
  `examples/run_swebench_fleet.py` writes a timestamped `.txt`+`.json` report to
  `REPORT_DIR` when set.
- **Sources** (`sources.py`): `Task`, `TaskSource`, `ListSource`, `JsonlSource`,
  `to_tasks`.
- **Placement** (`placement.py`): `RoundRobin`, `LeastLoaded`,
  `CapacityWeighted`, `ImageAffinity`, capacity-aware, `get_placement`.
- **Handles** (`handles.py`): `SandboxHandle` with `hostname`, `pod_name`,
  `pod_ip`, `endpoint`, router-free `exec` (thread-safe), `release`.
- **Fleet** (`fleet.py`): `SandboxFleet` — `load_tasks`, `preflight`, `plan`,
  `ensure_templates`, `start_warmpools`, `warm_image`/`unwarm_image`, `prepull`,
  `setup`, `acquire`/`acquire_batch`, `handles`/`hostnames`/`endpoints`,
  `release`/`release_all`, `teardown`, `run`, context manager.
- **Strategies + parallelism** (`strategies.py`): `none`/`naive`/`sliding` +
  `process_parallel` (bounded ThreadPool; per-task errors captured).
- **Preflight** (`preflight.py`): per-cluster checks → `PreflightReport`.
- **Pre-pull** (`prepull.py`): DaemonSet image cache (one init container/image).
- **Async** (`async_fleet.py`): `AsyncSandboxFleet` — awaitable parity over the
  sync core (thread-backed; `gather`+`Semaphore`; sync or coroutine `process_fn`).
- **SWE-bench adapter** (`adapters/swebench.py`): `SweBenchSource` (HF dataset),
  `swebench_probe`.
- **Observability** (`observability.py`, design phase 8): always-on `RunReport`
  (per-phase count/total/max, claims, tasks ok/err, warm-replica total+peak;
  `summary()`/`to_dict()`); opt-in Prometheus `asrl_*` series on the default
  registry (`asrl_phase_latency_seconds`, `asrl_task_latency_seconds`,
  `asrl_run_latency_seconds`, `asrl_claims_total`, `asrl_tasks_total`,
  `asrl_warm_replicas`; labels `phase·cluster·family·strategy·status`); opt-in
  OpenTelemetry spans that reuse the SDK's tracer/provider so fleet spans nest
  with the SDK's claim/exec spans; `repo_family()` cardinality bound;
  `serve_metrics()` HTTP helper. Wired through `fleet.py`/`strategies.py`/
  `async_fleet.py`; `ObservabilityConfig` on `FleetConfig.observability`;
  `fleet.report` after `run()`. `prometheus-client` is a dep; OTel via the
  `tracing` extra (no-op when absent).
- **Examples**: `examples/run_swebench_fleet.py` (multi-cluster CLI),
  `examples/rl_integration.md` (tunix / R2E-Gym / TorchRL / SkyRL).
- **Docs**: README, `docs/architecture.md`, this changelog.
- **Tests**: 96 mocked unit tests (sizing, config, resources incl. watch-based
  pool readiness, cluster, sources, placement, fleet incl. 2-cluster routing,
  strategies/parallel, preflight, prepull, async, swebench, observability incl.
  the RunReport environment block).

### Notes / known follow-ups
- Async backend is a thread-backed wrapper; a native `kubernetes_asyncio` path
  may replace the internals later (API stays the same).
- Candidate upstreams into `k8s-agent-sandbox`: SandboxTemplate/WarmPool
  constants + CRUD, and a `K8sHelper(api_client=...)` parameter.
- Version is dynamic/dev (`0.1.0.dev0`); switch to setuptools-scm on release.
