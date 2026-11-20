variable "project_id" {
  type = string
}

variable "region" {
  type    = string
  default = "europe-west1"
}

variable "zone" {
  type    = string
  default = "europe-west1-b"
}

variable "state_bucket" {
  type        = string
  description = "Output of infra/terraform/bootstrap."
}

variable "github_repository" {
  type        = string
  description = "owner/repo, e.g. jdoe/orbit."
}

variable "private_nodes" {
  type    = bool
  default = false
}

variable "master_authorized_cidrs" {
  type    = list(string)
  default = []
}

variable "k3s_version" {
  type    = string
  default = "v1.33.5+k3s1" # pin and bump deliberately (Dependabot cannot do it for you)
}

variable "edge_nodes" {
  type = map(object({
    machine_type = optional(string)
  }))
  default = {
    sat-1 = {}
    sat-2 = {}
    sat-3 = {}
  }
}
