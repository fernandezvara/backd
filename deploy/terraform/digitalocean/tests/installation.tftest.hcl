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

  mock_resource "digitalocean_database_cluster" {
    defaults = {
      id          = "00000000-0000-0000-0000-000000000003"
      private_uri = "mongodb+srv://doadmin:adminpw@private-db.example/admin?tls=true&authSource=admin&replicaSet=acme-mongo"
      user        = "doadmin"
      password    = "adminpw"
    }
  }

  mock_resource "digitalocean_database_user" {
    defaults = {
      password = "app pw/1"
    }
  }

  mock_data "digitalocean_database_ca" {
    defaults = {
      certificate = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----"
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

run "database_defaults_to_one_node" {
  command = apply

  assert {
    condition     = digitalocean_database_cluster.mongo.engine == "mongodb" && digitalocean_database_cluster.mongo.node_count == 1 && digitalocean_database_cluster.mongo.size == "db-s-1vcpu-2gb"
    error_message = "the default is one node of db-s-1vcpu-2gb"
  }

  assert {
    condition     = !strcontains(local.mongo_uri, "w=majority")
    error_message = "a one-node cluster has no majority to wait for"
  }
}

run "database_takes_three_nodes" {
  command = apply

  variables {
    database_nodes = 3
    database_size  = "db-s-2vcpu-4gb"
  }

  assert {
    condition     = digitalocean_database_cluster.mongo.node_count == 3 && digitalocean_database_cluster.mongo.size == "db-s-2vcpu-4gb"
    error_message = "database_nodes and database_size are used"
  }

  assert {
    condition     = strcontains(local.mongo_uri, "&w=majority")
    error_message = "three nodes write to a majority"
  }
}

run "database_refuses_two_nodes" {
  command = plan

  variables {
    database_nodes = 2
  }

  expect_failures = [var.database_nodes]
}

run "database_size_is_validated" {
  command = plan

  variables {
    database_size = "s-2vcpu-4gb"
  }

  expect_failures = [var.database_size]
}

run "database_is_open_to_the_cluster_only" {
  command = plan

  assert {
    condition     = length(digitalocean_database_firewall.mongo.rule) == 1 && one(digitalocean_database_firewall.mongo.rule).type == "k8s"
    error_message = "the only trusted source is the cluster"
  }
}

run "database_extra_addresses_are_added" {
  command = plan

  variables {
    database_allowed_cidrs = ["203.0.113.7/32"]
  }

  assert {
    condition     = length(digitalocean_database_firewall.mongo.rule) == 2
    error_message = "an allowed address adds a rule"
  }
}

run "uris_carry_the_ca_and_escape_the_password" {
  command = apply

  assert {
    condition     = startswith(output.mongo_uri, "mongodb+srv://backd:app%20pw%2F1@private-db.example/admin?") && strcontains(output.mongo_uri, "&tlsCAFile=/etc/backd/tls/ca.pem")
    error_message = "the app URI has the user, the escaped password and the CA path: ${nonsensitive(output.mongo_uri)}"
  }

  assert {
    condition     = startswith(nonsensitive(output.mongo_admin_uri), "mongodb+srv://doadmin:adminpw@")
    error_message = "the admin URI is doadmin's"
  }
}

run "the_firewall_and_the_nodes_share_a_tag_made_first" {
  command = plan

  assert {
    condition     = digitalocean_tag.nodes.name == "acme-nodes" && contains(digitalocean_firewall.nodes.tags, "acme-nodes") && contains(digitalocean_kubernetes_cluster.main.node_pool[0].tags, "acme-nodes")
    error_message = "the nodes and the firewall use the tag Terraform makes, so the firewall can be created before the nodes exist"
  }
}

run "the_ca_comes_from_the_api_by_default" {
  command = plan

  assert {
    condition     = length(data.digitalocean_database_ca.mongo) == 1
    error_message = "without database_ca the CA is read from the API"
  }
}

run "the_ca_can_be_given_when_the_api_has_none" {
  command = apply

  variables {
    database_ca = "-----BEGIN CERTIFICATE-----\nMIIB-given\n-----END CERTIFICATE-----"
  }

  assert {
    condition     = length(data.digitalocean_database_ca.mongo) == 0 && strcontains(output.mongo_ca, "MIIB-given")
    error_message = "a given CA is used and the API is not asked"
  }
}

run "the_ca_must_be_a_certificate" {
  command = plan

  variables {
    database_ca = "not a certificate"
  }

  expect_failures = [var.database_ca]
}
