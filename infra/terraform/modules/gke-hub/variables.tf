variable "project_id" {
  type = string
}

variable "name" {
  type    = string
  default = "orbit-hub"
}

variable "zone" {
  type        = string
  description = "Zone (not region): a zonal cluster is free-tier eligible."
}

variable "network_id" {
  type = string
}

variable "subnet_id" {
  type = string
}

variable "pods_range_name" {
  type = string
}

variable "services_range_name" {
  type = string
}

variable "network_tag" {
  type    = string
  default = "orbit-hub"
}

variable "machine_type" {
  type    = string
  default = "e2-standard-2"
}

variable "min_nodes" {
  type    = number
  default = 1
}

variable "max_nodes" {
  type    = number
  default = 2
}

variable "private_nodes" {
  type        = bool
  default     = false
  description = "Production setting is true (requires Cloud NAT). False keeps cost down (ADR-0005)."
}

variable "master_ipv4_cidr_block" {
  type    = string
  default = "172.16.0.0/28"
}

variable "master_authorized_cidrs" {
  type        = list(string)
  default     = []
  description = "CIDRs allowed to reach the Kubernetes API. Empty = no restriction."
}

variable "deletion_protection" {
  type    = bool
  default = false
}

variable "labels" {
  type    = map(string)
  default = {}
}
