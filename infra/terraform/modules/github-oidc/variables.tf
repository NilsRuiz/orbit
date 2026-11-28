variable "project_id" {
  description = "GCP project ID."
  type        = string
}

variable "github_repository" {
  type        = string
  description = "owner/repo allowed to authenticate, e.g. jdoe/orbit."
}

variable "state_bucket" {
  description = "Terraform state bucket the CI service account reads and locks."
  type        = string
}

variable "pool_id" {
  description = "Workload Identity pool ID."
  type        = string
  default     = "github-actions"
}

variable "ci_service_account_id" {
  description = "Account ID of the CI service account."
  type        = string
  default     = "orbit-ci"
}

variable "ci_project_roles" {
  type        = list(string)
  default     = ["roles/viewer", "roles/iam.securityReviewer"]
  description = "Roles for terraform plan. Apply from CI would need broader roles."
}
