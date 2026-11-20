resource "google_compute_network" "this" {
  name                    = var.name
  auto_create_subnetworks = false
}

resource "google_compute_subnetwork" "this" {
  name                     = "${var.name}-${var.region}"
  region                   = var.region
  network                  = google_compute_network.this.id
  ip_cidr_range            = var.nodes_cidr
  private_ip_google_access = true

  secondary_ip_range {
    range_name    = "pods"
    ip_cidr_range = var.pods_cidr
  }

  secondary_ip_range {
    range_name    = "services"
    ip_cidr_range = var.services_cidr
  }
}

# Edge → hub traffic: the hub exposes NATS leaf-node port 7422 through a GKE *internal*
# load balancer restricted with loadBalancerSourceRanges (see gitops/platform/nats-hub).
# GKE manages the matching firewall rules itself; nothing else is opened here.

# SSH only through Identity-Aware Proxy (no public SSH).
resource "google_compute_firewall" "iap_ssh" {
  name      = "${var.name}-iap-ssh"
  network   = google_compute_network.this.id
  direction = "INGRESS"

  allow {
    protocol = "tcp"
    ports    = ["22"]
  }

  source_ranges = ["35.235.240.0/20"] # Google IAP range
  target_tags   = [var.edge_network_tag, var.hub_network_tag]
}

# Cloud NAT is only needed with private nodes. Off by default to save cost (ADR-0005).
resource "google_compute_router" "this" {
  count   = var.enable_nat ? 1 : 0
  name    = "${var.name}-router"
  region  = var.region
  network = google_compute_network.this.id
}

resource "google_compute_router_nat" "this" {
  count                              = var.enable_nat ? 1 : 0
  name                               = "${var.name}-nat"
  router                             = google_compute_router.this[0].name
  region                             = var.region
  nat_ip_allocate_option             = "AUTO_ONLY"
  source_subnetwork_ip_ranges_to_nat = "ALL_SUBNETWORKS_ALL_IP_RANGES"
}
