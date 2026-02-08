# Agentic Sandbox Client Python

This Python client provides a simple, high-level interface for creating and interacting with
sandboxes managed by the Agent Sandbox controller. It's designed to be used as a context manager,
ensuring that sandbox resources are properly created and cleaned up.

It supports a **scalable, cloud-native architecture** using Kubernetes Gateways and a specialized
Router, while maintaining a convenient **Developer Mode** for local testing.

## Architecture

The client supports two planes of communication:

- **Control Plane (Sandbox Lifecycle):** Creating and deleting sandboxes, either via direct
  Kubernetes API access or through the [Sandbox Manager](sandbox-manager/README.md) HTTP service.
- **Data Plane (Sandbox Interaction):** Executing commands, reading/writing files, routed through
  the [Sandbox Router](sandbox-router/README.md).

### Connection Modes

1.  **Production (Gateway Mode):** Traffic flows from the Client -> Cloud Load Balancer (Gateway)
    -> Router Service -> Sandbox Pod. This supports high-scale deployments.
2.  **Development (Tunnel Mode):** Traffic flows from Localhost -> `kubectl port-forward` -> Router
    Service -> Sandbox Pod. This requires no public IP and works on Kind/Minikube.
3.  **Advanced / Internal Mode**: The client connects directly to a provided `api_url`, bypassing
    discovery. This is useful for in-cluster communication or when connecting through a custom domain.
4.  **Sandbox Manager Mode**: The client delegates sandbox lifecycle management to the
    [Sandbox Manager](sandbox-manager/README.md) HTTP service via `manager_url`, removing the need
    for direct Kubernetes API credentials. Data-plane traffic still flows through the Router.

## Prerequisites

- A running Kubernetes cluster.
- The **Agent Sandbox Controller** installed.
- `kubectl` installed and configured locally.

## Setup: Deploying the Router and Manager

Before using the client, you must deploy the `sandbox-router`. Optionally, deploy the
`sandbox-manager` to avoid requiring direct Kubernetes API access from clients.

1.  **Build and Push the Router Image:**

    For both Gateway Mode and Tunnel mode, follow the instructions in [sandbox-router](sandbox-router/README.md)
    to build, push, and apply the router image and resources.

2.  **(Optional) Deploy the Sandbox Manager:**

    If you want clients to create sandboxes via HTTP instead of the Kubernetes API directly,
    deploy the [sandbox-manager](sandbox-manager/README.md). This provides an additional layer of
    isolation and removes the need for clients to have Kubernetes credentials.

    ```bash
    # Follow the instructions in sandbox-manager/README.md to build, push, and deploy.
    kubectl apply -f sandbox-manager/sandbox_manager.yaml
    ```

3.  **Create a Sandbox Template:**

    Ensure a `SandboxTemplate` exists in your target namespace. The [test_client.py](test_client.py)
    uses the [python-runtime-sandbox](../../../examples/python-runtime-sandbox/) image.

    ```bash
    kubectl apply -f python-sandbox-template.yaml
    ```

## Installation

1.  **Create a virtual environment:**

    ```bash
    python3 -m venv .venv
    source .venv/bin/activate
    ```

2.  **Option 1: Install from source via git:**

    ```bash
    # Replace "main" with a specific version tag (e.g., "v0.1.0") from
    # https://github.com/kubernetes-sigs/agent-sandbox/releases to pin a version tag.
    export VERSION="main"

    pip install "git+https://github.com/kubernetes-sigs/agent-sandbox.git@${VERSION}#subdirectory=clients/python/agentic-sandbox-client"
    ```

3.  **Option 2: Install from source in editable mode:**

    If you have not already done so, first clone this repository:

    ```bash
    cd ~
    git clone https://github.com/kubernetes-sigs/agent-sandbox.git
    cd agent-sandbox/clients/python/agentic-sandbox-client
    ```

    And then install the agentic-sandbox-client into your activated .venv:

    ```bash
    pip install -e .
    ```

    If you are using [tracing with GCP](GCP.md#tracing-with-open-telemetry-and-google-cloud-trace),
    install with the optional tracing dependencies:

    ```
    pip install -e ".[tracing]"
    ```

## Usage Examples

### 1. Production Mode (GKE Gateway)

Use this when running against a real cluster with a public Gateway IP. The client automatically
discovers the Gateway.

```python
from agentic_sandbox import SandboxClient

# Connect via the GKE Gateway
with SandboxClient(
    template_name="python-sandbox-template",
    gateway_name="external-http-gateway",  # Name of the Gateway resource
    namespace="default"
) as sandbox:
    print(sandbox.run("echo 'Hello from Cloud!'").stdout)
```

### 2. Developer Mode (Local Tunnel)

Use this for local development or CI. If you omit `gateway_name`, the client automatically opens a
secure tunnel to the Router Service using `kubectl`.

```python
from agentic_sandbox import SandboxClient

# Automatically tunnels to svc/sandbox-router-svc
with SandboxClient(
    template_name="python-sandbox-template",
    namespace="default"
) as sandbox:
    print(sandbox.run("echo 'Hello from Local!'").stdout)
```

### 3. Advanced / Internal Mode

Use `api_url` to bypass discovery entirely. Useful for:

- **Internal Agents:** Running inside the cluster (connect via K8s DNS).
- **Custom Domains:** Connecting via HTTPS (e.g., `https://sandbox.example.com`).

```python
with SandboxClient(
    template_name="python-sandbox-template",
    # Connect directly to a URL
    api_url="http://sandbox-router-svc.default.svc.cluster.local:8080",
    namespace="default"
) as sandbox:
    sandbox.run("ls -la")
```

### 4. Sandbox Manager Mode

Use `manager_url` to delegate sandbox lifecycle to the Sandbox Manager service. The client no
longer needs Kubernetes API credentials — only HTTP access to the manager and router.

```python
from agentic_sandbox import SandboxClient

with SandboxClient(
    template_name="python-sandbox-template",
    manager_url="http://sandbox-manager-svc.default.svc.cluster.local:8080",
    api_url="http://sandbox-router-svc.default.svc.cluster.local:8080",
    namespace="default",
) as sandbox:
    print(sandbox.run("echo 'Hello via Manager!'").stdout)
```

For local development with port-forwarding:

```python
with SandboxClient(
    template_name="python-sandbox-template",
    manager_url="http://localhost:9090",  # port-forward to sandbox-manager-svc
    api_url="http://localhost:9091",      # port-forward to sandbox-router-svc
    namespace="default",
) as sandbox:
    print(sandbox.run("echo 'Hello via Manager!'").stdout)
```

### 5. Custom Ports

If your sandbox runtime listens on a port other than 8888 (e.g., a Node.js app on 3000), specify `server_port`.

```python
with SandboxClient(
    template_name="node-sandbox-template",
    server_port=3000
) as sandbox:
    # ...
```

## Testing

A test script is included to verify the full lifecycle (Creation -> Execution -> File I/O -> Cleanup).

### Run in Dev Mode:

```
python test_client.py --namespace default
```

### Run in Production Mode:

```
python test_client.py --gateway-name external-http-gateway
```

### Run with Sandbox Manager:

```bash
kubectl port-forward svc/sandbox-manager-svc 9090:8080 &
kubectl port-forward svc/sandbox-router-svc 9091:8080 &
python test_client.py --manager-url http://localhost:9090 --api-url http://localhost:9091
```
