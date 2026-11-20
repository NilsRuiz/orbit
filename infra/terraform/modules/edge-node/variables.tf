variable "name_prefix" {
  type    = string
  default = "orbit"
}

variable "nodes" {
  description = "Edge nodes to create, keyed by node ID (e.g. sat-1)."
  type = map(object({
    machine_type = optional(string)
  }))
}

variable "zone" {
  type = string
}

variable "subnet_id" {
  type = string
}

variable "network_tag" {
  type    = string
  default = "orbit-edge"
}

variable "machine_type" {
  type        = string
  default     = "e2-small"
  description = "Default machine type. Try t2a-standard-1 (ARM) in regions that offer it."
}

variable "image" {
  type    = string
  default = "debian-cloud/debian-12"
}

variable "k3s_version" {
  type        = string
  description = "Pinned k3s version, e.g. v1.33.5+k3s1. Never use 'latest' on a fleet."
}

variable "public_ip" {
  type    = bool
  default = true
}

variable "labels" {
  type    = map(string)
  default = {}
}
