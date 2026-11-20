# Simulated edge nodes ("satellites"): one spot VM per node, running k3s.
#
# Provider-neutral interface (ADR-0001): inputs `nodes`, `k3s_version`, `machine_type`;
# outputs `nodes` (id => { name, internal_ip }). A future `edge-node-scaleway` module must
# expose the same interface so envs can swap providers without other changes.

resource "google_service_account" "edge" {
  account_id   = "${var.name_prefix}-edge"
  display_name = "Orbit edge nodes (no permissions on purpose)"
}

resource "google_compute_instance" "node" {
  for_each = var.nodes

  name         = "${var.name_prefix}-${each.key}"
  zone         = var.zone
  machine_type = coalesce(each.value.machine_type, var.machine_type)
  tags         = [var.network_tag]

  labels = merge(var.labels, {
    orbit-node-id = each.key
    orbit-role    = "edge"
  })

  scheduling {
    provisioning_model          = "SPOT"
    preemptible                 = true
    automatic_restart           = false
    instance_termination_action = "STOP"
  }

  boot_disk {
    initialize_params {
      image = var.image
      size  = 20
      type  = "pd-standard"
    }
  }

  network_interface {
    subnetwork = var.subnet_id
    # Ephemeral public IP for egress (image pulls) since Cloud NAT is off by default.
    dynamic "access_config" {
      for_each = var.public_ip ? [1] : []
      content {}
    }
  }

  service_account {
    email  = google_service_account.edge.email
    scopes = ["https://www.googleapis.com/auth/logging.write"]
  }

  shielded_instance_config {
    enable_secure_boot = true
  }

  metadata = {
    enable-oslogin = "TRUE"
  }

  metadata_startup_script = templatefile("${path.module}/startup.sh.tftpl", {
    node_id     = each.key
    k3s_version = var.k3s_version
  })
}
