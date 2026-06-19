# rl-tunix SWE-bench warm pools — Optimization ideas

Working notes (local, git-ignored). A living backlog of optimizations for
running SWE-bench tasks on Agent Sandbox warm pools. Add ideas here as we go.

## Workflow recap (baseline we're optimizing)

1. **Task source** — `R2E-Gym/SWE-Bench-Verified`; each task has a real
   `docker_image` (`slimshetty/swebench-verified:sweb.eval.x86_64.<instance>`,
   ~1.2 GB).
2. **Template** — per unique image, a `SandboxTemplate` (v1beta1) running the
   image with `sleep infinity` (keepalive).
3. **Warm pool** — `SandboxWarmPool` (`replicas`, `sandboxTemplateRef`);
   controller pre-pulls + keeps N sandboxes Ready.
4. **Claim** — per task, a `SandboxClaim` (`warmPoolRef`) adopts a pre-warmed
   sandbox; the pool spins up a replacement.
5. **Resolve + exec** — claim `status.sandbox.name` → pod via
   `agents.x-k8s.io/pod-name` annotation → `kubectl exec` (router-free).
6. **Release** — delete claim, then tear down pools/templates.

Strategies (`none` / `naive` / `sliding`) differ only in *when* pools exist.

Current measured baseline (3×e2-standard-2, images node-cached): per task the
non-pull phases are ~claim 4s + exec 2.6s; first-run `wait warm (pull)` dominates
(image pull, minutes) and drops to ~1s once cached.

---

## Known quantities & replica sizing (prior knowledge)

**Everything is known before the run.** The dataset is static, so the
orchestrator can enumerate the full task list, the exact `docker_image` per task,
and per-image frequency up front (`eval_deepswe.py` already builds
`unique_images` + `image_totals`). This enables pre-pull / pre-size / pre-warm
with no guessing.

**SWE-Bench-Verified is 1 image : 1 task** — verified: 100 rows → 100 unique
images; 500 tasks → 500 unique images. So `tasks_image = 1` for every image.

**Replicas-per-image is driven by concurrency, not dataset totals:**

| Mode | concurrent uses of an image | replicas/pool |
| :--- | :--- | :--- |
| pass@1 eval (this example) | 1 | **1** |
| pass@k eval | k | **k** |
| RL training (GRPO group sampling) | `group_size` | **group_size** (~8–16) |

- The scaling variable is **how many image-pools are warm at once** = the
  in-flight concurrency budget (`MAX_CONCURRENT`), *not* the 500 total images.
- ⇒ `naive` is wasteful for verified (500 pools × 1); `sliding` is the right fit
  (keep only the in-flight window warm).
- Replicas/pool only exceed 1 when an image **repeats** in the active batch
  (pass@k, RL group sampling, or image-reusing datasets like R2E-Gym training /
  SWE-smith).
- Sizing rule:
  `replicas_image = min(concurrent_demand_image, MAX_CONCURRENT × tasks_image/tasks_total)`.

**Implication for strategy defaults:** for verified eval, prefer `sliding` with
`replicas=1` and a window ≈ `MAX_CONCURRENT`; reserve `replicas>1` for
pass@k / RL runs.

### When/how tunix actually decomposes it (`eval_deepswe.py`)

Both decisions are **precomputed from the static dataset, up front — before any
task runs** (nothing is discovered at runtime):

1. **What images** — at *module load* (lines ~188–196), right after
   `load_dataset`: filter to rows with `docker_image`, slice by `TASKS_LIMIT`,
   then `unique_images = set(e["docker_image"] for e in entries)`.
2. **How many replicas** — at the *start of `run_evaluation()`* (lines ~688–711):
   `image_totals = Counter(e["docker_image"] for e in entries)`, then per image
   `size = min(count, MAX_WARMPOOL_SIZE)`. `naive` sizes all pools up front;
   `sliding` applies the same `image_totals` incrementally as the window rolls
   (lines ~790–804).

So the replica rule today is **`replicas_image = min(tasks_for_that_image,
MAX_WARMPOOL_SIZE)`** — driven by tasks-per-image, capped by the flat cap.

**Gaps in this rule (→ optimization #2):**
- For verified (1:1) every `count == 1` ⇒ every pool is `replicas=1`;
  `MAX_WARMPOOL_SIZE` never bites. Per-image replicas only exceed 1 when an image
  repeats (pass@k / RL group sampling / image-reusing datasets, where the same
  instance appears as multiple `entries`).
- **Not concurrency-aware:** sizing uses *total* tasks-per-image, not in-flight
  concurrency. An image with 50 tasks and `MAX_WARMPOOL_SIZE=32` pre-warms 32
  idle pods even if only 8 run at once. The fix is to size to
  `min(concurrent_demand, MAX_CONCURRENT × tasks_image/tasks_total)` instead of
  raw `count`. Timing of the decomposition is fine; the **sizing formula** is the
  thing to improve.

## Idea backlog

> Template per idea: **Why / How / Impact / Effort / Status / Open questions**

### 1. Image pre-pull (kill cold-start) — IMPLEMENTED + MEASURED
- **Why:** the multi-GB image pull dominates first-use latency and gates warm
  pool readiness; at scale, node scale-ups re-pay it.
- **How:** `prepull.sh` deploys a `DaemonSet` with one init container per unique
  image (no-op cmd, `IfNotPresent`) → every node caches them; waits for all nodes
  ready, times it. `--delete` removes the DS (cached images persist).
- **Measured (fresh django family, 1 task):** `wait warm` **80.9s cold → 6.0s**
  after pre-pull; DaemonSet pull ran across all 3 nodes in 51.7s. See
  `performance.md` → "Pre-pull (opt #1) — findings".
- **Big caveat discovered — layer sharing:** 2nd image of an *already-pulled repo
  family* warms in ~11s (thin top layer); only the *first image of a fresh
  family* is truly cold (~81s). ⇒ pre-pull value ≈ per **unique repo family /
  base**, not per instance.
- **Impact:** High for fresh families / scale-up; ~break-even for a single task
  but **compounds** with #tasks·replicas·nodes.
- **Effort:** Done (Medium).
- **Status:** implemented (`prepull.sh`); measured.
- **Open:** init containers pull sequentially per node; disk ceiling ~80×1.2 GB
  on 100 GB nodes → pre-pull batch families only. Future: GKE Image Streaming /
  secondary boot disk (opts to compare).
- **When the DaemonSet is / isn't the right tool (honest assessment):**
  - *Bought us:* (a) the measurements (81s→6s) and the **layer-sharing** finding
    that reframed #9; (b) a **placement-agnostic + scale-up guarantee** — caches
    on every node incl. newly autoscaled ones, so a warm pod lands hot wherever
    the scheduler puts it. This is its one structural edge over on-demand pull.
  - *Didn't help:* single/few tasks ≈ **break-even** (pulls on all nodes to serve
    a 1-node task; cost just shifts from claim path to prepull step); **over-pulls**
    vs. a low-replica pool's actual node spread; and it's the **wrong tool for
    "all images"** (the #9 disk conundrum).
  - *Right sweet spot:* a **small hot working set you want instant regardless of
    placement and across scale-up** = the **sliding window's active images**, not
    the whole dataset. ⇒ keep it but scope it to the window (#9); for full-dataset
    runs prefer Image Streaming / node partitioning.
  - *Bottom line:* measured single-run win was marginal; value is **structural +
    diagnostic**, and only pays off feeding **many** claims spread across nodes
    (needs the concurrency/parallel-exec context, #2/#3).

### 2. Proportional / concurrency-aware pool sizing — IMPLEMENTED
- **Why:** the baseline `replicas = min(tasks_image, MAX_WARMPOOL_SIZE)` ignores
  concurrency and pre-warms one pod per *task* → many idle sandboxes; and has no
  global budget.
- **How (`sizing.py`):**
  `replicas_image = clamp(round(MAX_CONCURRENT × tasks_image / tasks_total),
  1, min(tasks_image, MAX_WARMPOOL_SIZE))` — depth = the image's share of the
  concurrency budget. Plus `recommend_window()` so `sliding`'s total warm
  footprint ≈ `MAX_CONCURRENT`. Wired into `strategies.py`, `run_swebench.py`
  (`MAX_CONCURRENT` env; `WARMPOOL_WINDOW_SIZE=0` ⇒ auto window) and `e2e_test.sh`
  (`compute_replicas`).
- **Demonstrated (`python sizing.py`):** skewed 8-image batch (100 tasks, cap 32)
  — baseline pre-warms **92 idle pods**; improved footprint is **8 / 11 / 32 / 92**
  at `MAX_CONCURRENT = 1 / 8 / 32 / 256`. Verified (1:1) stays 1 per image, but
  `sliding` window auto-scales 1→8 with the budget.
- **Coupling:** the budget that makes this real is execution concurrency, so it
  pairs with opt #3 (parallel exec). Default `MAX_CONCURRENT=1` (serial) keeps it
  correct today; raise it with #3.
- **Impact:** High for image-repeating / high-concurrency runs; correctness-safe
  for verified.
- **Effort:** Done (Low).
- **Status:** implemented (`sizing.py`), self-demo included; cluster-measure the
  footprint reduction once #3 lands.

### 3. Parallel task execution
- **Why:** the driver and `e2e_test.sh` currently claim+exec **serially**;
  wall-clock scales linearly with task count.
- **How:** claim/exec tasks concurrently up to a `MAX_CONCURRENT` (bounded by
  pool replicas + cluster capacity); in bash via background jobs + `wait`, in
  Python via asyncio/threads.
- **Impact:** High for multi-task runs.
- **Effort:** Medium (concurrency + error aggregation + timing per task).
- **Open:** how to attribute per-phase timers under concurrency.

### 4. Reuse a sandbox for multiple tasks  — DEPRIORITIZED (niche + risky)
- **Why considered:** claim-per-task churns sandboxes (each claim consumes a warm
  one and forces a replacement spin-up).
- **Reality check:** the model **dirties** the pod every run (edits `/testbed`,
  runs tests, installs deps). So reuse needs a reset between runs.
  - For **SWE-Bench-Verified it doesn't apply at all**: 1:1 image:task, each task
    is a *different image/repo* — nothing to reuse across tasks.
  - Only applies to **repeated runs of the same task** (RL group sampling /
    pass@k: same image + same clean `base_commit`).
- **How (if ever):** keep one claim for G rollouts of the same instance, reset
  between each: `git -C /testbed reset --hard <base_commit> && git -C /testbed
  clean -fdx`.
- **Risk:** reset only restores the git tree, not state *outside* the repo (pip
  installs, caches, `/tmp`, DBs) → **state bleed can corrupt reward**. A fresh
  claimed sandbox guarantees a pristine baseline.
- **Verdict:** fresh-per-task/rollout is the safe correctness default. Skip unless
  claim churn is proven to be a real bottleneck in the group-sampling path.
- **Status:** deprioritized.

### 5. Autoscale pools on demand (HPA)
- **Why:** static replicas can't track bursty claim rates.
- **How:** HPA on the controller's claim-rate metric (see
  `../hpa-swp-scaling`); scale warm pools to maintain a target claim rate.
- **Impact:** Medium/High at scale.
- **Effort:** Medium (needs managed Prometheus + custom metrics adapter).
- **Status:** reference example exists upstream.

### 6. Faster / cheaper readiness + lifecycle
- **Why:** we poll `readyReplicas` / Sandbox `Ready` on a fixed interval;
  abandoned claims linger.
- **How:** add a container `readinessProbe` so Ready means "usable"; use the
  SDK's `shutdown_after_seconds` (claim TTL → auto-delete) for crash-safety;
  consider `kubectl wait --for=condition=Ready` vs custom poll.
- **Impact:** Low/Medium (snappier, self-healing).
- **Effort:** Low.

### 7. Right-size sandbox resources
- **Why:** template requests `250m / 512Mi`; real SWE-bench builds/tests may
  need more, and over-request limits pods-per-node density.
- **How:** profile per repo; set requests/limits per image class.
- **Impact:** Medium (density vs OOM/throttle trade-off).
- **Effort:** Low/Medium.

### 8. gVisor isolation (correctness/security, not speed)
- **Why:** untrusted, model-generated code should be isolated.
- **How:** gVisor-enabled node pool + `RUNTIME_CLASS=gvisor`.
- **Impact:** Security (note: gVisor adds some runtime overhead).
- **Effort:** Medium (infra).
- **Status:** supported via env, not enabled on the current cluster.

### 9. Working-set pre-pull for massive runs (disk-bounded)
- **Conundrum:** for a run over *all* images, "pre-pull every image on every
  node" (current `prepull.sh`) cannot fit — 500 verified × ~1.2 GB ≈ 600 GB vs
  ~100 GB node disk (worse for R2E-Gym/SWE-smith's thousands).
- **Reframe:** a node only needs images for pods scheduled **on it**, so the unit
  is a **per-node working set**, not the whole batch. And node disk = **sum of
  unique layers** (family base stored once + thin per-instance diffs), not Σ full
  image sizes — so the ~12 family bases dominate, not the 500 tags.
- **How:**
  1. **Pre-pull follows the sliding window**, not the batch: pull only the active
     window's images; as it slides, pull the next and let finished ones go.
     (Evolve `prepull.sh` to take the window, not a fixed list.)
  2. **Lean on kubelet image GC:** unused images are LRU-evicted past the disk
     high-threshold (~85% on GKE); a live warm pool pins its image, a torn-down
     pool's image becomes evictable. Window-follow + GC = self-managing cache.
  3. **Working-set constraint:** `window × (family_base + diffs) ≲ node_disk ×
     gc_low_threshold` — bounds window width (ties to the concurrency/sizing
     budget, #2).
- **Scale answers (compare later):** **GKE Image Streaming (gcfs)** — lazy
  layer streaming + bounded cache, removes the "fit everything" problem entirely;
  **node partitioning by repo family** (nodeSelector/affinity per template) so
  each node holds a few families; **secondary boot disk** baking family bases.
- **Impact:** required for full-dataset runs (otherwise disk is the hard gate).
- **Effort:** Medium (window-follow prepull) / Medium-High (streaming, partitioning).
- **Status:** idea (recorded). Depends on / extends #1 (pre-pull) and #2 (sizing).
- **Open:** measure real per-node unique-layer footprint per family; pick window
  width from disk budget; evaluate Image Streaming vs DaemonSet pre-pull.

---

## Decisions / changelog

- _(none yet)_
