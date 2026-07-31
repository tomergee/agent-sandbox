data "agentsandbox_sandbox" "dev" {
  name      = "dev-1"
  namespace = "default"
}

output "pod_ips" {
  value = data.agentsandbox_sandbox.dev.pod_ips
}
