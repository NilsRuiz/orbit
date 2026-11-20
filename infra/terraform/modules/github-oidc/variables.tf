variable "project_id" {
  type = string
}

variable "github_repository" {
  type        = string
  description = "owner/repo allowed to authenticate, e.g. jdoe/orbit."
}

variable "state_bucket" {
  type = string
}

variable "pool_id" {
  type    = string
  default = "github-actions"
}

variable "ci_service_account_id" {
  type    = string
  default = "orbit-ci"
}

variable "ci_project_roles" {
  type        = list(string)
  default     = ["roles/viewer", "roles/iam.securityReviewer"]
  description = "Roles for terraform plan. Apply from CI would need broader roles."
}
