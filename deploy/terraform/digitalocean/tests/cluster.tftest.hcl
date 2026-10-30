# Offline: the provider is mocked, nothing is created.
mock_provider "digitalocean" {
  mock_resource "digitalocean_kubernetes_cluster" {
    defaults = {
      id = "00000000-0000-0000-0000-000000000001"
      kube_config = [{
        raw_config             = "apiVersion: v1"
        host                   = "https://cluster.example"
        token                  = "token"
        cluster_ca_certificate = "Y2E="
        client_certificate     = ""
        client_key             = ""
        expires_at             = "2030-01-01T00:00:00Z"
      }]
    }
  }

  mock_resource "digitalocean_vpc" {
    defaults = {
      id       = "00000000-0000-0000-0000-000000000002"
      ip_range = "10.30.0.0/16"
    }
  }

  mock_data "digitalocean_kubernetes_versions" {
    defaults = {
      latest_version = "1.33.1-do.0"
    }
  }
}

variables {
  install = "acme"
}

run "defaults_are_two_small_nodes" {
  command = plan

  assert {
    condition     = digitalocean_kubernetes_cluster.main.node_pool[0].size == "s-2vcpu-4gb" && digitalocean_kubernetes_cluster.main.node_pool[0].node_count == 2
    error_message = "the default is two nodes of 2 vCPU / 4 GB"
  }

  assert {
    condition     = !digitalocean_kubernetes_cluster.main.ha && !digitalocean_kubernetes_cluster.main.node_pool[0].auto_scale
    error_message = "no high-availability control plane and no autoscaling by default"
  }
}

run "nodes_take_the_size_and_the_count" {
  command = plan

  variables {
    node_size  = "g-2vcpu-8gb"
    node_count = 3
  }

  assert {
    condition     = digitalocean_kubernetes_cluster.main.node_pool[0].size == "g-2vcpu-8gb" && digitalocean_kubernetes_cluster.main.node_pool[0].node_count == 3
    error_message = "node_size and node_count are used"
  }
}

run "autoscaling_replaces_the_count" {
  command = plan

  variables {
    node_min = 2
    node_max = 5
  }

  assert {
    condition     = digitalocean_kubernetes_cluster.main.node_pool[0].auto_scale && digitalocean_kubernetes_cluster.main.node_pool[0].min_nodes == 2 && digitalocean_kubernetes_cluster.main.node_pool[0].max_nodes == 5
    error_message = "node_min and node_max turn autoscaling on"
  }
}

run "autoscaling_needs_both_ends" {
  command = plan

  variables {
    node_min = 2
  }

  expect_failures = [var.node_max]
}

run "the_range_must_be_ordered" {
  command = plan

  variables {
    node_min = 4
    node_max = 2
  }

  expect_failures = [var.node_max]
}

run "nodes_open_no_port_to_the_world" {
  command = plan

  assert {
    condition = alltrue([
      for r in digitalocean_firewall.nodes.inbound_rule :
      !contains(coalesce(r.source_addresses, []), "0.0.0.0/0") && !contains(coalesce(r.source_addresses, []), "::/0")
    ])
    error_message = "no inbound rule may name the whole internet"
  }
}

run "the_control_plane_can_be_restricted" {
  command = plan

  variables {
    control_plane_allowed_cidrs = ["203.0.113.7/32"]
  }

  assert {
    condition     = digitalocean_kubernetes_cluster.main.control_plane_firewall[0].enabled
    error_message = "the control plane firewall follows control_plane_allowed_cidrs"
  }
}

run "install_name_is_validated" {
  command = plan

  variables {
    install = "Acme_1"
  }

  expect_failures = [var.install]
}

run "the_kubeconfig_is_an_output" {
  command = plan

  assert {
    condition     = output.cluster_name == "acme"
    error_message = "the cluster is named after the installation"
  }
}

run "the_firewall_and_the_nodes_share_a_tag_made_first" {
  command = plan

  assert {
    condition     = digitalocean_tag.nodes.name == "acme-nodes" && contains(digitalocean_firewall.nodes.tags, "acme-nodes") && contains(digitalocean_kubernetes_cluster.main.node_pool[0].tags, "acme-nodes")
    error_message = "the nodes and the firewall use the tag Terraform makes, so the firewall can be created before the nodes exist"
  }
}
