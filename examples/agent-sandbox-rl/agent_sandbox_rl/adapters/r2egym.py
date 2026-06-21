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

"""R2E-Gym adapter: run R2E-Gym's `RepoEnv` on a fleet-pre-warmed sandbox.

R2E-Gym's `kubernetes-sandbox` backend cold-creates a sandbox per env (via the
SDK's old context-manager `SandboxClient`). This adapter instead **binds** an
already-warm pod acquired from an `agent_sandbox_rl` `SandboxFleet` into R2E-Gym's
`DockerRuntime`/`RepoEnv`, so SWE-bench rollouts reuse the warm pool (and, since
tunix deepswe drives R2E-Gym, tunix benefits transitively).

Usage::

    from agent_sandbox_rl import SandboxFleet, FleetConfig
    from agent_sandbox_rl.adapters.swebench import SweBenchSource
    from agent_sandbox_rl.adapters.r2egym import make_fleet_repo_env, r2egym_command_files

    fleet = SandboxFleet(FleetConfig(...))
    fleet.load_tasks(SweBenchSource(limit=5, keep_row=True))   # keep_row REQUIRED

    def rollout(task, handle):
        env = make_fleet_repo_env(handle, command_files=r2egym_command_files())
        try:
            instruction = env.get_task_instruction()
            obs, reward, done, info = env.step(some_action)
            return {"id": task.id, "obs": str(obs)}
        finally:
            env.close()          # safe no-op teardown; does NOT delete the pod

    results = fleet.run(rollout, strategy="sliding")

**Ownership:** the env never deletes the pod — `env.close()` is a no-op. The fleet
owns the pod's lifecycle; `fleet.run`/`fleet.release(handle)` frees it. Namespace
flows automatically from the handle's cluster (`ClusterConfig.namespace`).

Requires R2E-Gym (`pip install r2egym`, or `pip install -e` the R2E-Gym checkout).
Importing this module is cheap; the R2E-Gym subclasses are built lazily on first
use so the core package imports fine without R2E-Gym installed.
"""

from __future__ import annotations

import contextlib
import logging

logger = logging.getLogger("agent_sandbox_rl.adapters.r2egym")

_HINT = (
    "requires R2E-Gym — `pip install r2egym` (or `pip install -e` the R2E-Gym "
    "checkout). See examples/rl_integration.md."
)

# Memoized (FleetDockerRuntime, FleetRepoEnv) so isinstance() stays valid and the
# subclasses are built only once, on first use.
_CLASSES = None


def _import_r2egym():
  try:
    from r2egym.agenthub.environment.env import EnvArgs, RepoEnv  # noqa: F401
    from r2egym.agenthub.runtime import docker as docker_mod
    return docker_mod, EnvArgs, RepoEnv
  except ImportError as e:  # pragma: no cover - exercised via sys.modules in tests
    raise RuntimeError(f"agent_sandbox_rl.adapters.r2egym {_HINT}") from e


def _build_classes():
  """Build (and memoize) the R2E-Gym subclasses. Raises RuntimeError w/o r2egym."""
  global _CLASSES
  if _CLASSES is not None:
    return _CLASSES

  from kubernetes import client as k8s
  docker_mod, _EnvArgs, RepoEnv = _import_r2egym()
  DockerRuntime = docker_mod.DockerRuntime

  class FleetDockerRuntime(DockerRuntime):
    """A `DockerRuntime` bound to a fleet-warmed pod instead of cold-creating one.

    Constructed from an `agent_sandbox_rl.SandboxHandle`; overrides only the
    backend lifecycle hooks so the rest of R2E-Gym's runtime (exec, file copy,
    setup_env, reward) works unchanged against the warm pod.
    """

    def __init__(self, handle, *, command=("/bin/bash", "-l"), logger=None,
                 **docker_kwargs):
      self._handle = handle
      ds = (getattr(handle.task, "metadata", None) or {}).get("ds")
      if ds is None:
        raise RuntimeError(
            "Task.metadata['ds'] is required for the R2E-Gym adapter — load "
            "tasks with SweBenchSource(keep_row=True).")
      super().__init__(
          ds=ds, docker_image=handle.task.image, command=list(command),
          logger=logger, backend="kubernetes-sandbox", **docker_kwargs)

    @property
    def _ns(self) -> str:
      return self._handle._cluster.namespace

    @contextlib.contextmanager
    def _ns_patched(self):
      """Point the base methods' module-level DEFAULT_NAMESPACE at our handle's
      namespace for the duration of a base call (base hardcodes "default")."""
      old = docker_mod.DEFAULT_NAMESPACE
      docker_mod.DEFAULT_NAMESPACE = self._ns
      try:
        yield
      finally:
        docker_mod.DEFAULT_NAMESPACE = old

    def _start_kubernetes_sandbox(self):
      # Bind to the warm pod from the fleet handle — no SandboxClient, no template.
      cl = self._handle._cluster
      self.sb_client = None
      self.custom_api = None
      self.container_name = self._handle.pod_name
      # Per-runtime CoreV1Api (mirrors SandboxHandle.exec's isolation): the
      # kubernetes stream() websocket exec is not thread-safe across a shared
      # ApiClient, so each runtime gets its own.
      self.client = k8s.CoreV1Api(k8s.ApiClient(cl.api_client.configuration))
      self.container = self.client.read_namespaced_pod(
          name=self.container_name, namespace=cl.namespace)
      self.logger.info("Bound warm pod '%s' (ns=%s) from fleet handle",
                       self.container_name, cl.namespace)

    def _stop_kubernetes_sandbox(self):
      # The fleet owns the pod; release happens via fleet.release(handle).
      self.logger.debug("FleetDockerRuntime: teardown is a no-op (fleet owns "
                        "pod '%s')", getattr(self, "container_name", "?"))

    def _run_kubernetes(self, *args, **kwargs):
      with self._ns_patched():
        return super()._run_kubernetes(*args, **kwargs)

    def _copy_to_container_kubernetes(self, *args, **kwargs):
      with self._ns_patched():
        return super()._copy_to_container_kubernetes(*args, **kwargs)

  class FleetRepoEnv(RepoEnv):
    """`RepoEnv` whose runtime is a `FleetDockerRuntime` (no cold pod start).

    Mirrors `RepoEnv.__init__` but swaps the runtime construction so no throwaway
    sandbox is created. One episode per acquired handle is the intended pattern.
    """

    def __init__(self, handle, args, *, logger=None, verbose=True,
                 step_timeout=90, reward_timeout=300):
      if logger is None:
        from r2egym.agenthub.utils.log import get_logger
        self.logger = get_logger("FleetRepoEnv")
      else:
        self.logger = logger
      if not verbose:
        self.logger.setLevel(logging.CRITICAL)

      self.runtime = FleetDockerRuntime(handle, logger=self.logger)

      self.args = args
      self.done = False
      self.observation = None
      self.state = None
      from r2egym.agenthub.agent.commands import ParseCommandBash
      self.cmd_parser = ParseCommandBash()
      self.backend = "kubernetes-sandbox"
      self.step_timeout = step_timeout
      self.reward_timeout = reward_timeout
      self.logger.info("Initialized FleetRepoEnv: %s image: %s",
                       self.runtime.repo_name, self.runtime.docker_image)

    def reset(self):
      """Soft reset: re-bind to the same warm pod (no cold start, no setup rerun).

      For a fresh episode, prefer `fleet.release(handle)` + `fleet.acquire(task)`
      to get a clean pod.
      """
      self.logger.info("FleetRepoEnv soft reset (re-binding warm pod)")
      self.runtime.start_container(
          self.runtime.docker_image, self.runtime.command,
          self.runtime.container_name)
      self.observation = "Environment reset"
      self.state = None
      self.done = False
      return self.observation

  _CLASSES = (FleetDockerRuntime, FleetRepoEnv)
  return _CLASSES


def make_fleet_repo_env(handle, *, command_files=None, verbose=False,
                        step_timeout: int = 90, reward_timeout: int = 300):
  """Build an R2E-Gym `RepoEnv` bound to a fleet-warmed pod (`handle`).

  ``handle`` must come from a fleet whose tasks were loaded with
  ``SweBenchSource(keep_row=True)`` (the env/reward grading need the full dataset
  row in ``task.metadata['ds']``). ``command_files`` are R2E-Gym tool files copied
  into the pod (see `r2egym_command_files`). Call ``env.close()`` when done — it is
  a no-op that does NOT delete the pod; the fleet releases it.
  """
  _, FleetRepoEnv = _build_classes()
  from r2egym.agenthub.environment.env import EnvArgs
  ds = (getattr(handle.task, "metadata", None) or {}).get("ds")
  if ds is None:
    raise RuntimeError(
        "Task.metadata['ds'] is required for the R2E-Gym adapter — load tasks "
        "with SweBenchSource(keep_row=True).")
  env = FleetRepoEnv(handle, EnvArgs(ds=ds), verbose=verbose,
                     step_timeout=step_timeout, reward_timeout=reward_timeout)
  if command_files:
    env.add_commands(list(command_files))
  return env


def r2egym_command_files() -> list:
  """The default R2E-Gym (`r2egym` scaffold) tool files to load into a sandbox.

  Mirrors tunix deepswe's ``R2EGYM_COMMAND_FILES`` (derived from the installed
  ``r2egym`` package). Requires R2E-Gym installed.
  """
  import os
  try:
    import r2egym
  except ImportError as e:
    raise RuntimeError(f"r2egym_command_files {_HINT}") from e
  base = os.path.join(os.path.dirname(r2egym.__file__), "agenthub", "tools")
  return [
      os.path.join(base, "r2egym", "file_editor.py"),
      os.path.join(base, "search.py"),
      os.path.join(base, "r2egym", "execute_bash.py"),
      os.path.join(base, "finish.py"),
  ]


def __getattr__(name):
  """Lazily expose the R2E-Gym subclasses (PEP 562) so e.g.
  ``from agent_sandbox_rl.adapters.r2egym import FleetRepoEnv`` triggers the build
  (and a clear RuntimeError if R2E-Gym is missing)."""
  if name == "FleetDockerRuntime":
    return _build_classes()[0]
  if name == "FleetRepoEnv":
    return _build_classes()[1]
  raise AttributeError(f"module {__name__!r} has no attribute {name!r}")
