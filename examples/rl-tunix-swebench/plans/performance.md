# rl-tunix SWE-bench warm pools — Performance log

Working notes (local, git-ignored). Tracks measured performance of runs and the
optimizations we test. All times in seconds unless noted. See
[`optimizations.md`](./optimizations.md) for the idea backlog.

## Environment

| Item | Value |
| :--- | :--- |
| Cluster | `glottman-sandbox-test-1` (GKE Standard) |
| Project / Region | `gke-ai-eco-dev` / `us-central1` |
| GKE version (master/node) | `1.35.5-gke.1000000` |
| kubectl (client/server) | `v1.35.3-dispatcher` / `v1.35.5-gke.1000000` |
| Node pool | `standard-pool` — `e2-standard-2`, **3 nodes**, no autoscaling |
| Disk / type | 100 GB `pd-balanced` per node |
| Allocatable / node | ~`1930m` CPU, ~`5.9Gi` mem (≈ **5.8 vCPU / 17.6Gi** total) |
| Accelerators | none (no GPU/TPU) |
| Agent Sandbox controller | `…/agent-sandbox-repo/agent-sandbox-controller:latest` (custom main build) |
| CRD API version | `extensions.agents.x-k8s.io/v1beta1` (installed 2026-05-28) |
| RuntimeClasses | `gvisor`, `confidential-linked-runner` (no gVisor node pool → **gvisor unused**) |
| Sandbox-router | not deployed (exec is router-free via `kubectl exec`) |
| Task images | `slimshetty/swebench-verified:sweb.eval.x86_64.<instance>` (~1.2 GB each, Docker Hub) |

> Update this table whenever the cluster/version/pool/controller changes, and
> note the change date.

## Runs

Phase columns are wall-clock seconds for that phase (aggregated across tasks).
**Cached** = task image already present on the node (no multi-GB pull).
Legend: prov=provision, warm=wait-for-ready (pull), clm=claim, exec=exec probe,
tear=teardown.

| Date (local) | Tool | Strategy | Tasks | Win/MaxPool | Opt tested | Cached | prov | warm | clm | exec | tear | **TOTAL** | Notes |
| :--- | :--- | :--- | :--: | :--: | :--- | :--: | --: | --: | --: | --: | --: | --: | :--- |
| 2026-06-18 | e2e_test.sh | naive | 1 | –/1 | baseline | yes | 3.8 | 1.0 | 3.8 | 2.6 | 5.0 | **28.5** | astropy-12907 |
| 2026-06-18 | e2e_test.sh | none | 1 | –/1 | baseline | yes | 4.1 | 1.1 | 4.1 | 2.6 | 5.0 | **30.9** | astropy-12907 |
| 2026-06-18 | e2e_test.sh | sliding | 2 | 1/8 | baseline | yes | – | – | – | – | – | **53.8** | 12907→13033; only total captured |

### Cold-pull baseline (separate observation)
- First-ever pull of a ~1.2 GB SWE-bench image onto a fresh node: **minutes**
  (dominates the `warm` phase). Once node-cached, `warm` ≈ 1 s. → image pre-pull
  (opt #1) targets exactly this.

### Notes on baseline numbers
- `preflight` (~1.8 s), `fetch tasks` (~3–4 s, HF API), `create namespace`
  (~1 s) are fixed per-run overheads not shown in the per-strategy columns above.
- Serial claim+exec: `clm`+`exec` scale ~linearly with task count today
  (opt #3 = parallelize).
- These are **baseline** (no optimizations applied). New rows should set the
  "Opt tested" column to the optimization being measured and keep the same
  cluster/config (or note deltas in Environment).

## How to reproduce a row

```bash
cd agent-sandbox/examples/rl-tunix-swebench
NODE_SELECTOR_KEY=cloud.google.com/gke-nodepool NODE_SELECTOR_VAL=standard-pool \
  ./e2e_test.sh -s <none|naive|sliding> -n <tasks> [-w <window>] -y
```

Copy the printed `── Benchmark ──` block into a new row above.
