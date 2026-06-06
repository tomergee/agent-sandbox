# JavaScript/TypeScript SDK for Agent Sandbox

To enable the TypeScript-based **OpenClaw** gateway to natively provision, manage, and interact with Agent Sandboxes, we will implement a lightweight, high-performance JS/TS SDK: `k8s-agent-sandbox-js`.

This SDK will match the design patterns and features of the official Go and Python SDKs, ensuring a consistent integration experience.

---

## 1. Core Client & Handles

The SDK divides operations between `SandboxClient` (management) and `SandboxInstance` (operations), with delegated sub-engines `SandboxCommands` and `SandboxFiles`.

### A. `SandboxClient`
Manages the lifecycle of Sandbox and SandboxClaim CRDs, including connection registries.

```typescript
import { KubeConfig } from '@kubernetes/client-node';

export interface SandboxClientOptions {
  kubeconfig?: KubeConfig;
  namespace?: string;
  connectionStrategy?: ConnectionStrategy;
}

export class SandboxClient {
  constructor(options?: SandboxClientOptions);

  /**
   * Lease or claim a sandbox from a warm pool using a SandboxClaim
   */
  async claimSandbox(options: {
    name: string;
    poolName: string;
    ttlSeconds?: number;
    env?: Record<string, string>;
  }): Promise<SandboxInstance>;

  /**
   * Retrieve an existing sandbox handle by name
   */
  async getSandbox(name: string): Promise<SandboxInstance>;

  /**
   * Delete a sandbox (and its associated claim)
   */
  async deleteSandbox(name: string): Promise<void>;
}
```

### B. `SandboxInstance`
Exposes high-level execution, file operations, and lifecycle hooks.

```typescript
export class SandboxInstance {
  readonly name: string;
  readonly namespace: string;
  readonly commands: SandboxCommands;
  readonly files: SandboxFiles;

  /**
   * Direct shortcut delegators for ease-of-use
   */
  async run(command: string, options?: ExecOptions): Promise<ExecResult>;
  async read(path: string): Promise<Buffer>;
  async write(path: string, content: string | Buffer): Promise<void>;

  /**
   * Core lifecycle hooks
   */
  async getStatus(): Promise<{
    state: 'Pending' | 'Running' | 'Suspended';
    ready: boolean;
    ipAddress?: string;
  }>;

  /**
   * Administrative suspension (forces pod termination while keeping metadata)
   */
  async suspend(): Promise<void>;
  async resume(): Promise<void>;
}
```

---

## 2. Transport & Connection Discovery

Following the established SDK guidelines, the JS/TS SDK supports three connection discovery strategies to resolve the base HTTP/WS URL of the `sandbox-router` daemon:

1. **`DirectConnectionStrategy`**: Uses a pre-configured static router endpoint (useful for development).
2. **`GatewayConnectionStrategy`**: Dynamically watches the Kubernetes `Gateway` resource inside the namespace to resolve its external IP/hostname.
3. **`PortForwardConnectionStrategy`**:
   - Resolves the `sandbox-router-svc` pod via standard EndpointSlices.
   - Establishes a secure, native Kubernetes SPDY port-forwarding tunnel to port `8080` on the pod using `@kubernetes/client-node`'s `PortForward` API.
   - Automatically manages websocket connections and recovers from channel disconnects.

---

## 3. Router Daemon API Protocol Integration

All executions and file transfers are transmitted via HTTP to the `sandbox-router` base URL using the following HTTP headers:
- `X-Sandbox-ID`: Name of the target Sandbox.
- `X-Sandbox-Namespace`: Kubernetes namespace.
- `X-Request-ID`: Correlation ID for log tracing.

### A. Command Execution (`POST /execute`)
- **Payload**: `{"command": "<command_string>"}`
- **Response**: `{"stdout": "...", "stderr": "...", "exit_code": number}`
- **Limit**: Response combined output is capped at 16 MB.

### B. File Upload (`POST /upload`)
- **Payload**: Multipart form payload.
- **Validation**: The filename must be a plain basename (no path directory prefixes allowed).

### C. File Download (`GET /download/<encoded-path>`)
- **Payload**: Returns raw file bytes.
- **Requirement**: Paths must be percent-encoded (excluding `A-Za-z0-9 -_.~` safe set).

### D. Directory Listing (`GET /list/<encoded-path>`)
- **Response**: `Array<{name: string, size: number, type: "file" | "directory", mod_time: string}>`.

---

## 4. Resilient HTTP Client with Jittered Backoff

The JS/TS SDK will ship with a built-in resilient HTTP client to guarantee communication robustness:
- **Transient Retries**: Automatically retries failures indicating transient issues (status codes `500`, `502`, `503`, `504`, and TCP connection resets).
- **Retry Limit**: Up to **6 attempts**.
- **Exponential Backoff & Jitter**: Starts with an initial delay of `500ms`, scaling exponentially up to `8000ms` max delay, combined with randomized jitter to avoid server thundering herds.
- **Execution Safety**: By default, execution calls (`run()`) will use **0 retries** to ensure non-idempotent scripts are not executed multiple times, unless explicitly configured via `ExecOptions` (e.g., `{ maxAttempts: 6 }`).
