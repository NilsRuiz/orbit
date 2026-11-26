terraform {
  required_version = ">= 1.6"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.12"
    }
  }

  # Bucket passed at init: terraform init -backend-config="bucket=<PROJECT_ID>-orbit-tfstate"
  backend "gcs" {
    prefix = "orbit/dev"
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
  zone    = var.zone

  default_labels = local.labels
}

locals {
  labels = {
    project     = "orbit"
    environment = "dev"
    managed-by  = "terraform"
  }
}

module "network" {
  source = "../../modules/network"

  name       = "orbit-dev"
  region     = var.region
  enable_nat = var.private_nodes
}

module "hub" {
  source = "../../modules/gke-hub"

  project_id              = var.project_id
  name                    = "orbit-hub-dev"
  zone                    = var.zone
  network_id              = module.network.network_id
  subnet_id               = module.network.subnet_id
  pods_range_name         = module.network.pods_range_name
  services_range_name     = module.network.services_range_name
  network_tag             = module.network.hub_network_tag
  private_nodes           = var.private_nodes
  master_authorized_cidrs = var.master_authorized_cidrs
  labels                  = local.labels
}

module "edge" {
  source = "../../modules/edge-node"

  name_prefix = "orbit-dev"
  nodes       = var.edge_nodes
  zone        = var.zone
  subnet_id   = module.network.subnet_id
  network_tag = module.network.edge_network_tag
  k3s_version = var.k3s_version
  labels      = local.labels
}

module "github_oidc" {
  source = "../../modules/github-oidc"

  project_id        = var.project_id
  github_repository = var.github_repository
  state_bucket      = var.state_bucket
}
