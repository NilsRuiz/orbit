variable "name_prefix" {
  description = "Prefix of the VM and service account names, e.g. orbit-dev."
  type        = string
  default     = "orbit"
}

variable "nodes" {
  description = "Edge nodes to create, keyed by node ID (e.g. sat-1)."
  type = map(object({
    machine_type = optional(string)
  }))
}

variable "zone" {
  description = "Zone of the edge VMs."
  type        = string
}

variable "subnet_id" {
  description = "Subnetwork the VMs attach to (network module)."
  type        = string
}

variable "network_tag" {
  description = "Network tag of the VMs, used by firewall rules."
  type        = string
  default     = "orbit-edge"
}

variable "machine_type" {
  type        = string
  default     = "e2-small"
  description = "Default machine type. Try t2a-standard-1 (ARM) in regions that offer it."
}

variable "image" {
  description = "Boot image. Debian 12: small, and k3s supports it."
  type        = string
  default     = "debian-cloud/debian-12"
}

variable "k3s_version" {
  type        = string
  description = "Pinned k3s version, e.g. v1.37.2+k3s1. Never use 'latest' on a fleet."
}

variable "public_ip" {
  description = "Ephemeral public IP for egress (images, GitHub). Needed while Cloud NAT is off (ADR-0005)."
  type        = bool
  default     = true
}

variable "labels" {
  description = "Labels on every VM, on top of orbit-node-id and orbit-role."
  type        = map(string)
  default     = {}
}
