resource "agentsandbox_sandbox" "dev" {
  name      = "dev-1"
  namespace = "default"

  pod_template = {
    containers = [{
      name    = "runtime"
      image   = "python:3.12-slim"
      command = ["sleep", "infinity"]
      env = [{
        name  = "SESSION"
        value = "dev"
      }]
      resources = {
        requests = { cpu = "250m", memory = "256Mi" }
        limits   = { memory = "512Mi" }
      }
    }]

    # Anything the typed schema doesn't cover goes through spec_overrides
    # (strategic-merge-patched over the typed fields):
    spec_overrides = yamlencode({
      runtimeClassName = "gvisor"
    })
  }

  service = true

  volume_claim_templates = [{
    name         = "work"
    access_modes = ["ReadWriteOnce"]
    storage      = "1Gi"
  }]

  depends_on = [agentsandbox_install.this]
}

output "sandbox_fqdn" {
  value = agentsandbox_sandbox.dev.service_fqdn
}
