# rl-tunix SWE-bench Warm Pools

This example shows how a reinforcement-learning / evaluation pipeline —
[**rl-tunix**](https://github.com/google/tunix) (the `tunix` post-training
library together with [`R2E-Gym`](https://github.com/R2E-Gym/R2E-Gym)) — uses
**Agent Sandbox warm pools** to run [SWE-bench](https://www.swebench.com/) tasks
at scale, securely and with low per-task startup latency.

Each SWE-bench task ships as its own multi-GB Docker image (the repository
checked out at a specific commit, with its toolchain installed). An RL learner
generates thousands of trajectories across hundreds of these images, so two
things matter: **isolation** (untrusted, model-generated code runs inside each
sandbox) and **startup latency** (a cold sandbox must pull a large image and
start a pod). Agent Sandbox `SandboxWarmPool`s solve the latter by keeping a
configurable number of sandboxes pre-warmed per task image; this example adds
the orchestration that decides *which* pools to keep warm, and *when*.

## Architecture

```mermaid
flowchart LR
    Learner["rl-tunix learner / eval"]
    subgraph Orchestration["run_swebench.py"]
      Strategy["warm-pool strategy<br/>(none / naive / sliding)"]
    end
    Learner --> Strategy
    Strategy -->|"create (v1beta1)"| Template[SandboxTemplate]
    Strategy -->|"create (v1beta1)"| Pool[SandboxWarmPool]
    Pool -->|references| Template
    Pool -->|pre-warms| WarmSb["Ready Sandboxes<br/>(per task image)"]
    Strategy -->|"create_sandbox(warmpool=...)"| SDK["Python SDK<br/>SandboxClient"]
    SDK -->|claims| WarmSb
    SDK -->|get_pod_name| Pod[Sandbox Pod]
    Strategy -->|kubectl exec| Pod
```

For each task the driver **claims** a pre-warmed sandbox from the image's pool
with the Python SDK (`client.create_sandbox(warmpool=...)`), runs a command
inside it, and **terminates** it. Commands run via `kubectl exec` (the
router-free path), so the [Sandbox Router](../python-sdk-quickstart/) is *not*
required.

## Warm-pool strategies

| Strategy | Behavior | Use case | Idle cost |
| :--- | :--- | :--- | :--- |
| `none` | Provision a size-1 pool on demand per task, tear it down after. | Debugging, tiny budgets. | Lowest (every task pays cold-start). |
| `naive` | Pre-warm a pool for **every** unique image up front; tear all down at the end. | Small batches, ample capacity, maximum task shuffle. | Highest (all pools idle together). |
| `sliding` | Sort tasks by image; keep only `WARMPOOL_WINDOW_SIZE` pools warm, rolling forward as each image's tasks complete. | Large, image-diverse batches with limited capacity. | Balanced. |

Per-image pool size is `min(tasks_for_image, MAX_WARMPOOL_SIZE)`.

## Prerequisites

- A Kubernetes cluster with the Agent Sandbox **controller and extensions**
  installed (the `SandboxTemplate` / `SandboxClaim` / `SandboxWarmPool` CRDs).
  See the [installation guide](../../README.md#installation).
- The Python SDK and deps: `pip install -r requirements.txt`
  (installs `k8s-agent-sandbox`, `kubernetes`, `datasets`).
- `kubectl` configured for the cluster (the SDK and driver use your kubeconfig).
- Optional: a Docker Hub pull secret named via `IMAGE_PULL_SECRET` if anonymous
  pulls of the SWE-bench images get rate-limited.
- Optional: a gVisor-enabled node pool for strong isolation (set
  `RUNTIME_CLASS=gvisor`).

> **Heads up:** SWE-bench images are multi-GB. On a small cluster keep
> `TASKS_LIMIT` and pool sizes tiny, and allow time for the first image pull
> (raise `SANDBOX_READY_TIMEOUT`).

## Run it and what to expect

First-time setup (once per cluster/shell):

```bash
kubectl apply -f manifests/namespace.yaml
pip install -r requirements.txt
```

### Option A — Python driver (all three strategies)

```bash
# Naive, a single task, on a standard node pool:
WARMPOOL_STRATEGY=naive \
TASKS_LIMIT=1 \
MAX_WARMPOOL_SIZE=1 \
NAMESPACE=rl-tunix-swebench \
NODE_SELECTOR_KEY=cloud.google.com/gke-nodepool \
NODE_SELECTOR_VAL=standard-pool \
SANDBOX_READY_TIMEOUT=1200 \
python run_swebench.py
```

**What the driver does, in order:** loads the dataset → creates a
`SandboxTemplate` + `SandboxWarmPool` for each task image → waits for the warm
pool to report `readyReplicas` (this is the slow step on a cold node: the
multi-GB image pull) → claims a pre-warmed sandbox per task with the SDK → execs
the probe command inside `/testbed` → terminates the sandbox → tears the pools
down → prints a JSON summary.

**Expected console output** (abridged; the first run is dominated by the image
pull, subsequent claims are seconds):

```text
INFO ... Loading dataset R2E-Gym/SWE-Bench-Verified [test]
INFO ... Loaded 1 tasks (1 unique images)
INFO ... Running 'naive' strategy over 1 tasks
INFO ... [naive] pre-warming slimshetty/swebench-verified:...astropy__astropy-12907 (replicas=1)
INFO ... Creating SandboxTemplate 'r2e-img-a8d0235275f3' ...
INFO ... Created SandboxWarmPool 'pool-r2e-img-a8d0235275f3' (replicas=1)
INFO ... WarmPool 'pool-r2e-img-a8d0235275f3': 0/1 ready
INFO ... WarmPool 'pool-r2e-img-a8d0235275f3': 1/1 ready
INFO ... [astropy__astropy-12907] pod=pool-r2e-img-a8d0235275f3-n5whj output=READY ...
INFO ... Deleted SandboxWarmPool 'pool-r2e-img-a8d0235275f3'
```

```json
{
  "strategy": "naive",
  "results": [
    {
      "instance_id": "astropy__astropy-12907",
      "docker_image": "slimshetty/swebench-verified:sweb.eval.x86_64.astropy__astropy-12907",
      "pod": "pool-r2e-img-a8d0235275f3-n5whj",
      "output": "READY pool-r2e-img-a8d0235275f3-n5whj\nd16bfe05a7 Merge pull request #12900 from Cadair/custom_compound_model",
      "elapsed_s": 2.2
    }
  ]
}
```

The `output` field is proof the real task environment is live: the git line is
the actual repository checked out at the task's commit under `/testbed`. After
the run, `kubectl get all,sandboxwarmpools,sandboxtemplates -n rl-tunix-swebench`
should be empty — the driver cleans up after itself.

**Watch it work** (in a second terminal, while the driver runs):

```bash
kubectl get pods,sandboxwarmpools -n rl-tunix-swebench -w
# pool-...-xxxxx   0/1   ContainerCreating   <- pulling the image
# pool-...-xxxxx   1/1   Running             <- pre-warmed, ready to claim
```

**Try the other strategies** (with a couple of tasks so the difference shows):

```bash
WARMPOOL_STRATEGY=sliding TASKS_LIMIT=4 WARMPOOL_WINDOW_SIZE=1 MAX_WARMPOOL_SIZE=2 \
  NAMESPACE=rl-tunix-swebench python run_swebench.py
WARMPOOL_STRATEGY=none     TASKS_LIMIT=2 \
  NAMESPACE=rl-tunix-swebench python run_swebench.py
```

What to expect from each:

| Strategy | What you'll see on the cluster | Trade-off |
| :--- | :--- | :--- |
| `naive` | All image pools appear up front and stay Ready until the end. | Fastest per-task claim; highest idle footprint. |
| `sliding` | Only `WARMPOOL_WINDOW_SIZE` pools exist at once; pools disappear as their image's tasks finish and the next image's pool appears. | Balanced footprint; tasks are run grouped by image. |
| `none` | A single size-1 pool blinks into existence per task, then is deleted. | Lowest idle footprint; every task pays the cold-start. |

### Option B — Pure kubectl (single image, no Python)

```bash
kubectl apply -f manifests/namespace.yaml
kubectl apply -f manifests/sandbox-template.example.yaml
kubectl apply -f manifests/sandboxwarmpool.example.yaml

kubectl get sandboxwarmpool -n rl-tunix-swebench -w   # wait for readyReplicas
kubectl get pods -n rl-tunix-swebench
```

Expect the warm pool's `READY` column to go to `2` once the image is pulled, and
two `pool-r2e-img-astropy-12907-xxxxx` pods `Running`. Tear it down:

```bash
kubectl delete -f manifests/sandboxwarmpool.example.yaml
kubectl delete -f manifests/sandbox-template.example.yaml
```

To pre-warm **multiple** images at once (the naive strategy by hand), apply the
whole `manifests/warmpools/` directory — each file is a `SandboxTemplate` +
`SandboxWarmPool` pair for one image:

```bash
kubectl apply -f manifests/warmpools/                  # one pool per image
kubectl get sandboxwarmpools -n rl-tunix-swebench -w   # each goes to READY
kubectl delete -f manifests/warmpools/
```

### Option C — Notebook

Open [`rl-tunix-swebench-demo.ipynb`](./rl-tunix-swebench-demo.ipynb) for an
interactive walk-through of all three strategies. The committed copy includes
the outputs from a real run (two `astropy` tasks) so you can see expected
results before running it yourself. Run it with the example directory as the
working directory so `import strategies, warmpool` resolves.

## Configuration

| Env var | Default | Description |
| :--- | :--- | :--- |
| `WARMPOOL_STRATEGY` | `naive` | `none`, `naive`, or `sliding`. |
| `WARMPOOL_WINDOW_SIZE` | `2` | (sliding) unique images kept warm concurrently. |
| `MAX_WARMPOOL_SIZE` | `8` | Cap on replicas per image pool. |
| `TASKS_LIMIT` | `1` | Number of tasks from the dataset (`0` = all). |
| `DATASET_NAME` | `R2E-Gym/SWE-Bench-Verified` | HF dataset with a `docker_image` column. |
| `DATASET_SPLIT` | `test` | Dataset split. |
| `NAMESPACE` | `rl-tunix-swebench` | Namespace for the CRs. |
| `NODE_SELECTOR_KEY` / `NODE_SELECTOR_VAL` | _(unset)_ | Optional node pinning. |
| `RUNTIME_CLASS` | _(unset)_ | e.g. `gvisor` for isolation. |
| `IMAGE_PULL_SECRET` | _(unset)_ | Optional Docker Hub pull secret. |
| `SANDBOX_READY_TIMEOUT` | `900` | Seconds to wait for a sandbox/pool to be ready. |

## Cleanup

The driver tears down everything it creates. To remove anything left over:

```bash
kubectl delete sandboxwarmpools,sandboxtemplates,sandboxclaims,sandboxes \
  --all -n rl-tunix-swebench
kubectl delete namespace rl-tunix-swebench
```

## Scaling notes

For production-scale runs (thousands of concurrent trajectories) the strategies
here pair with two infra optimizations from the rl-tunix design:

- **Image pre-pull** — pre-pull task images onto nodes (e.g. a `DaemonSet`)
  before the run so warm pods skip the multi-GB pull.
- **Proportional sizing** — size each image's pool to its share of the batch,
  `replicas_image ≈ GlobalConcurrency × tasks_image / tasks_total`, capped by
  `MAX_WARMPOOL_SIZE`.
- **Autoscaling** — combine with cluster autoscaler / capacity buffers, or scale
  pools on claim-rate metrics (see [`../hpa-swp-scaling`](../hpa-swp-scaling)).

## Relation to the rl-tunix branches

This example is a self-contained re-implementation of the warm-pool integration
prototyped in the `agentic-sandbox-integration` branches of `tunix`
(`examples/deepswe/eval_deepswe.py`) and `R2E-Gym` (the `kubernetes-sandbox`
backend in `agenthub/runtime/docker.py`). It differs from those prototypes in a
few deliberate ways so it runs against current Agent Sandbox:

- **API `v1alpha1` → `v1beta1`.** Warm pool spec fields are `replicas` /
  `sandboxTemplateRef` here (the prototype used `size` / `templateRef`).
- **Current Python SDK.** Sandboxes are claimed with
  `SandboxClient().create_sandbox(warmpool=...)`; the prototype used an older
  `SandboxClient(template_name=..., api_url=...)` context-manager API that no
  longer exists.
- **gVisor optional.** `runtimeClassName` is unset by default for portability;
  set `RUNTIME_CLASS=gvisor` on a gVisor-enabled pool.
- **No model in the loop.** The driver execs a lightweight probe command per
  task instead of running an RL agent, to keep the example focused on the
  warm-pool orchestration.

The original (v1alpha1) template from the R2E-Gym branch is kept verbatim under
[`manifests/reference/`](./manifests/reference/) for side-by-side comparison.
