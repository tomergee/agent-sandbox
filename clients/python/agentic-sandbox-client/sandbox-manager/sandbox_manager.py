# Copyright 2025 The Kubernetes Authors.
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

"""
Sandbox Manager HTTP service that abstracts Kubernetes API interactions
for SandboxClaim lifecycle management. Runs in-cluster and exposes a
REST API for creating, querying, and deleting sandboxes.
"""

import os
import logging

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, Field
from kubernetes import client, config, watch
from kubernetes.client import ApiException

# Constants for API Groups and Resources (matching sandbox_client.py)
CLAIM_API_GROUP = "extensions.agents.x-k8s.io"
CLAIM_API_VERSION = "v1alpha1"
CLAIM_PLURAL_NAME = "sandboxclaims"

SANDBOX_API_GROUP = "agents.x-k8s.io"
SANDBOX_API_VERSION = "v1alpha1"
SANDBOX_PLURAL_NAME = "sandboxes"

POD_NAME_ANNOTATION = "agents.x-k8s.io/pod-name"

SANDBOX_READY_TIMEOUT = int(os.environ.get("SANDBOX_READY_TIMEOUT", "180"))

logging.basicConfig(level=logging.INFO,
                    format='%(asctime)s - %(levelname)s - %(message)s')

app = FastAPI()

# Kubernetes client (initialized on startup)
custom_objects_api: client.CustomObjectsApi | None = None


# --- Pydantic Models ---

class CreateSandboxRequest(BaseModel):
    template_name: str
    namespace: str = "default"
    annotations: dict[str, str] = Field(default_factory=dict)


class SandboxResponse(BaseModel):
    claim_name: str
    sandbox_name: str
    pod_name: str
    namespace: str
    annotations: dict[str, str]
    status: str


class DeleteResponse(BaseModel):
    status: str
    claim_name: str


# --- Startup ---

@app.on_event("startup")
def startup():
    """Initialize the Kubernetes client using in-cluster config."""
    global custom_objects_api
    try:
        config.load_incluster_config()
    except config.ConfigException:
        config.load_kube_config()
    custom_objects_api = client.CustomObjectsApi()
    logging.info("Kubernetes client initialized.")


# --- Endpoints ---

@app.get("/healthz")
async def health_check():
    """A simple health check endpoint that always returns 200 OK."""
    return {"status": "ok"}


def _create_claim(claim_name: str, template_name: str, namespace: str,
                  annotations: dict[str, str]):
    """Creates a SandboxClaim custom resource."""
    manifest = {
        "apiVersion": f"{CLAIM_API_GROUP}/{CLAIM_API_VERSION}",
        "kind": "SandboxClaim",
        "metadata": {
            "name": claim_name,
            "annotations": annotations,
        },
        "spec": {"sandboxTemplateRef": {"name": template_name}},
    }
    custom_objects_api.create_namespaced_custom_object(
        group=CLAIM_API_GROUP,
        version=CLAIM_API_VERSION,
        namespace=namespace,
        plural=CLAIM_PLURAL_NAME,
        body=manifest,
    )


def _wait_for_sandbox_ready(claim_name: str, namespace: str, timeout: int):
    """Watches the Sandbox resource until it reaches Ready status."""
    w = watch.Watch()
    for event in w.stream(
        func=custom_objects_api.list_namespaced_custom_object,
        namespace=namespace,
        group=SANDBOX_API_GROUP,
        version=SANDBOX_API_VERSION,
        plural=SANDBOX_PLURAL_NAME,
        field_selector=f"metadata.name={claim_name}",
        timeout_seconds=timeout,
    ):
        if event["type"] in ["ADDED", "MODIFIED"]:
            sandbox_object = event["object"]
            status = sandbox_object.get("status", {})
            conditions = status.get("conditions", [])
            for cond in conditions:
                if cond.get("type") == "Ready" and cond.get("status") == "True":
                    metadata = sandbox_object.get("metadata", {})
                    sandbox_name = metadata.get("name")
                    if not sandbox_name:
                        raise RuntimeError(
                            "Could not determine sandbox name from sandbox object.")

                    annotations = metadata.get("annotations", {})
                    pod_name = annotations.get(POD_NAME_ANNOTATION, sandbox_name)

                    w.stop()
                    return sandbox_name, pod_name, annotations

    return None, None, None


@app.post("/sandbox/create", response_model=SandboxResponse, status_code=201)
def create_sandbox(req: CreateSandboxRequest):
    """
    Creates a SandboxClaim and waits for the Sandbox to become ready.
    This is a synchronous (blocking) endpoint — FastAPI runs it in a threadpool.
    """
    claim_name = f"sandbox-claim-{os.urandom(4).hex()}"

    logging.info(
        f"Creating SandboxClaim '{claim_name}' in namespace '{req.namespace}' "
        f"using template '{req.template_name}'..."
    )

    try:
        _create_claim(claim_name, req.template_name, req.namespace, req.annotations)
    except ApiException as e:
        logging.error(f"Failed to create SandboxClaim: {e}")
        raise HTTPException(status_code=500, detail=f"Failed to create SandboxClaim: {e.reason}")

    logging.info(f"Waiting for Sandbox '{claim_name}' to become ready...")

    try:
        sandbox_name, pod_name, annotations = _wait_for_sandbox_ready(
            claim_name, req.namespace, SANDBOX_READY_TIMEOUT
        )
    except Exception as e:
        # Clean up the claim if waiting fails
        logging.error(f"Error waiting for sandbox: {e}")
        try:
            custom_objects_api.delete_namespaced_custom_object(
                group=CLAIM_API_GROUP, version=CLAIM_API_VERSION,
                namespace=req.namespace, plural=CLAIM_PLURAL_NAME, name=claim_name,
            )
        except Exception:
            pass
        raise HTTPException(status_code=500, detail=f"Error waiting for sandbox: {e}")

    if sandbox_name is None:
        # Timed out — clean up the claim
        logging.warning(f"Sandbox '{claim_name}' did not become ready within {SANDBOX_READY_TIMEOUT}s")
        try:
            custom_objects_api.delete_namespaced_custom_object(
                group=CLAIM_API_GROUP, version=CLAIM_API_VERSION,
                namespace=req.namespace, plural=CLAIM_PLURAL_NAME, name=claim_name,
            )
        except Exception:
            pass
        raise HTTPException(
            status_code=408,
            detail=f"Sandbox did not become ready within {SANDBOX_READY_TIMEOUT} seconds.",
        )

    logging.info(f"Sandbox '{sandbox_name}' is ready (pod: {pod_name}).")

    return SandboxResponse(
        claim_name=claim_name,
        sandbox_name=sandbox_name,
        pod_name=pod_name,
        namespace=req.namespace,
        annotations=annotations,
        status="Ready",
    )


@app.get("/sandbox/{namespace}/{claim_name}", response_model=SandboxResponse)
def get_sandbox_status(namespace: str, claim_name: str):
    """Returns the current status of a Sandbox."""
    # Fetch the Sandbox object
    try:
        sandbox_obj = custom_objects_api.get_namespaced_custom_object(
            group=SANDBOX_API_GROUP,
            version=SANDBOX_API_VERSION,
            namespace=namespace,
            plural=SANDBOX_PLURAL_NAME,
            name=claim_name,
        )
    except ApiException as e:
        if e.status == 404:
            raise HTTPException(status_code=404, detail=f"Sandbox '{claim_name}' not found.")
        raise HTTPException(status_code=500, detail=f"Error fetching sandbox: {e.reason}")

    metadata = sandbox_obj.get("metadata", {})
    annotations = metadata.get("annotations", {})
    sandbox_name = metadata.get("name", claim_name)
    pod_name = annotations.get(POD_NAME_ANNOTATION, sandbox_name)

    # Determine status from conditions
    status_val = "Pending"
    conditions = sandbox_obj.get("status", {}).get("conditions", [])
    for cond in conditions:
        if cond.get("type") == "Ready" and cond.get("status") == "True":
            status_val = "Ready"
            break

    return SandboxResponse(
        claim_name=claim_name,
        sandbox_name=sandbox_name,
        pod_name=pod_name,
        namespace=namespace,
        annotations=annotations,
        status=status_val,
    )


@app.delete("/sandbox/{namespace}/{claim_name}", response_model=DeleteResponse)
def delete_sandbox(namespace: str, claim_name: str):
    """Deletes a SandboxClaim."""
    logging.info(f"Deleting SandboxClaim '{claim_name}' in namespace '{namespace}'...")
    try:
        custom_objects_api.delete_namespaced_custom_object(
            group=CLAIM_API_GROUP,
            version=CLAIM_API_VERSION,
            namespace=namespace,
            plural=CLAIM_PLURAL_NAME,
            name=claim_name,
        )
    except ApiException as e:
        if e.status == 404:
            raise HTTPException(status_code=404, detail=f"SandboxClaim '{claim_name}' not found.")
        raise HTTPException(status_code=500, detail=f"Error deleting sandbox claim: {e.reason}")

    logging.info(f"SandboxClaim '{claim_name}' deleted successfully.")
    return DeleteResponse(status="deleted", claim_name=claim_name)
