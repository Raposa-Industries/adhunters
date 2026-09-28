variable "hcloud_token" {
  description = "Hetzner Cloud API token (read/write), set as a sensitive workspace variable in HCP Terraform."
  type        = string
  sensitive   = true
}

variable "cloudflare_api_token" {
  description = "Cloudflare API token. Unused until the tunnel and Access land with Scout."
  type        = string
  sensitive   = true
  default     = null
}

variable "tailscale_auth_key" {
  description = "Tailscale pre-authorized, tagged auth key that a new box uses once to join the tailnet."
  type        = string
  sensitive   = true
}

variable "admin_ssh_keys" {
  description = "Public SSH keys for the admin user, used only over Tailscale."
  type        = list(string)
}

variable "location_main" {
  description = "Where the worker and data boxes run."
  type        = string
  default     = "nbg1"
}

variable "location_standby" {
  description = "Where the standby capture box runs: a different eu-central datacenter from location_main."
  type        = string
  default     = "fsn1"
}

variable "image" {
  type    = string
  default = "ubuntu-24.04"
}
