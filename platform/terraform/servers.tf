# Servers A+ (decided 28 Sep 2026): two CX43s side by side, plus a CX23
# standby capture box in a second eu-central datacenter.

locals {
  boxes = {
    worker = {
      type      = "cx43"
      location  = var.location_main
      role      = "worker"
      private   = "10.20.1.10"
      capture   = true # its IPv4 is on the proxy allowlist
      placement = true
    }
    data = {
      type      = "cx43"
      location  = var.location_main
      role      = "data"
      private   = "10.20.1.20"
      capture   = false
      placement = true
    }
    standby = {
      type      = "cx23"
      location  = var.location_standby
      role      = "standby"
      private   = "10.20.1.30"
      capture   = true
      placement = false # different datacenter from the others
    }
  }
  capture_boxes = { for k, b in local.boxes : k => b if b.capture }
}

# Capture IPs are Primary IPs, so a dead or resized box's replacement keeps the
# address the proxy provider allowlisted. Neither can be deleted by accident.
resource "hcloud_primary_ip" "capture" {
  for_each = local.capture_boxes

  name              = "adhunters-${each.key}-v4"
  location          = each.value.location
  type              = "ipv4"
  auto_delete       = false
  delete_protection = true
  labels            = { platform = "adhunters", role = each.value.role, allowlisted = "proxies" }
}

resource "hcloud_server" "box" {
  for_each = local.boxes

  name               = "adhunters-${each.key}"
  server_type        = each.value.type
  image              = var.image
  location           = each.value.location
  firewall_ids       = [hcloud_firewall.closed.id]
  placement_group_id = each.value.placement ? hcloud_placement_group.main.id : null
  labels             = { platform = "adhunters", role = each.value.role }

  user_data = templatefile("${path.module}/../servers/cloud-init.yaml.tftpl", {
    hostname           = "adhunters-${each.key}"
    role               = each.value.role
    admin_ssh_keys     = var.admin_ssh_keys
    tailscale_auth_key = var.tailscale_auth_key
  })

  public_net {
    ipv4_enabled = true
    ipv4         = each.value.capture ? hcloud_primary_ip.capture[each.key].id : null
    ipv6_enabled = true
  }

  network {
    network_id = hcloud_network.main.id
    ip         = each.value.private
  }

  # The first boot's cloud-init is all user_data does; later changes to the
  # setup happen through the setup script, not by replacing the box.
  lifecycle {
    ignore_changes = [user_data, image]
  }

  depends_on = [hcloud_network_subnet.eu_central]
}
