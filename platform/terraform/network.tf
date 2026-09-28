# Boxes talk over Tailscale (with TLS on Postgres). The private network is a
# second path inside Hetzner that also carries the standby's scrapes cheaply.
resource "hcloud_network" "main" {
  name     = "adhunters"
  ip_range = "10.20.0.0/16"
  labels   = { platform = "adhunters" }
}

resource "hcloud_network_subnet" "eu_central" {
  network_id   = hcloud_network.main.id
  type         = "cloud"
  network_zone = "eu-central"
  ip_range     = "10.20.1.0/24"
}

# No public ports at all. People arrive through the Cloudflare tunnel, which
# dials out; machines and SSH use Tailscale, which also dials out. The UDP
# port lets Tailscale make direct connections instead of relaying.
resource "hcloud_firewall" "closed" {
  name   = "adhunters-closed"
  labels = { platform = "adhunters" }

  rule {
    direction  = "in"
    protocol   = "icmp"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  rule {
    direction  = "in"
    protocol   = "udp"
    port       = "41641"
    source_ips = ["0.0.0.0/0", "::/0"]
  }
}

resource "hcloud_placement_group" "main" {
  name   = "adhunters-main"
  type   = "spread"
  labels = { platform = "adhunters" }
}
