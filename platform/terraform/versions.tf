terraform {
  required_version = ">= 1.13.0"

  # State and runs live in HCP Terraform. Create the organization and a
  # workspace named "platform" (execution mode: remote) before the first init.
  cloud {
    organization = "adhunters"
    workspaces {
      name = "platform"
    }
  }

  required_providers {
    hcloud = {
      source  = "hetznercloud/hcloud"
      version = "~> 1.69"
    }
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 5.26"
    }
  }
}

provider "hcloud" {
  token = var.hcloud_token
}

# The tunnel, DNS and Access apps are added here when the first new app
# (Scout) goes up. Access is deliberately not in front of today's spy.
provider "cloudflare" {
  api_token = var.cloudflare_api_token
}
