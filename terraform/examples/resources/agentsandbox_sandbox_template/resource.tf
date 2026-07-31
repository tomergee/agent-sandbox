resource "agentsandbox_sandbox_template" "python" {
  name      = "python-runtime"
  namespace = "default"

  pod_template = {
    containers = [{
      name    = "runtime"
      image   = "python:3.12-slim"
      command = ["sleep", "infinity"]
    }]
  }

  service = true

  # Allow claims to inject per-session env vars.
  env_vars_injection_policy = "Allowed"

  network_policy = {
    egress = yamlencode([
      {
        to = [{ podSelector = {} }]
      }
    ])
  }

  depends_on = [agentsandbox_install.this]
}
