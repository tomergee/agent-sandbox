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

"""`SandboxFleet` — the synchronous orchestrator.

Drives the full lifecycle across one or many clusters: load tasks → preflight →
plan (compute replicas) → ensure templates → start warm pools → acquire (claim a
sandbox per task, returning a `SandboxHandle` with a hostname/endpoint) →
release → teardown. Use the primitives directly from an RL loop, or the managed
`run()`. (Strategies + parallelism land in phase 4; async in phase 6.)
"""

from __future__ import annotations

import collections
import logging
import threading
from dataclasses import dataclass
from typing import Callable, Optional

from . import sizing
from .cluster import Cluster, ClusterRegistry
from .config import ClusterConfig, FleetConfig
from .exceptions import PreflightError
from .handles import SandboxHandle
from .placement import get_placement
from .sources import Task, to_tasks

logger = logging.getLogger("agent_sandbox_rl.fleet")


@dataclass
class PlanEntry:
  """One image's provisioning plan on a chosen cluster."""

  cluster: str
  image: str
  template: str
  pool: str
  replicas: int
  tasks: int


class FleetPlan:
  """The result of `SandboxFleet.plan()`: per-image placement + sizing."""

  def __init__(self, entries: list[PlanEntry]):
    self.entries = entries
    self._by_image = {e.image: e for e in entries}

  def for_image(self, image: str) -> Optional[PlanEntry]:
    return self._by_image.get(image)

  @property
  def total_replicas(self) -> int:
    return sum(e.replicas for e in self.entries)

  def by_cluster(self) -> "dict[str, list[PlanEntry]]":
    out: dict[str, list[PlanEntry]] = collections.defaultdict(list)
    for e in self.entries:
      out[e.cluster].append(e)
    return dict(out)


class SandboxFleet:
  """Synchronous multi-cluster warm-pool orchestrator."""

  def __init__(self, config: FleetConfig | None = None,
               registry: ClusterRegistry | None = None):
    self.config = config or FleetConfig()
    self.registry = registry or self._default_registry(self.config)
    self.placement = get_placement(self.config.placement)
    self.tasks: list[Task] = []
    self.plan_: FleetPlan | None = None
    self._handles: list[SandboxHandle] = []
    self._warmed: dict[str, int] = {}        # image -> replicas currently warmed
    self._lock = threading.Lock()            # guards bookkeeping under parallel run

  @staticmethod
  def _default_registry(config: FleetConfig) -> ClusterRegistry:
    if config.clusters:
      return ClusterRegistry.from_configs(config.clusters, labels=config.labels)
    # Single cluster from the ambient kube context.
    return ClusterRegistry([Cluster(ClusterConfig(), labels=config.labels)])

  # --- inputs ------------------------------------------------------------ #
  def load_tasks(self, source) -> list[Task]:
    self.tasks = to_tasks(source)
    logger.info("Loaded %d tasks (%d unique images)",
                len(self.tasks), len({t.image for t in self.tasks}))
    return self.tasks

  def image_counts(self) -> "collections.OrderedDict[str, int]":
    counts: "collections.OrderedDict[str, int]" = collections.OrderedDict()
    for t in self.tasks:
      counts[t.image] = counts.get(t.image, 0) + 1
    return counts

  # --- preflight / plan -------------------------------------------------- #
  def preflight(self) -> dict[str, bool]:
    """Verify each cluster is reachable and the extension CRDs are installed."""
    report: dict[str, bool] = {}
    for c in self.registry:
      try:
        c.resources.list_warmpools()  # touches the v1beta1 CRD
      except Exception as e:  # noqa: BLE001
        raise PreflightError(f"cluster {c.name!r} preflight failed: {e}") from e
      report[c.name] = True
    logger.info("Preflight OK on %d cluster(s): %s",
                len(report), ", ".join(report))
    return report

  def plan(self) -> FleetPlan:
    """Assign each unique image to a cluster (placement) and size its pool."""
    counts = self.image_counts()
    # image -> cluster (each unique image placed once).
    assigned: "collections.OrderedDict[str, Cluster]" = collections.OrderedDict()
    for image in counts:
      assigned[image] = self.placement.select(image, self.registry)
    # per-cluster totals for proportional sizing.
    cluster_totals: dict[str, int] = collections.defaultdict(int)
    for image, c in assigned.items():
      cluster_totals[c.name] += counts[image]

    entries: list[PlanEntry] = []
    for image, c in assigned.items():
      replicas = sizing.compute_replicas(
          counts[image], cluster_totals[c.name],
          self.config.max_concurrent, self.config.max_warmpool_size)
      template = self.config.template_name(image)
      entries.append(PlanEntry(
          cluster=c.name, image=image, template=template,
          pool=f"pool-{template}", replicas=replicas, tasks=counts[image]))
    self.plan_ = FleetPlan(entries)
    logger.info("Plan: %d images across %d cluster(s), %d total warm replicas",
                len(entries), len(self.plan_.by_cluster()),
                self.plan_.total_replicas)
    return self.plan_

  # --- provisioning ------------------------------------------------------ #
  def _ensure_pool(self, cluster: Cluster, image: str, replicas: int) -> str:
    template = self.config.template_name(image)
    pool = f"pool-{template}"
    cluster.resources.ensure_template(
        image, template, cluster.template_spec(self.config.template))
    cluster.resources.create_warmpool(pool, template, replicas)
    return pool

  def ensure_templates(self) -> None:
    plan = self.plan_ or self.plan()
    for e in plan.entries:
      c = self.registry.get(e.cluster)
      c.resources.ensure_template(
          e.image, e.template, c.template_spec(self.config.template))

  def start_warmpools(self, wait: bool = True) -> None:
    plan = self.plan_ or self.plan()
    for e in plan.entries:
      c = self.registry.get(e.cluster)
      c.resources.ensure_template(
          e.image, e.template, c.template_spec(self.config.template))
      c.resources.create_warmpool(e.pool, e.template, e.replicas)
      c.active_replicas += e.replicas
      if wait:
        c.resources.wait_for_pool_ready(
            e.pool, e.replicas, timeout=self.config.ready_timeout)

  def warm_image(self, image: str, *, replicas_override: int | None = None,
                 wait: bool = True) -> None:
    """Warm one image's pool (used by sliding/none to bound the footprint)."""
    entry = (self.plan_ or self.plan()).for_image(image)
    if entry is None:
      raise KeyError(f"image not in plan: {image}")
    c = self.registry.get(entry.cluster)
    reps = replicas_override if replicas_override is not None else entry.replicas
    c.resources.ensure_template(
        image, entry.template, c.template_spec(self.config.template))
    c.resources.create_warmpool(entry.pool, entry.template, reps)
    with self._lock:
      c.active_replicas += reps
      self._warmed[image] = reps
    if wait:
      c.resources.wait_for_pool_ready(
          entry.pool, reps, timeout=self.config.ready_timeout)

  def unwarm_image(self, image: str) -> None:
    """Tear down one image's pool + template."""
    entry = (self.plan_ or self.plan()).for_image(image)
    if entry is None:
      return
    c = self.registry.get(entry.cluster)
    c.resources.delete_warmpool(entry.pool)
    c.resources.delete_template(entry.template)
    with self._lock:
      reps = self._warmed.pop(image, entry.replicas)
      c.active_replicas = max(0, c.active_replicas - reps)

  def setup(self) -> "SandboxFleet":
    """preflight → plan → ensure templates → start (and wait for) warm pools."""
    self.preflight()
    self.plan()
    self.start_warmpools(wait=True)
    return self

  # --- claims ------------------------------------------------------------ #
  def acquire(self, task: Task) -> SandboxHandle:
    """Claim a sandbox for ``task`` and return a `SandboxHandle`."""
    entry = self.plan_.for_image(task.image) if self.plan_ else None
    if entry is not None:
      cluster = self.registry.get(entry.cluster)
      pool = entry.pool
    else:
      cluster = self.placement.select(task.image, self.registry)
      pool = self._ensure_pool(cluster, task.image, 1)
      with self._lock:
        cluster.active_replicas += 1

    sandbox = cluster.sandbox_client.create_sandbox(
        warmpool=pool, namespace=cluster.namespace,
        sandbox_ready_timeout=self.config.ready_timeout)
    pod = sandbox.get_pod_name()
    try:
      pod_ip = sandbox.get_pod_ip()
    except Exception:  # noqa: BLE001
      pod_ip = None
    handle = SandboxHandle(
        task=task, cluster_name=cluster.name, claim_name=sandbox.claim_name,
        sandbox_id=sandbox.sandbox_id, pod_name=pod, hostname=sandbox.sandbox_id,
        pod_ip=pod_ip, sandbox=sandbox, _cluster=cluster)
    with self._lock:
      cluster.active_claims += 1
      self._handles.append(handle)
    return handle

  def acquire_batch(self, tasks: list[Task]) -> list[SandboxHandle]:
    return [self.acquire(t) for t in tasks]

  def handles(self) -> list[SandboxHandle]:
    return list(self._handles)

  def hostnames(self) -> list[str]:
    return [h.hostname for h in self._handles]

  def endpoints(self, port: int = 8888) -> list[str]:
    return [h.endpoint(port) for h in self._handles]

  def release(self, handle: SandboxHandle) -> None:
    handle.release()
    with self._lock:
      if handle in self._handles:
        self._handles.remove(handle)
      c = self.registry.get(handle.cluster_name)
      if c.active_claims > 0:
        c.active_claims -= 1

  def release_all(self) -> None:
    for h in list(self._handles):
      self.release(h)

  # --- teardown ---------------------------------------------------------- #
  def teardown(self, delete_namespace: bool = False) -> None:
    """Release all claims and delete every resource this fleet created."""
    self.release_all()
    sel = None
    for c in self.registry:
      sel = c.resources.managed_selector()
      for pool in c.resources.list_warmpools(label_selector=sel):
        c.resources.delete_warmpool(pool)
      for tmpl in c.resources.list_templates(label_selector=sel):
        c.resources.delete_template(tmpl)
      c.active_replicas = 0
      c.active_claims = 0
      if delete_namespace:
        try:
          c.core_api.delete_namespace(c.namespace)
        except Exception:  # noqa: BLE001
          pass
    self.plan_ = None

  def __enter__(self) -> "SandboxFleet":
    return self.setup()

  def __exit__(self, *exc) -> None:
    self.teardown()

  # --- managed runner ---------------------------------------------------- #
  def run(self, process_fn: Callable[[Task, SandboxHandle], object],
          strategy: str = "naive", concurrency: int | None = None) -> list:
    """Run all loaded tasks under ``strategy`` (none|naive|sliding) with up to
    ``concurrency`` parallel claim+exec (defaults to ``config.max_concurrent``).
    Returns one result per task (a per-task exception is captured, not raised).
    """
    from .strategies import STRATEGIES
    if strategy not in STRATEGIES:
      raise ValueError(f"unknown strategy '{strategy}'; choose from {sorted(STRATEGIES)}")
    conc = concurrency or self.config.max_concurrent
    return STRATEGIES[strategy](self, process_fn, conc)
