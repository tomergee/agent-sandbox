# agent-sandbox-rl

Generic, multi-cluster batch orchestration for running SWE-bench-style RL/eval
workloads on [Agent Sandbox](https://agent-sandbox.sigs.k8s.io/). Builds on
`k8s-agent-sandbox`: load images → configure one/many clusters → templates/pools
→ size replicas → preflight → warm pools → claims (hostname/endpoint per sandbox)
→ teardown. Framework-agnostic (plugs into R2E-Gym, tunix, TorchRL, SkyRL).

See the full design at
[`../../plans/agent-sandbox-rl-design.md`](../../plans/agent-sandbox-rl-design.md).

## Status

**Phase 1** (this commit): config models, replica sizing, constants, exceptions.
Cluster/resources/fleet/strategies/async land in later phases per the design's
implementation order.

```python
from agent_sandbox_rl import FleetConfig, ClusterConfig, compute_replicas

cfg = FleetConfig(
    clusters=[ClusterConfig(name="rl-testing-tomer", namespace="rl-tunix-swebench")],
    max_concurrent=8, max_warmpool_size=32,
)
compute_replicas(tasks_image=40, tasks_total=100, max_concurrent=8, max_pool=32)  # -> 3
```

```bash
python -m agent_sandbox_rl.sizing   # old-vs-new sizing demo
```

## Install (editable, with the SDK)

```bash
pip install -e clients/python/agentic-sandbox-client \
            -e 'examples/rl-tunix-swebench/clients/python/agent-sandbox-rl[swebench]'
pytest examples/rl-tunix-swebench/clients/python/agent-sandbox-rl
```

Targets the **v1beta1 ("beta")** Agent Sandbox API.
