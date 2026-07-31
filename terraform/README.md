# terraform-provider-agentsandbox

A Terraform provider for the OSS [kubernetes-sigs/agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox)
controller: install the controller declaratively and manage `Sandbox`,
`SandboxTemplate`, `SandboxWarmPool`, and `SandboxClaim` objects as typed
Terraform resources with readiness waiting and status outputs.

## Resources

| Resource | Manages |
|---|---|
| `agentsandbox_install` | Controller installation from the upstream release manifest (SSA, upgrade with prune, destroy) |
| `agentsandbox_sandbox` | `Sandbox` (agents.x-k8s.io/v1beta1) |
| `agentsandbox_sandbox_template` | `SandboxTemplate` (extensions.agents.x-k8s.io/v1beta1) |
| `agentsandbox_sandbox_warmpool` | `SandboxWarmPool` |
| `agentsandbox_sandbox_claim` | `SandboxClaim` |
| `data.agentsandbox_sandbox` | Reads a live Sandbox (serviceFQDN, podIPs, conditions) |

## Quick start

```hcl
provider "agentsandbox" {
  kubeconfig_path = "~/.kube/config"
}

resource "agentsandbox_install" "this" {
  version = "v0.5.4"
}

resource "agentsandbox_sandbox" "dev" {
  name = "dev-1"

  pod_template = {
    containers = [{
      name    = "runtime"
      image   = "python:3.12-slim"
      command = ["sleep", "infinity"]
    }]
    # escape hatch for fields the typed schema doesn't cover:
    spec_overrides = yamlencode({ runtimeClassName = "gvisor" })
  }

  depends_on = [agentsandbox_install.this]
}

output "fqdn" {
  value = agentsandbox_sandbox.dev.service_fqdn
}
```

See `examples/` for warm pools, claims, and templates.

## Design notes

- **Server-side apply** with field manager `terraform-provider-agentsandbox`; create and
  update share one code path, and `force_conflicts` on the installer takes ownership from
  prior `kubectl apply`s.
- **`pod_template.spec_overrides`** is a YAML/JSON PodSpec fragment merged over the typed
  attributes with Kubernetes strategic-merge-patch semantics: overrides win; `containers`
  and `env` merge by `name`, `ports` by `containerPort`; unkeyed lists (`command`, `args`,
  `tolerations`) are replaced wholesale; explicit `null` deletes a field. The provider
  records the merged result in private state so refresh doesn't report override-induced
  differences as drift.
- **Waits**: sandboxes and claims wait for the `Ready` condition (`wait_for_ready`,
  default `true`), warm pools wait for `readyReplicas == replicas`, the installer waits for
  CRDs `Established` and controller deployments `Available`. All bounded by `timeouts`.
- **Namespace caveat**: the upstream conversion webhook hardcodes
  `agent-sandbox-system`; the installer cannot target another namespace.
- **Ordering caveat**: CR resources in the same config as `agentsandbox_install` should
  declare `depends_on` on it. CR creates additionally retry CRD-not-found errors for ~30s
  to absorb races.

## Development

```sh
make build     # compile
make test      # unit tests (no cluster needed)
make kind-up   # local kind cluster
make testacc   # acceptance tests (TF_ACC=1, needs kind + terraform)
```

Local iteration without `terraform init`, via `~/.terraformrc`:

```hcl
provider_installation {
  dev_overrides {
    "glottman/agentsandbox" = "/Users/you/go/bin"
  }
  direct {}
}
```

Then `go install .` and run `terraform plan` directly.

## License

Apache-2.0
