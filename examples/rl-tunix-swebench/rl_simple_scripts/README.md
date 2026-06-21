# rl_simple_scripts — original prototype (scripts)

The first, **script-based** implementation of the SWE-bench warm-pool example.
Two parallel takes on the same flow (provision pools → claim a sandbox per task →
exec a probe → tear down):

- **Python driver** — `run_swebench.py` → `strategies.py` → `sizing.py` +
  `warmpool.py`. Talks to the cluster with `kubernetes` + the `k8s-agent-sandbox`
  SDK.
- **Bash e2e** — `e2e_test.sh` (self-contained, `kubectl`/`curl`/`jq` only; no
  Python) with per-phase benchmark timers, plus `prepull.sh` (a DaemonSet image
  pre-pull helper).

`requirements.txt` installs the Python deps (`kubernetes`, `k8s-agent-sandbox`,
`datasets`).

These are kept as a readable, dependency-light reference. For the productized,
multi-cluster, sync+async version see the package under
[`../clients/python/agent-sandbox-rl`](../clients/python/agent-sandbox-rl). The
shared `manifests/` and the interactive `rl-tunix-swebench-demo.ipynb` (which
imports `strategies`/`warmpool` from here) live one level up.

## Run

From the **example root** (`examples/rl-tunix-swebench/`):

```bash
pip install -r rl_simple_scripts/requirements.txt
kubectl apply -f manifests/namespace.yaml

# Python driver
WARMPOOL_STRATEGY=naive TASKS_LIMIT=1 NAMESPACE=rl-tunix-swebench \
  python rl_simple_scripts/run_swebench.py

# Bash e2e
./rl_simple_scripts/e2e_test.sh -s naive -n 2 -y
```

See the top-level [`../README.md`](../README.md) for full options, expected
output, and configuration.
