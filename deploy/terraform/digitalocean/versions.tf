terraform {
  required_version = ">= 1.9"

  required_providers {
    digitalocean = {
      source  = "digitalocean/digitalocean"
      version = "~> 2.106"
    }
  }
}

# Credentials come from the environment, so they never reach a file:
#   DIGITALOCEAN_TOKEN                               an API token (read and write)
#   SPACES_ACCESS_KEY_ID, SPACES_SECRET_ACCESS_KEY   a Spaces key that can create buckets
# The variables exist for the cases the environment can't serve; leave them unset otherwise.
provider "digitalocean" {
  token             = var.do_token
  spaces_access_id  = var.spaces_access_id
  spaces_secret_key = var.spaces_secret_key
}
