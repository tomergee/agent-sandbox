resource "agentsandbox_sandbox_claim" "session" {
  name           = "user-42"
  namespace      = "default"
  warm_pool_name = agentsandbox_sandbox_warmpool.pool.name

  env = [{
    name  = "SESSION_ID"
    value = "42"
  }]

  additional_pod_metadata = {
    labels = { "session" = "user-42" }
  }
}

output "session_fqdn" {
  value = agentsandbox_sandbox_claim.session.service_fqdn
}
