# Copyright 2026 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# %% [markdown]
# # DeepSWE eval on Agent Sandbox warm pools (no model)
#
# Runs the **R2E-Gym** SWE environment — the same `RepoEnv` that tunix deepswe
# drives — on sandboxes pre-warmed by an `agent-sandbox-rl` `SandboxFleet`,
# instead of cold-creating one per task. No model / TPU required: a **stub
# policy** exercises the env path so you can validate and benchmark the
# infrastructure (warm pools, sizing, claim latency, observability) on its own.
#
# - **Tier B** (R2E-Gym installed): build a `FleetRepoEnv` bound to a warm pod,
#   read the task instruction, run one stub action, score-free. This is the real
#   integration path.
# - **Tier A** (R2E-Gym absent): fall back to the router-free probe via
#   `handle.exec`. The fleet/warm-pool path is identical; only the in-sandbox
#   work differs.
#
# A real rollout swaps the stub for `env.step(model_action)` and (optionally)
# `env.compute_reward()`. See `rl_integration.md`.
#
# Jupytext `# %%` cells — open directly in Jupyter, or `jupytext --to notebook`.

# %%
import json
import logging
import os

from agent_sandbox_rl import (
    ClusterConfig,
    FleetConfig,
    SandboxFleet,
    TemplateSpec,
)
from agent_sandbox_rl.adapters.swebench import SWEBENCH_PROBE, SweBenchSource

logging.basicConfig(
    level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")

# %% [markdown]
# ## Configuration (env-overridable)

# %%
NAMESPACE = os.getenv("NAMESPACE", "rl-tunix-swebench")
TASKS_LIMIT = int(os.getenv("TASKS_LIMIT", "5"))
STRATEGY = os.getenv("WARMPOOL_STRATEGY", "sliding")
MAX_CONCURRENT = int(os.getenv("MAX_CONCURRENT", "5"))
MAX_WARMPOOL_SIZE = int(os.getenv("MAX_WARMPOOL_SIZE", "8"))
READY_TIMEOUT = int(os.getenv("SANDBOX_READY_TIMEOUT", "1200"))
RUN_REWARD = os.getenv("RUN_REWARD", "0") == "1"   # off: compute_reward runs real tests (slow)
REPORT_DIR = os.getenv("REPORT_DIR", "")

node_selector = None
if os.getenv("NODE_SELECTOR_KEY") and os.getenv("NODE_SELECTOR_VAL"):
  node_selector = {os.environ["NODE_SELECTOR_KEY"]: os.environ["NODE_SELECTOR_VAL"]}

# %% [markdown]
# ## Build the fleet and load SWE-bench tasks
#
# `keep_row=True` is required by the R2E-Gym adapter (the env / reward grading
# need the full dataset row, stored under `task.metadata["ds"]`).

# %%
fleet = SandboxFleet(FleetConfig(
    clusters=[ClusterConfig(name="default", namespace=NAMESPACE)],
    max_concurrent=MAX_CONCURRENT,
    max_warmpool_size=MAX_WARMPOOL_SIZE,
    ready_timeout=READY_TIMEOUT,
    template=TemplateSpec(node_selector=node_selector),
))
fleet.load_tasks(SweBenchSource(limit=TASKS_LIMIT, keep_row=True))
print(f"loaded {len(fleet.tasks)} tasks across "
      f"{len(fleet.image_counts())} images")

# %% [markdown]
# ## Pick the per-task work: R2E-Gym env (Tier B) or exec probe (Tier A)

# %%
try:
  from agent_sandbox_rl.adapters.r2egym import (
      make_fleet_repo_env,
      r2egym_command_files,
  )
  _ = r2egym_command_files()          # forces the r2egym import; raises if absent
  HAVE_R2EGYM = True
except Exception as e:  # noqa: BLE001
  HAVE_R2EGYM = False
  print(f"[Tier A] R2E-Gym not available ({e}); using exec-only probe. "
        f"`pip install -e ../../../R2E-Gym` for the full RepoEnv path.")


def rollout(task, handle):
  """Stub policy. Real rollouts replace the body with env.step(model_action)."""
  if not HAVE_R2EGYM:
    out = handle.exec(SWEBENCH_PROBE).strip()
    return {"id": task.id, "tier": "A", "probe": out.splitlines()[:1]}

  env = make_fleet_repo_env(handle, command_files=r2egym_command_files(),
                            verbose=False)
  try:
    instruction = env.get_task_instruction()
    # Stub action via the real R2E-Gym runtime exec path (namespace-correct,
    # bound to the warm pod). A policy would instead call env.step(action).
    listing, _code = env.runtime.run("ls /testbed | head -n 5")
    result = {"id": task.id, "tier": "B",
              "instruction_chars": len(instruction or ""),
              "testbed_head": listing.strip().splitlines()[:5]}
    if RUN_REWARD:
      result["reward"] = env.compute_reward()    # runs the real test suite (slow)
    return result
  finally:
    env.close()        # no-op teardown; the fleet releases the pod


# %% [markdown]
# ## Run all tasks under the warm-pool strategy

# %%
try:
  results = fleet.run(rollout, strategy=STRATEGY, concurrency=MAX_CONCURRENT)
  print("\n=== results ===")
  for r in results:
    print(" ", r)
  print("\n=== report ===")
  print(fleet.report.summary())
finally:
  # fleet.run already tears down; this is belt-and-suspenders for partial runs.
  if fleet.plan_ is not None:
    fleet.teardown()

# %% [markdown]
# ## Persist the RunReport (optional)

# %%
if REPORT_DIR and fleet.report is not None:
  import datetime
  import pathlib

  out = pathlib.Path(REPORT_DIR)
  out.mkdir(parents=True, exist_ok=True)
  stamp = datetime.datetime.now().strftime("%Y%m%d-%H%M%S")
  base = out / f"deepswe_{STRATEGY}_{len(fleet.tasks)}tasks_{stamp}"
  base.with_suffix(".txt").write_text(fleet.report.summary() + "\n")
  base.with_suffix(".json").write_text(
      json.dumps(fleet.report.to_dict(), indent=2) + "\n")
  print(f"wrote {base}.txt / .json")
