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

## Sandbox lifecycle & terminology

"Claimed" = a sandbox has been handed to a consumer (the model's rollout) for
**exclusive use**.

| State | Meaning | Usable by the model? |
| :--- | :--- | :--: |
| **Warm / pooled** | `SandboxWarmPool` keeps N sandboxes pre-created + Ready, but **unassigned** (idle). | No |
| **Claimed** | A `SandboxClaim` adopted one warm sandbox and **bound it to you** (`status.sandbox.name`). | **Yes** |
| **Released** | Claim deleted → sandbox **destroyed** (not returned to the pool). | No |

- **Claim = allocation.** Creating a `SandboxClaim` (with `warmPoolRef`) makes the
  controller pick a pre-warmed sandbox and dedicate it to that claim.
- **Exclusive + singleton.** One claim ↔ one sandbox ↔ one pod; never shared.
- **The pool refills.** Taking a warm sandbox triggers a replacement to keep
  `replicas` Ready (object dumps show your sandbox + a fresh one).
- **Not checkout/return.** Releasing = deleting the claim = the sandbox is torn
  down, not recycled. Each task gets a fresh one (= claim-per-task churn; see
  optimization #4 in `optimizations.md`).
- **What `clm` measures:** time-to-available = create claim → adopt warm sandbox
  → resolve name → confirm Ready. Seconds with a warm pool; includes cold-start
  without pre-warming.

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
| 2026-06-18 | e2e_test.sh | naive | 1 | –/1 | cold, same family | partial | 3.8 | **11.0** | 3.9 | 2.7 | 5.0 | **38.3** | astropy-13236; base layers already cached (astropy) |
| 2026-06-18 | e2e_test.sh | naive | 1 | –/1 | **cold, fresh family** | no | 3.8 | **80.9** | 3.9 | 2.7 | 4.9 | **108.2** | django-10097; true cold pull (~1.2 GB) |
| 2026-06-18 | e2e_test.sh | naive | 1 | –/1 | **pre-pull (#1)** | pre-pulled | 3.7 | **6.0** | 3.7 | 2.6 | 4.9 | **34.0** | django-11095; warm phase = pod start only, pull done by DaemonSet |

### Pre-pull (opt #1) — findings

DaemonSet pre-pull (`prepull.sh`) vs cold, measured on fresh `django` images
(different repo family from the cached `astropy` ones):

| Path (1 task) | pull cost | where it's paid | TOTAL e2e |
| :--- | --: | :--- | --: |
| Cold (no pre-pull) | **80.9s** | in the claim path (`wait warm`), on 1 node | 108.2 |
| Pre-pull then run | 51.7s + 6.0s | DaemonSet step (all 3 nodes, parallel) + ~pod-start | 34.0 (+51.7 prep) |

**Findings:**
1. **Layer sharing is huge.** A second image in an *already-pulled repo family*
   warms in ~11s (only the thin top layer); the *first* image of a fresh family
   is ~81s. ⇒ pre-pull value is mostly about covering **each unique repo family /
   fresh base**, not every instance.
2. **Pre-pull removes the pull from the claim path:** `wait warm` 80.9s → 6.0s
   (just pod start). Time-to-claimable ~84.6s → ~61s here.
3. **Parallel across nodes + scale-up:** the DaemonSet pulled on all 3 nodes at
   once (51.7s total) and any newly-added node would pull automatically — the
   cold path instead re-pays the pull per new pod landing on an uncached node.
4. **For a single task it's ~break-even; it compounds** with more
   tasks/claims/replicas per image and with node scale-up (pull paid once, reused
   by every later claim on every node).
5. **Caveat:** init containers pull **sequentially per node**, and all images
   accumulate on the node's 100 GB disk (~80 × 1.2 GB ceiling) → pre-pull the
   *batch's* unique families, not all 500.

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
