variable "name" {
  type        = string
  description = "Network name prefix."
}

variable "region" {
  description = "Region of the subnetwork."
  type        = string
}

variable "nodes_cidr" {
  description = "Primary range: GKE nodes and edge VMs."
  type        = string
  default     = "10.10.0.0/20"
}

variable "pods_cidr" {
  description = "Secondary range for GKE pods."
  type        = string
  default     = "10.20.0.0/16"
}

variable "services_cidr" {
  description = "Secondary range for GKE services."
  type        = string
  default     = "10.30.0.0/20"
}

variable "hub_network_tag" {
  description = "Network tag of the hub nodes."
  type        = string
  default     = "orbit-hub"
}

variable "edge_network_tag" {
  description = "Network tag of the edge VMs."
  type        = string
  default     = "orbit-edge"
}

variable "enable_nat" {
  type        = bool
  default     = false
  description = "Create Cloud Router + NAT. Required when GKE nodes are private."
}
