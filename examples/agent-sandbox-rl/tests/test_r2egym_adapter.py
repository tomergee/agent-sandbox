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

"""R2E-Gym adapter tests — run with NO cluster and NO real r2egym.

Two layers: (1) the lazy guard raises cleanly when r2egym is absent; (2) the
override logic (warm-pod binding, no-op teardown, namespace forwarding,
no-cold-start) is unit-tested by injecting a fake r2egym base into sys.modules.
"""

import sys
import types
from unittest.mock import MagicMock

import pytest

from agent_sandbox_rl.adapters import r2egym as adapter
from agent_sandbox_rl.handles import SandboxHandle
from agent_sandbox_rl.sources import Task


def _reset_classes():
  adapter._CLASSES = None


# --- layer 1: guard with r2egym absent ------------------------------------ #
def test_guard_without_r2egym(monkeypatch):
  _reset_classes()
  # Force the import to fail even if r2egym were installed.
  monkeypatch.setitem(sys.modules, "r2egym", None)
  with pytest.raises(RuntimeError, match="R2E-Gym"):
    adapter._build_classes()
  with pytest.raises(RuntimeError, match="R2E-Gym"):
    _ = adapter.FleetRepoEnv  # PEP 562 lazy access
  _reset_classes()


# --- fake r2egym base for layer 2 ----------------------------------------- #
class _FakeBaseRuntime:
  """Stand-in for r2egym DockerRuntime: dispatches lifecycle like the real one,
  records the module-level DEFAULT_NAMESPACE seen during exec/copy."""

  last_run_ns = None
  last_copy_ns = None

  def __init__(self, ds, docker_image=None, command=None, logger=None,
               backend="docker", **kw):
    self.ds = ds
    self.docker_image = docker_image
    self.command = command
    self.logger = logger or MagicMock()
    self.backend = backend
    self.repo_name = "repo"
    self.docker_kwargs = kw
    self.container = None
    self.container_name = None
    self.deleted = False
    self.start_container(docker_image, command, None)   # → _start_kubernetes_sandbox
    self.setup_env()

  def start_container(self, image, command, name, **kw):
    if self.backend == "kubernetes-sandbox":
      self._start_kubernetes_sandbox()

  def stop_container(self):
    if self.backend == "kubernetes-sandbox":
      self._stop_kubernetes_sandbox()

  def close(self):
    self.stop_container()

  def setup_env(self):
    pass

  def add_commands(self, files):
    for f in files:
      self._copy_to_container_kubernetes(f, "/usr/local/bin/x")

  def get_task_instruction(self):
    return "instruction"

  # overridden by the adapter subclass:
  def _start_kubernetes_sandbox(self):
    raise NotImplementedError

  def _stop_kubernetes_sandbox(self):
    raise NotImplementedError

  def _run_kubernetes(self, code, timeout=900, args="", workdir=""):
    _FakeBaseRuntime.last_run_ns = sys.modules[
        "r2egym.agenthub.runtime.docker"].DEFAULT_NAMESPACE
    return (f"ran:{code}", "0")

  def _copy_to_container_kubernetes(self, src, dest):
    _FakeBaseRuntime.last_copy_ns = sys.modules[
        "r2egym.agenthub.runtime.docker"].DEFAULT_NAMESPACE


class _FakeBaseRepoEnv:
  def add_commands(self, files):
    self.runtime.add_commands(files)


class _FakeEnvArgs:
  def __init__(self, ds=None, repo_path=None, docker_image=None):
    self.ds = ds


class _FakeParseCommandBash:
  pass


def _install_fake_r2egym(monkeypatch):
  """Install fake r2egym.* modules so the adapter's subclasses can be built
  without the real package or a cluster."""
  def mod(name):
    m = types.ModuleType(name)
    monkeypatch.setitem(sys.modules, name, m)
    return m

  mod("r2egym")
  mod("r2egym.agenthub")
  mod("r2egym.agenthub.environment")
  env_mod = mod("r2egym.agenthub.environment.env")
  env_mod.EnvArgs = _FakeEnvArgs
  env_mod.RepoEnv = _FakeBaseRepoEnv
  mod("r2egym.agenthub.runtime")
  docker_mod = mod("r2egym.agenthub.runtime.docker")
  docker_mod.DockerRuntime = _FakeBaseRuntime
  docker_mod.DEFAULT_NAMESPACE = "default"
  docker_mod.CMD_TIMEOUT = 900
  log_mod = mod("r2egym.agenthub.utils")
  log_sub = mod("r2egym.agenthub.utils.log")
  log_sub.get_logger = lambda *a, **k: MagicMock()
  mod("r2egym.agenthub.agent")
  cmd_mod = mod("r2egym.agenthub.agent.commands")
  cmd_mod.ParseCommandBash = _FakeParseCommandBash
  return docker_mod


@pytest.fixture
def fake_r2egym(monkeypatch):
  _reset_classes()
  _FakeBaseRuntime.last_run_ns = None
  _FakeBaseRuntime.last_copy_ns = None
  docker_mod = _install_fake_r2egym(monkeypatch)
  # patch the kubernetes client the adapter builds per-runtime
  fake_pod = object()
  fake_core = MagicMock()
  fake_core.read_namespaced_pod.return_value = fake_pod
  monkeypatch.setattr("kubernetes.client.ApiClient", lambda cfg: MagicMock())
  monkeypatch.setattr("kubernetes.client.CoreV1Api", lambda api=None: fake_core)
  yield types.SimpleNamespace(docker_mod=docker_mod, core=fake_core, pod=fake_pod)
  _reset_classes()


def _handle(ns="rl-ns", with_ds=True):
  cluster = types.SimpleNamespace(
      name="c1", namespace=ns,
      api_client=types.SimpleNamespace(configuration=object()),
      core_api=MagicMock())
  meta = {"repo": "django/django"}
  if with_ds:
    meta["ds"] = {"instance_id": "x", "docker_image": "img", "repo": "django/django"}
  task = Task(id="x", image="img:latest", metadata=meta)
  return SandboxHandle(task=task, cluster_name="c1", claim_name="claim-1",
                       sandbox_id="sb-1", pod_name="pod-1", hostname="sb-1",
                       pod_ip="10.0.0.1", sandbox=None, _cluster=cluster)


# --- layer 2: override logic ---------------------------------------------- #
def test_start_binds_warm_pod(fake_r2egym):
  FleetDockerRuntime, _ = adapter._build_classes()
  rt = FleetDockerRuntime(_handle(ns="rl-ns"))
  assert rt.container_name == "pod-1"
  assert rt.sb_client is None and rt.custom_api is None
  assert rt.container is fake_r2egym.pod
  fake_r2egym.core.read_namespaced_pod.assert_called_once()
  _, kwargs = fake_r2egym.core.read_namespaced_pod.call_args
  assert kwargs["name"] == "pod-1" and kwargs["namespace"] == "rl-ns"


def test_stop_is_noop(fake_r2egym):
  FleetDockerRuntime, _ = adapter._build_classes()
  rt = FleetDockerRuntime(_handle())
  rt._stop_kubernetes_sandbox()
  rt.stop_container()
  # no delete API ever touched on the per-runtime client
  assert not fake_r2egym.core.delete_namespaced_pod.called


def test_run_uses_handle_namespace(fake_r2egym):
  FleetDockerRuntime, _ = adapter._build_classes()
  rt = FleetDockerRuntime(_handle(ns="rl-ns"))
  out, code = rt._run_kubernetes("echo hi", workdir="/testbed")
  assert _FakeBaseRuntime.last_run_ns == "rl-ns"     # not "default"
  assert (out, code) == ("ran:echo hi", "0")
  # and the global is restored after the call
  assert fake_r2egym.docker_mod.DEFAULT_NAMESPACE == "default"


def test_copy_uses_handle_namespace(fake_r2egym):
  FleetDockerRuntime, _ = adapter._build_classes()
  rt = FleetDockerRuntime(_handle(ns="rl-ns"))
  rt._copy_to_container_kubernetes("/src", "/dst")
  assert _FakeBaseRuntime.last_copy_ns == "rl-ns"
  assert fake_r2egym.docker_mod.DEFAULT_NAMESPACE == "default"


def test_make_fleet_repo_env_single_runtime_no_cold_start(fake_r2egym):
  env = adapter.make_fleet_repo_env(_handle(), command_files=["/a.py", "/b.py"])
  # exactly one pod bound (the warm one); no second/cold DockerRuntime
  assert fake_r2egym.core.read_namespaced_pod.call_count == 1
  assert env.runtime.container_name == "pod-1"
  assert env.backend == "kubernetes-sandbox"
  # command files copied via the namespace-correct path
  assert _FakeBaseRuntime.last_copy_ns == "rl-ns"


def test_make_fleet_repo_env_requires_ds(fake_r2egym):
  with pytest.raises(RuntimeError, match="keep_row=True"):
    adapter.make_fleet_repo_env(_handle(with_ds=False))
