variable "name" {
  type        = string
  description = "Network name prefix."
}

variable "region" {
  type = string
}

variable "nodes_cidr" {
  type    = string
  default = "10.10.0.0/20"
}

variable "pods_cidr" {
  type    = string
  default = "10.20.0.0/16"
}

variable "services_cidr" {
  type    = string
  default = "10.30.0.0/20"
}

variable "hub_network_tag" {
  type    = string
  default = "orbit-hub"
}

variable "edge_network_tag" {
  type    = string
  default = "orbit-edge"
}

variable "enable_nat" {
  type        = bool
  default     = false
  description = "Create Cloud Router + NAT. Required when GKE nodes are private."
}
