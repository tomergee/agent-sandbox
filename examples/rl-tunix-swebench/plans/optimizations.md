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

## Idea backlog

> Template per idea: **Why / How / Impact / Effort / Status / Open questions**

### 1. Image pre-pull (kill cold-start)
- **Why:** the multi-GB image pull dominates first-use latency and gates warm
  pool readiness; at scale, node scale-ups re-pay it.
- **How:** before the run, deploy a `DaemonSet` with an init container per unique
  image so every node caches them; optionally pre-scale the node pool first so
  new nodes come up pre-cached. (Described in the rl-tunix design doc §5.1.)
- **Impact:** High (turns minutes → seconds on warm-up).
- **Effort:** Medium.
- **Status:** idea.
- **Open:** disk pressure with many large images per node; GC/eviction policy.

### 2. Proportional pool sizing
- **Why:** flat `MAX_WARMPOOL_SIZE` over- or under-provisions per image.
- **How:** size each pool to its share of the batch:
  `replicas_image ≈ GlobalConcurrency × tasks_image / tasks_total`, capped.
  (Design doc §5.2.)
- **Impact:** Medium (less idle reservation, fewer cold claims).
- **Effort:** Low.
- **Status:** idea.

### 3. Parallel task execution
- **Why:** the driver and `e2e_test.sh` currently claim+exec **serially**;
  wall-clock scales linearly with task count.
- **How:** claim/exec tasks concurrently up to a `MAX_CONCURRENT` (bounded by
  pool replicas + cluster capacity); in bash via background jobs + `wait`, in
  Python via asyncio/threads.
- **Impact:** High for multi-task runs.
- **Effort:** Medium (concurrency + error aggregation + timing per task).
- **Open:** how to attribute per-phase timers under concurrency.

### 4. Reuse a sandbox for multiple tasks
- **Why:** claim-per-task churns sandboxes (each claim consumes a warm one and
  forces a replacement spin-up).
- **How:** for tasks sharing an image, claim once and run several sequentially
  (reset `/testbed` between tasks), or use claim TTL batching.
- **Impact:** Medium (fewer claim/replace cycles).
- **Effort:** Medium.
- **Open:** state bleed between tasks; correctness of a git reset vs fresh pod.

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
