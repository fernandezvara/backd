# The DigitalOcean project that holds the installation, so the cluster, the database, the Spaces,
# the load balancer and the domain show up together in the control panel (and in its billing view).
#
# Not every resource can belong to a project: the VPC, the registry, the CDN and the certificates
# have no URN, and the firewall and the tag are not project resources. They stay where they are.
#
# The domain already exists (dns.tf reads it): adding it here MOVES it into this project, and
# destroying the project returns it to the default one. Nothing about its records changes.
resource "digitalocean_project" "main" {
  name        = coalesce(var.project_name, var.install)
  description = var.project_description
  purpose     = var.project_purpose
  environment = var.project_environment

  resources = concat(
    [
      digitalocean_kubernetes_cluster.main.urn,
      digitalocean_database_cluster.mongo.urn,
      digitalocean_spaces_bucket.private.urn,
      data.digitalocean_loadbalancer.ingress.urn,
      data.digitalocean_domain.main.urn,
    ],
    digitalocean_spaces_bucket.public[*].urn,
  )
}
