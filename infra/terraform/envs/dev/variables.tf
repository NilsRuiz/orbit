variable "project_id" {
  description = "GCP project ID."
  type        = string
}

variable "region" {
  description = "Region of the network and the state."
  type        = string
  default     = "europe-west1"
}

variable "zone" {
  description = "Zone of the hub cluster and the edge VMs (zonal: GKE free tier)."
  type        = string
  default     = "europe-west1-b"
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
  description = "Private GKE nodes behind Cloud NAT: the production setting, off for cost (ADR-0005)."
  type        = bool
  default     = false
}

variable "master_authorized_cidrs" {
  description = "CIDRs allowed to reach the Kubernetes API, e.g. your IP/32. Empty = unrestricted."
  type        = list(string)
  default     = []
}

variable "k3s_version" {
  description = "k3s on the edge VMs, pinned: bump deliberately, never latest on a fleet."
  type        = string
  default     = "v1.37.2+k3s1" # the Kubernetes minor of the hub (k3d) and envtest
}

variable "edge_nodes" {
  description = "Edge nodes to create, keyed by node ID (a DNS-1123 label, e.g. sat-1)."
  type = map(object({
    machine_type = optional(string)
  }))
  default = {
    sat-1 = {}
    sat-2 = {}
    sat-3 = {}
  }
}
