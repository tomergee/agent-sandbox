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

"""agent-sandbox-rl: generic, multi-cluster batch orchestration for RL on
Agent Sandbox. See plans/agent-sandbox-rl-design.md.

Phase 1 surface: config models + replica sizing + constants/exceptions.
Cluster/resources/fleet land in later phases.
"""

from . import constants
from .cluster import Cluster, ClusterRegistry, build_api_client
from .config import ClusterConfig, FleetConfig, ResourceSpec, TemplateSpec
from .exceptions import (
    CapacityError,
    FleetError,
    NoClusterAvailableError,
    PreflightError,
)
from .async_fleet import AsyncSandboxFleet
from .fleet import FleetPlan, PlanEntry, SandboxFleet
from .handles import SandboxHandle
from .placement import (
    CapacityWeighted,
    ImageAffinity,
    LeastLoaded,
    RoundRobin,
    get_placement,
)
from .preflight import PreflightReport, preflight_cluster
from .prepull import prepull, prepull_delete
from .resources import Resources
from .sizing import compute_replicas, plan, recommend_window
from .sources import JsonlSource, ListSource, Task, TaskSource, to_tasks
from .strategies import STRATEGIES, process_parallel

__all__ = [
    "constants",
    # config
    "FleetConfig",
    "ClusterConfig",
    "TemplateSpec",
    "ResourceSpec",
    # sizing
    "compute_replicas",
    "recommend_window",
    "plan",
    # sources
    "Task",
    "TaskSource",
    "ListSource",
    "JsonlSource",
    "to_tasks",
    # cluster / resources
    "Resources",
    "Cluster",
    "ClusterRegistry",
    "build_api_client",
    # placement
    "get_placement",
    "RoundRobin",
    "LeastLoaded",
    "CapacityWeighted",
    "ImageAffinity",
    # fleet
    "SandboxFleet",
    "AsyncSandboxFleet",
    "FleetPlan",
    "PlanEntry",
    "SandboxHandle",
    # strategies
    "STRATEGIES",
    "process_parallel",
    # preflight / prepull
    "PreflightReport",
    "preflight_cluster",
    "prepull",
    "prepull_delete",
    # exceptions
    "FleetError",
    "PreflightError",
    "CapacityError",
    "NoClusterAvailableError",
]

__version__ = "0.1.0.dev0"
