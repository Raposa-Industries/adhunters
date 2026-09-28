output "capture_ips" {
  description = "Give both of these to the proxy provider for the allowlist before capture runs from them."
  value       = { for k, ip in hcloud_primary_ip.capture : k => ip.ip_address }
}

output "private_ips" {
  value = { for k, s in hcloud_server.box : k => one(s.network[*].ip) }
}
