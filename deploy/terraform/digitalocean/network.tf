locals {
  # Every resource carries the installation's name, and the nodes this tag, which the firewall
  # below selects by.
  node_tag = "${var.install}-nodes"
  tags     = ["backd", "backd-${var.install}"]
}

resource "digitalocean_vpc" "main" {
  name        = "${var.install}-vpc"
  region      = var.region
  ip_range    = var.vpc_ip_range
  description = "backd installation ${var.install}"
}

# The nodes accept nothing from the internet: only the private network answers them (the load
# balancer is in it), and a rule for the load balancer itself is added with it. DigitalOcean
# also keeps a firewall of its own on every cluster for the traffic Kubernetes needs; the rules
# of both add up, and this one never opens a port to the world. The nodes can reach out (image
# pulls, DNS, Let's Encrypt, Spaces): what a function may reach is decided in the cluster, by
# network policies and the egress proxy.
resource "digitalocean_firewall" "nodes" {
  name = "${var.install}-nodes"
  tags = [local.node_tag]

  inbound_rule {
    protocol         = "tcp"
    port_range       = "1-65535"
    source_addresses = [digitalocean_vpc.main.ip_range]
  }

  inbound_rule {
    protocol         = "udp"
    port_range       = "1-65535"
    source_addresses = [digitalocean_vpc.main.ip_range]
  }

  inbound_rule {
    protocol         = "icmp"
    source_addresses = [digitalocean_vpc.main.ip_range]
  }

  outbound_rule {
    protocol              = "tcp"
    port_range            = "1-65535"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }

  outbound_rule {
    protocol              = "udp"
    port_range            = "1-65535"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }

  outbound_rule {
    protocol              = "icmp"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }
}
