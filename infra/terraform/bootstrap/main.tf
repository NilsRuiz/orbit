# One-time bootstrap: remote state bucket + APIs. Uses local state on purpose
# (chicken-and-egg). Keep the generated terraform.tfstate out of Git.

terraform {
  required_version = ">= 1.6"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.12"
    }
  }
}

variable "project_id" {
  type        = string
  description = "GCP project ID."
}

variable "region" {
  description = "Region of the state bucket."
  type        = string
  default     = "europe-west1"
}

provider "google" {
  project = var.project_id
  region  = var.region
}

locals {
  apis = [
    "compute.googleapis.com",
    "container.googleapis.com",
    "iam.googleapis.com",
    "iamcredentials.googleapis.com",
    "sts.googleapis.com",
    "cloudresourcemanager.googleapis.com",
    "logging.googleapis.com",
    "monitoring.googleapis.com",
  ]
}

resource "google_project_service" "apis" {
  for_each           = toset(local.apis)
  service            = each.value
  disable_on_destroy = false
}

resource "google_storage_bucket" "tfstate" {
  name                        = "${var.project_id}-orbit-tfstate"
  location                    = var.region
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"
  force_destroy               = false

  versioning {
    enabled = true
  }

  lifecycle_rule {
    condition {
      num_newer_versions = 10
    }
    action {
      type = "Delete"
    }
  }
}

output "state_bucket" {
  value = google_storage_bucket.tfstate.name
}
