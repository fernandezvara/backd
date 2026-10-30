# The domain must already exist on DigitalOcean DNS; this fails with "not found" when it doesn't.
data "digitalocean_domain" "main" {
  name = var.domain
}

locals {
  # A record's name is relative to the domain ("@" for the domain itself).
  api_record   = var.api_host == var.domain ? "@" : trimsuffix(var.api_host, ".${var.domain}")
  admin_record = var.admin_host == var.domain ? "@" : trimsuffix(var.admin_host, ".${var.domain}")
}

resource "digitalocean_record" "api" {
  domain = data.digitalocean_domain.main.id
  type   = "A"
  name   = local.api_record
  value  = data.digitalocean_loadbalancer.ingress.ip
  ttl    = var.dns_ttl
}

resource "digitalocean_record" "admin" {
  domain = data.digitalocean_domain.main.id
  type   = "A"
  name   = local.admin_record
  value  = data.digitalocean_loadbalancer.ingress.ip
  ttl    = var.dns_ttl
}

# The public files' name, when a CDN with its own host is in front of the public Space.
resource "digitalocean_record" "assets" {
  count  = local.has_public && var.public_cdn && var.assets_host != "" ? 1 : 0
  domain = data.digitalocean_domain.main.id
  type   = "CNAME"
  name   = var.assets_host == var.domain ? "@" : trimsuffix(var.assets_host, ".${var.domain}")
  value  = "${digitalocean_cdn.public[0].endpoint}."
  ttl    = var.dns_ttl
}
