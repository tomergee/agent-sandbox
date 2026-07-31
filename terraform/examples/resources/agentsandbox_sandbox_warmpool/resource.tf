resource "agentsandbox_sandbox_warmpool" "pool" {
  name                  = "python-pool"
  namespace             = "default"
  replicas              = 3
  sandbox_template_name = agentsandbox_sandbox_template.python.name
  update_strategy       = "OnReplenish"
}
