# Sandbox Manager

The Sandbox Manager is an HTTP service that abstracts Kubernetes API interactions for
SandboxClaim lifecycle management. It runs in-cluster and exposes a REST API for creating,
querying, and deleting sandboxes, removing the need for clients to have direct Kubernetes
API access.

## Architecture

The Sandbox Manager provides an additional layer of isolation between the Python SDK and the
Kubernetes control plane. Instead of the client directly creating and deleting `SandboxClaim`
custom resources via the Kubernetes API, it makes HTTP requests to the Sandbox Manager, which
handles all Kubernetes interactions with a dedicated ServiceAccount and minimal RBAC permissions.

The request flow is as follows:

```
Python Client  ──HTTP──▶  Sandbox Manager  ──K8s API──▶  SandboxClaim/Sandbox CRs
Python Client  ──HTTP──▶  Sandbox Router   ──K8s DNS──▶  Sandbox Pod (run/read/write)
```

1. The client sends a `POST /sandbox/create` request to the Sandbox Manager with the desired
   template name and namespace.
2. The manager creates a `SandboxClaim` custom resource and watches for the corresponding
   `Sandbox` to become ready.
3. Once ready, the manager returns the sandbox details (claim name, pod name, status) to the
   client.
4. The client uses the existing Sandbox Router for data-plane operations (execute, upload,
   download).
5. On cleanup, the client sends a `DELETE /sandbox/{namespace}/{claim_name}` request to the
   manager, which deletes the `SandboxClaim`.

## API Endpoints

| Method   | Path                                  | Description                                      |
|----------|---------------------------------------|--------------------------------------------------|
| `GET`    | `/healthz`                            | Health check, returns `{"status": "ok"}`          |
| `POST`   | `/sandbox/create`                     | Create a SandboxClaim and wait for readiness      |
| `GET`    | `/sandbox/{namespace}/{claim_name}`   | Get the current status of a sandbox               |
| `DELETE` | `/sandbox/{namespace}/{claim_name}`   | Delete a SandboxClaim                             |

### POST /sandbox/create

**Request body:**
```json
{
  "template_name": "python-sandbox-template",
  "namespace": "default",
  "annotations": {}
}
```

**Response (201 Created):**
```json
{
  "claim_name": "sandbox-claim-a1b2c3d4",
  "sandbox_name": "sandbox-claim-a1b2c3d4",
  "pod_name": "my-pod-xyz",
  "namespace": "default",
  "annotations": {},
  "status": "Ready"
}
```

## Building the Docker Image

### Prerequisites

- Python 3.13+
- Docker

### Build Steps

Use the provided `Dockerfile` to build and push the image to your container registry.

```bash
export SANDBOX_MANAGER_IMG=your_registry_path/sandbox-manager:latest
docker build -t $SANDBOX_MANAGER_IMG .
docker push $SANDBOX_MANAGER_IMG
```

## Deployment

### Deploy the Sandbox Manager

In `sandbox_manager.yaml` replace `IMAGE_PLACEHOLDER` with the `$SANDBOX_MANAGER_IMG` from the
previous step, and then apply the manifest. This will create the ServiceAccount, ClusterRole,
ClusterRoleBinding, Service, and Deployment.

```bash
sed -i "s|IMAGE_PLACEHOLDER|${SANDBOX_MANAGER_IMG}|g" sandbox_manager.yaml
kubectl apply -f sandbox_manager.yaml
```

### Configuration

The following environment variables can be set on the deployment:

| Variable                | Default | Description                                    |
|-------------------------|---------|------------------------------------------------|
| `SANDBOX_READY_TIMEOUT` | `180`   | Seconds to wait for a sandbox to become ready  |

## Using with the Python Client

Pass `manager_url` to `SandboxClient` to route lifecycle operations through the Sandbox Manager
instead of using the Kubernetes API directly:

```python
from agentic_sandbox import SandboxClient

with SandboxClient(
    template_name="python-sandbox-template",
    manager_url="http://sandbox-manager-svc.default.svc.cluster.local:8080",
    api_url="http://sandbox-router-svc.default.svc.cluster.local:8080",
    namespace="default",
) as sandbox:
    result = sandbox.run("echo 'Hello!'")
    print(result.stdout)
```

For local development with port-forwarding:

```bash
kubectl port-forward svc/sandbox-manager-svc 9090:8080 &
kubectl port-forward svc/sandbox-router-svc 9091:8080 &
```

```python
with SandboxClient(
    template_name="python-sandbox-template",
    manager_url="http://localhost:9090",
    api_url="http://localhost:9091",
    namespace="default",
) as sandbox:
    result = sandbox.run("echo 'Hello!'")
    print(result.stdout)
```
