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

"""Cluster placement policies.

A `Placement` decides which cluster handles a given image/task. All policies
skip clusters without spare capacity and raise `NoClusterAvailableError` if none
qualify.
"""

from __future__ import annotations

import hashlib
import itertools
from typing import Protocol

from .cluster import Cluster, ClusterRegistry
from .exceptions import NoClusterAvailableError


def _eligible(registry: ClusterRegistry, need: int = 1) -> list[Cluster]:
  elig = [c for c in registry if c.has_capacity(need)]
  if not elig:
    raise NoClusterAvailableError(
        "no cluster has capacity for this task (check max_replicas / load)")
  return elig


class Placement(Protocol):
  """Selects a cluster for an image."""

  def select(self, image: str, registry: ClusterRegistry) -> Cluster:
    ...


class RoundRobin:
  """Cycle through eligible clusters in registry order."""

  def __init__(self):
    self._counter = itertools.count()

  def select(self, image: str, registry: ClusterRegistry) -> Cluster:
    elig = _eligible(registry)
    return elig[next(self._counter) % len(elig)]


class LeastLoaded:
  """Pick the eligible cluster with the fewest active claims (ties: replicas)."""

  def select(self, image: str, registry: ClusterRegistry) -> Cluster:
    elig = _eligible(registry)
    return min(elig, key=lambda c: (c.active_claims, c.active_replicas))


class CapacityWeighted:
  """Pick the eligible cluster with the best weight-to-load ratio.

  Higher ``ClusterConfig.weight`` and lower current load win. Deterministic.
  """

  def select(self, image: str, registry: ClusterRegistry) -> Cluster:
    elig = _eligible(registry)
    return max(elig, key=lambda c: c.config.weight / (1 + c.active_replicas))


class ImageAffinity:
  """Route an image (hence its repo family) to a consistent cluster.

  Keeps a given image on the same cluster across calls so its warm pool and the
  node-cached image layers are reused. Falls back to `LeastLoaded` among eligible
  clusters when the affinity target is out of capacity.
  """

  def __init__(self):
    self._fallback = LeastLoaded()

  def select(self, image: str, registry: ClusterRegistry) -> Cluster:
    _eligible(registry)  # raises if none have capacity
    names = registry.names()
    idx = int(hashlib.md5(image.encode()).hexdigest(), 16) % len(names)
    target = registry.get(names[idx])
    if target.has_capacity(1):
      return target
    return self._fallback.select(image, registry)


_REGISTRY = {
    "round-robin": RoundRobin,
    "least-loaded": LeastLoaded,
    "capacity-weighted": CapacityWeighted,
    "image-affinity": ImageAffinity,
}


def get_placement(name: str) -> Placement:
  """Build a placement policy by name (see `config.PLACEMENTS`)."""
  try:
    return _REGISTRY[name]()
  except KeyError:
    raise ValueError(f"unknown placement '{name}'; choose from {sorted(_REGISTRY)}")
