output "nodes" {
  description = "node_id => { name, internal_ip }"
  value = {
    for id, vm in google_compute_instance.node : id => {
      name        = vm.name
      internal_ip = vm.network_interface[0].network_ip
    }
  }
}
