terraform {
  required_providers {
    agentsandbox = {
      source = "glottman/agentsandbox"
    }
  }
}

provider "agentsandbox" {
  kubeconfig_path = "~/.kube/config"
  config_context  = "kind-agentsandbox-dev"
}
