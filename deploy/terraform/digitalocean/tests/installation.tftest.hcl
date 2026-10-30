# Offline: the provider is mocked, nothing is created.
mock_provider "helm" {}

mock_provider "kubernetes" {}

mock_provider "digitalocean" {
  mock_data "digitalocean_loadbalancer" {
    defaults = {
      id = "00000000-0000-0000-0000-0000000000lb"
      ip = "203.0.113.10"
    }
  }

  mock_data "digitalocean_domain" {
    defaults = {
      id = "example.com"
    }
  }

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

  mock_resource "digitalocean_container_registry" {
    defaults = {
      endpoint = "registry.digitalocean.com/acme"
    }
  }

  mock_resource "digitalocean_container_registry_docker_credentials" {
    defaults = {
      docker_credentials = "{\"auths\":{}}"
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

  mock_resource "digitalocean_spaces_bucket" {
    defaults = {
      bucket_domain_name = "acme-public.fra1.digitaloceanspaces.com"
    }
  }

  mock_resource "digitalocean_cdn" {
    defaults = {
      endpoint = "acme-public.fra1.cdn.digitaloceanspaces.com"
    }
  }

  mock_data "digitalocean_kubernetes_versions" {
    defaults = {
      latest_version = "1.33.1-do.0"
    }
  }
}

variables {
  install             = "acme"
  private_bucket_name = "acme-files"
  domain              = "example.com"
  api_host            = "api.example.com"
  admin_host          = "admin.example.com"
  acme_email          = "ops@example.com"
  dns_token           = "dns-token"
  database_ca         = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----"
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

  variables {
    database_ca = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----"
  }

  assert {
    condition     = startswith(output.mongo_uri, "mongodb+srv://backd:app%20pw%2F1@private-db.example/admin?") && strcontains(output.mongo_uri, "&tlsCAFile=/etc/backd/tls/ca.pem")
    error_message = "the app URI has the user, the escaped password and the CA path: ${nonsensitive(output.mongo_uri)}"
  }

  assert {
    condition     = startswith(nonsensitive(output.mongo_admin_uri), "mongodb+srv://doadmin:adminpw@")
    error_message = "the admin URI is doadmin's"
  }
}

run "one_private_space_by_default" {
  command = plan

  assert {
    condition     = digitalocean_spaces_bucket.private.acl == "private" && length(digitalocean_spaces_bucket.public) == 0 && length(digitalocean_spaces_key.public) == 0
    error_message = "the default is one private Space and no public one"
  }

  assert {
    condition     = output.spaces_public == null
    error_message = "no public Space: no output for it"
  }
}

run "the_private_space_has_a_key_of_its_own" {
  command = plan

  assert {
    condition     = one(digitalocean_spaces_key.private.grant).bucket == "acme-files" && one(digitalocean_spaces_key.private.grant).permission == "readwrite"
    error_message = "the private key is bound to the private Space"
  }
}

run "private_cors_only_when_origins_are_given" {
  command = plan

  assert {
    condition     = length(digitalocean_spaces_bucket_cors_configuration.private) == 0
    error_message = "no origins, no CORS rule"
  }
}

run "private_cors_carries_the_upload_headers" {
  command = plan

  variables {
    private_cors_origins = ["https://app.example.com"]
  }

  assert {
    condition     = contains(one(digitalocean_spaces_bucket_cors_configuration.private[0].cors_rule).allowed_headers, "x-amz-checksum-sha256") && contains(one(digitalocean_spaces_bucket_cors_configuration.private[0].cors_rule).allowed_methods, "PUT")
    error_message = "direct uploads need PUT and the checksum header"
  }
}

run "two_spaces_make_a_public_one_nobody_can_list" {
  command = plan

  variables {
    buckets            = "private_and_public"
    public_bucket_name = "acme-public"
  }

  assert {
    condition     = digitalocean_spaces_bucket.public[0].acl == "private"
    error_message = "the public Space is not public-read: that ACL allows listing"
  }

  assert {
    condition     = jsondecode(digitalocean_spaces_bucket_policy.public[0].policy).Statement[0].Action == ["s3:GetObject"]
    error_message = "the policy allows reading objects and nothing else"
  }

  assert {
    condition     = one(digitalocean_spaces_key.public[0].grant).bucket == "acme-public"
    error_message = "the public key is bound to the public Space"
  }
}

run "the_public_space_needs_a_name" {
  command = plan

  variables {
    buckets = "private_and_public"
  }

  expect_failures = [var.public_bucket_name]
}

run "the_spaces_need_different_names" {
  command = plan

  variables {
    buckets            = "private_and_public"
    public_bucket_name = "acme-files"
  }

  expect_failures = [var.public_bucket_name]
}

run "bucket_names_are_validated" {
  command = plan

  variables {
    private_bucket_name = "Acme_Files"
  }

  expect_failures = [var.private_bucket_name]
}

run "public_url_without_a_cdn_is_the_space" {
  command = apply

  variables {
    buckets            = "private_and_public"
    public_bucket_name = "acme-public"
  }

  assert {
    condition     = output.spaces_public.url == "https://acme-public.fra1.digitaloceanspaces.com"
    error_message = "without a CDN the files are read from the Space"
  }
}

run "public_url_through_the_cdn_endpoint" {
  command = apply

  variables {
    buckets            = "private_and_public"
    public_bucket_name = "acme-public"
    public_cdn         = true
  }

  assert {
    condition     = output.spaces_public.url == "https://acme-public.fra1.cdn.digitaloceanspaces.com" && length(digitalocean_certificate.assets) == 0
    error_message = "a CDN without a host uses the CDN's own address and needs no certificate"
  }
}

run "public_url_through_the_cdn_host" {
  command = apply

  variables {
    buckets            = "private_and_public"
    public_bucket_name = "acme-public"
    public_cdn         = true
    assets_host        = "assets.example.com"
  }

  assert {
    condition     = output.spaces_public.url == "https://assets.example.com" && length(digitalocean_certificate.assets) == 1 && digitalocean_cdn.public[0].custom_domain == "assets.example.com"
    error_message = "a CDN host serves the files and gets a certificate"
  }
}

run "assets_host_needs_the_cdn" {
  command = plan

  variables {
    buckets            = "private_and_public"
    public_bucket_name = "acme-public"
    assets_host        = "assets.example.com"
  }

  expect_failures = [var.assets_host]
}

run "realm_storage_snippet_names_both_spaces" {
  command = apply

  variables {
    buckets            = "private_and_public"
    public_bucket_name = "acme-public"
  }

  assert {
    condition     = strcontains(output.realm_storage, "bucket: acme-files") && strcontains(output.realm_storage, "public_storage:") && strcontains(output.realm_storage, "url: https://acme-public.fra1.digitaloceanspaces.com")
    error_message = "the snippet has storage: and public_storage:"
  }
}

run "registry_defaults_to_basic_named_after_the_install" {
  command = plan

  assert {
    condition     = digitalocean_container_registry.main.subscription_tier_slug == "basic" && digitalocean_container_registry.main.name == "acme"
    error_message = "the default is a Basic registry named after the installation"
  }
}

run "registry_takes_the_starter_tier" {
  command = plan

  variables {
    registry_tier = "starter"
    registry_name = "acme-registry"
  }

  assert {
    condition     = digitalocean_container_registry.main.subscription_tier_slug == "starter" && digitalocean_container_registry.main.name == "acme-registry"
    error_message = "registry_tier and registry_name are used"
  }
}

run "registry_tier_is_validated" {
  command = plan

  variables {
    registry_tier = "enterprise"
  }

  expect_failures = [var.registry_tier]
}

run "the_cluster_pulls_through_the_integration" {
  command = plan

  assert {
    condition     = digitalocean_kubernetes_cluster.main.registry_integration
    error_message = "the cluster is integrated with the registry"
  }
}

run "the_pull_credential_cannot_push_and_the_push_one_can" {
  command = plan

  assert {
    condition     = digitalocean_container_registry_docker_credentials.push.write && !digitalocean_container_registry_docker_credentials.pull.write
    error_message = "the pipeline pushes; the cluster side only reads"
  }
}

run "push_credential_expiry_is_in_seconds" {
  command = plan

  variables {
    registry_push_credential_days = 30
  }

  assert {
    condition     = digitalocean_container_registry_docker_credentials.push.expiry_seconds == 2592000
    error_message = "30 days are 2592000 seconds"
  }
}

run "dns_points_the_names_at_the_load_balancer" {
  command = apply

  assert {
    condition     = digitalocean_record.api.name == "api" && digitalocean_record.api.value == "203.0.113.10" && digitalocean_record.admin.name == "admin" && digitalocean_record.admin.value == "203.0.113.10"
    error_message = "api and admin are A records of the load balancer's address"
  }

  assert {
    condition     = digitalocean_record.api.ttl == 300 && length(digitalocean_record.assets) == 0
    error_message = "the TTL defaults to 300 and there is no assets record without a CDN host"
  }
}

run "the_domain_itself_is_the_at_record" {
  command = apply

  variables {
    api_host = "example.com"
  }

  assert {
    condition     = digitalocean_record.api.name == "@"
    error_message = "the domain itself is @"
  }
}

run "assets_is_a_cname_of_the_cdn" {
  command = apply

  variables {
    buckets            = "private_and_public"
    public_bucket_name = "acme-public"
    public_cdn         = true
    assets_host        = "assets.example.com"
  }

  assert {
    condition     = digitalocean_record.assets[0].type == "CNAME" && digitalocean_record.assets[0].name == "assets" && endswith(digitalocean_record.assets[0].value, ".")
    error_message = "the assets name is a CNAME of the CDN endpoint, written as a full name"
  }
}

run "hosts_must_be_inside_the_domain" {
  command = plan

  variables {
    api_host = "api.other.org"
  }

  expect_failures = [var.api_host]
}

run "admin_and_api_are_different_names" {
  command = plan

  variables {
    admin_host = "api.example.com"
  }

  expect_failures = [var.admin_host]
}

run "lb_size_is_validated" {
  command = plan

  variables {
    lb_size = "lb-huge"
  }

  expect_failures = [var.lb_size]
}

run "ingress_keeps_the_client_address_and_the_one_load_balancer" {
  command = plan

  variables {
    lb_size      = "lb-medium"
    lb_algorithm = "least_connections"
  }

  assert {
    condition     = strcontains(helm_release.traefik.values[0], "do-loadbalancer-name: acme-ingress") && strcontains(helm_release.traefik.values[0], "do-loadbalancer-size-slug: lb-medium") && strcontains(helm_release.traefik.values[0], "do-loadbalancer-algorithm: least_connections")
    error_message = "the Service asks for the load balancer the variables describe"
  }

  assert {
    condition     = strcontains(helm_release.traefik.values[0], "do-loadbalancer-enable-proxy-protocol: \"true\"") && strcontains(helm_release.traefik.values[0], "externalTrafficPolicy: Local") && strcontains(helm_release.traefik.values[0], "do-loadbalancer-hostname: api.example.com")
    error_message = "the PROXY protocol, Local traffic and the hostname keep the client's address"
  }

  assert {
    condition     = strcontains(helm_release.traefik.values[0], "10.30.0.0/16") && !strcontains(helm_release.traefik.values[0], "0.0.0.0/0")
    error_message = "only the private network may send the PROXY protocol"
  }
}

run "the_node_firewall_needs_nothing_that_is_unknown_at_plan" {
  command = plan

  # A rule that named the load balancer (made later, by Traefik's Service) put an unknown value in a
  # list, which DigitalOcean's provider rejects with "Null value found in list".
  assert {
    condition     = alltrue([for r in digitalocean_firewall.nodes.inbound_rule : length(coalesce(r.source_load_balancer_uids, [])) == 0])
    error_message = "no inbound rule may name the load balancer: it does not exist at plan time"
  }
}

run "cert_manager_answers_dns_challenges_with_its_own_token" {
  command = plan

  assert {
    condition     = kubernetes_secret.dns_token.metadata[0].namespace == "cert-manager" && helm_release.cert_manager.version == "v1.21.3"
    error_message = "the DNS token lives in cert-manager's namespace and the chart is pinned"
  }

  assert {
    condition     = strcontains(helm_release.cluster_issuer.values[0], "acme-v02.api.letsencrypt.org") && strcontains(helm_release.cluster_issuer.values[0], "ops@example.com")
    error_message = "the issuer uses Let's Encrypt production and the email"
  }
}

run "staging_issuer_when_asked" {
  command = plan

  variables {
    acme_staging = true
  }

  assert {
    condition     = strcontains(helm_release.cluster_issuer.values[0], "acme-staging-v02")
    error_message = "acme_staging selects the staging service"
  }
}

run "acme_email_and_dns_token_are_validated" {
  command = plan

  variables {
    acme_email = "not an email"
    dns_token  = ""
  }

  expect_failures = [var.acme_email, var.dns_token]
}

run "the_namespace_is_restricted" {
  command = plan

  assert {
    condition     = kubernetes_namespace.backd.metadata[0].name == "backd" && kubernetes_namespace.backd.metadata[0].labels["pod-security.kubernetes.io/enforce"] == "restricted"
    error_message = "backd's namespace enforces the restricted pod security standard"
  }
}

run "the_secrets_hold_what_the_chart_reads" {
  command = apply

  assert {
    condition = alltrue([for k in ["BACKD_SECRETS_KEY", "BACKD_EXECUTOR_TOKEN", "BACKD_CALLBACK_KEY", "BACKD_EGRESS_KEY", "BACKD_METRICS_TOKEN"] :
    contains(keys(kubernetes_secret.keys.data), k) && length(kubernetes_secret.keys.data[k]) >= 32])
    error_message = "backd-keys has the five keys, each at least 32 characters (the minimum backd accepts)"
  }

  assert {
    condition     = length(distinct(values(nonsensitive(kubernetes_secret.keys.data)))) == 5
    error_message = "the five keys differ"
  }

  assert {
    condition     = contains(keys(kubernetes_secret.mongo.data), "MONGO_URI") && contains(keys(kubernetes_secret.mongo.data), "MONGO_ADMIN_URI") && contains(keys(kubernetes_secret.mongo.data), "ca.pem")
    error_message = "backd-mongo has both URIs and the CA"
  }

  assert {
    condition     = kubernetes_secret.registry.type == "kubernetes.io/dockerconfigjson" && contains(keys(kubernetes_secret.registry.data), ".dockerconfigjson")
    error_message = "backd-registry is a Docker config Secret"
  }
}

run "the_install_configmap_describes_the_installation" {
  command = apply

  assert {
    condition     = kubernetes_config_map.install.data["BACKD_URL"] == "https://api.example.com" && kubernetes_config_map.install.data["BACKD_DEPLOYMENT_ID"] == "acme" && kubernetes_config_map.install.data["BACKD_ENV"] == "production" && kubernetes_config_map.install.data["PRIVATE_BUCKET"] == "acme-files"
    error_message = "the ConfigMap names the URL, the deployment, the environment and the bucket"
  }

  assert {
    condition     = !contains(keys(kubernetes_config_map.install.data), "PUBLIC_BUCKET")
    error_message = "no public bucket, no entry for it"
  }
}

run "no_secret_is_in_the_configmap" {
  command = apply

  assert {
    condition     = alltrue([for v in values(kubernetes_config_map.install.data) : !strcontains(v, "password") && !strcontains(v, "token") && !startswith(v, "mongodb")])
    error_message = "the ConfigMap holds no credential"
  }
}

run "a_space_key_cannot_be_fullaccess" {
  command = plan

  variables {
    spaces_key_permission = "fullaccess"
  }

  expect_failures = [var.spaces_key_permission]
}

run "the_firewall_and_the_nodes_share_a_tag_made_first" {
  command = plan

  assert {
    condition     = digitalocean_tag.nodes.name == "acme-nodes" && contains(digitalocean_firewall.nodes.tags, "acme-nodes") && contains(digitalocean_kubernetes_cluster.main.node_pool[0].tags, "acme-nodes")
    error_message = "the nodes and the firewall use the tag Terraform makes, so the firewall can be created before the nodes exist"
  }
}

run "no_ca_leaves_the_option_out_and_the_system_roots_verify" {
  command = apply

  variables {
    database_ca = null
  }

  assert {
    condition     = !strcontains(nonsensitive(output.mongo_uri), "tlsCAFile") && strcontains(nonsensitive(output.mongo_uri), "tls=true")
    error_message = "without the CA the URI does not name a file that is not there, and still requires TLS"
  }

  assert {
    condition     = output.mongo_ca == ""
    error_message = "no CA is invented"
  }
}

run "a_given_ca_is_named_in_the_uri_and_the_output" {
  command = apply

  variables {
    database_ca = "-----BEGIN CERTIFICATE-----\nMIIB-given\n-----END CERTIFICATE-----"
  }

  assert {
    condition     = strcontains(output.mongo_ca, "MIIB-given") && strcontains(nonsensitive(output.mongo_uri), "tlsCAFile=/etc/backd/tls/ca.pem")
    error_message = "a given CA is in the output and the URI points at the mounted file"
  }
}

run "the_ca_must_be_a_certificate" {
  command = plan

  variables {
    database_ca = "not a certificate"
  }

  expect_failures = [var.database_ca]
}
