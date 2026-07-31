# Install the agent-sandbox controller (core + extensions) into the cluster.
# The controller always lands in the agent-sandbox-system namespace — the
# upstream conversion webhook hardcodes it.
resource "agentsandbox_install" "this" {
  version  = "v0.5.4"
  manifest = "sandbox-with-extensions"

  # Keep CRDs (and therefore all sandbox CRs) on terraform destroy:
  # crds_on_destroy = "retain"

  timeouts {
    create = "15m"
  }
}
