# Plugging `agent-sandbox-rl` into an RL framework

The fleet is framework-agnostic. The contract is simple:

1. build a `FleetConfig` (one or many `ClusterConfig`s),
2. `load_tasks(...)`,
3. either drive the **primitives** yourself (`setup` → `acquire` → use
   `handle.hostname`/`handle.endpoint()`/`handle.exec(...)` → `release` →
   `teardown`), or call the **managed runner** `fleet.run(process_fn, strategy,
   concurrency)`.

A `SandboxHandle` is the integration point: `hostname` (stable in-cluster DNS),
`pod_name`, `pod_ip`, `endpoint(port)`, `exec(cmd)` (router-free), `release()`.

## Generic env wrapper (primitives)

```python
from agent_sandbox_rl import SandboxFleet, FleetConfig, ClusterConfig, SweBenchSource

fleet = SandboxFleet(FleetConfig(
    clusters=[ClusterConfig(name="c1", namespace="rl")],
    max_concurrent=16, max_warmpool_size=32, placement="image-affinity"))
fleet.load_tasks(SweBenchSource(limit=500))
fleet.setup()                         # preflight + plan + warm pools

class SweEnv:
    def reset(self, task):
        self.h = fleet.acquire(task)  # a live, isolated sandbox
        return self.h.endpoint()      # connect your agent here (or self.h.exec)
    def step(self, action):
        return self.h.exec(action)    # router-free command exec
    def close(self):
        fleet.release(self.h)

# ... run rollouts ...
fleet.teardown()
```

## tunix (`eval_deepswe.py`)

Replace the hand-rolled warm-pool block in `run_evaluation` with the fleet:

```python
fleet = SandboxFleet(FleetConfig(clusters=[ClusterConfig(namespace=NS)],
                                 max_concurrent=MAX_CONCURRENT))
fleet.load_tasks([{"id": e["instance_id"], "image": e["docker_image"]} for e in entries])
results = fleet.run(my_rollout_fn, strategy="sliding", concurrency=MAX_CONCURRENT)
```

## R2E-Gym (`kubernetes-sandbox` backend)

`DockerRuntime._start_kubernetes_sandbox` becomes a fleet acquire; exec stays
router-free:

```python
self._handle = fleet.acquire(Task(id=instance_id, image=docker_image))
# self.container_name = self._handle.pod_name   # for kubectl exec
out = self._handle.exec(cmd)
# teardown: self._handle.release()
```

## TorchRL / SkyRL

Wrap `acquire`/`release` around an episode in your `EnvBase`/env:

```python
class SandboxEnv(EnvBase):
    def _reset(self, td):
        self._h = fleet.acquire(self._task)
        ...
    def _step(self, td):
        obs = self._h.exec(td["action"])
        ...
    def close(self):
        fleet.release(self._h)
```

For async frameworks, use `AsyncSandboxFleet` (awaitable `acquire`/`release`/
`run`; `process_fn` may be a coroutine):

```python
from agent_sandbox_rl import AsyncSandboxFleet
fleet = AsyncSandboxFleet(cfg); fleet.load_tasks(src)
results = await fleet.run(async_rollout, strategy="sliding", concurrency=64)
```

## Multi-cluster

Give several `ClusterConfig`s (different `context`/`kubeconfig`) and a
`placement` policy; the fleet spreads pools/claims across clusters and each
`SandboxHandle` carries its owning cluster's connection info:

```python
FleetConfig(clusters=[
    ClusterConfig(name="us-central2", context="ctx-a", namespace="rl"),
    ClusterConfig(name="us-east1",   context="ctx-b", namespace="rl", weight=2.0),
], placement="image-affinity", max_concurrent=128)
```

Cross-cluster reachability is the caller's concern: in-cluster learners use the
sandbox DNS hostname; out-of-cluster learners need per-cluster routable endpoints
(Gateway/LoadBalancer) or co-located workers.
