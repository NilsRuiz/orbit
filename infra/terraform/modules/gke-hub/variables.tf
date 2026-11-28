variable "project_id" {
  description = "GCP project ID (Workload Identity pool name)."
  type        = string
}

variable "name" {
  description = "Cluster name; also the node service account prefix."
  type        = string
  default     = "orbit-hub"
}

variable "zone" {
  type        = string
  description = "Zone (not region): a zonal cluster is free-tier eligible."
}

variable "network_id" {
  description = "VPC network (network module)."
  type        = string
}

variable "subnet_id" {
  description = "Subnetwork (network module)."
  type        = string
}

variable "pods_range_name" {
  description = "Secondary range name for pods."
  type        = string
}

variable "services_range_name" {
  description = "Secondary range name for services."
  type        = string
}

variable "network_tag" {
  description = "Network tag of the hub nodes."
  type        = string
  default     = "orbit-hub"
}

variable "machine_type" {
  description = "Node machine type. e2-standard-2 fits NATS, the operator and Flux."
  type        = string
  default     = "e2-standard-2"
}

variable "min_nodes" {
  description = "Autoscaling minimum of the spot pool."
  type        = number
  default     = 1
}

variable "max_nodes" {
  description = "Autoscaling maximum of the spot pool."
  type        = number
  default     = 2
}

variable "private_nodes" {
  type        = bool
  default     = false
  description = "Production setting is true (requires Cloud NAT). False keeps cost down (ADR-0005)."
}

variable "master_ipv4_cidr_block" {
  description = "Control plane range, with private nodes only."
  type        = string
  default     = "172.16.0.0/28"
}

variable "master_authorized_cidrs" {
  type        = list(string)
  default     = []
  description = "CIDRs allowed to reach the Kubernetes API. Empty = no restriction."
}

variable "deletion_protection" {
  description = "Protect the cluster from deletion. Off for on-demand dev sessions (ADR-0005)."
  type        = bool
  default     = false
}

variable "labels" {
  description = "Labels on the cluster and its nodes."
  type        = map(string)
  default     = {}
}
