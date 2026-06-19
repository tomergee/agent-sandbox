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

---

## Decisions / changelog

- _(none yet)_
