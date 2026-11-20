output "hub_get_credentials" {
  value = module.hub.get_credentials_command
}

output "edge_nodes" {
  value = module.edge.nodes
}

output "github_wif_provider" {
  value = module.github_oidc.workload_identity_provider
}

output "github_ci_service_account" {
  value = module.github_oidc.service_account_email
}
