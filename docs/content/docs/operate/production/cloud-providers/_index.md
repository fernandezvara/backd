---
title: "Cloud providers"
description: "Walkthroughs from an empty account to a running installation on a provider: the infrastructure it builds, what it costs, and what that provider cannot do."
icon: "cloud"
weight: 313
toc: false
---

[Production deployment](../) says what an installation needs and [Kubernetes](../kubernetes/) how backd runs on a cluster. A provider page covers *where*: the infrastructure to create (cluster, database, storage, registry, load balancer, DNS), the Terraform that creates it, the cost, and what that provider cannot do.

| Provider | What is there |
|---|---|
| [DigitalOcean](digitalocean/) | Terraform for a DOKS cluster, a managed MongoDB, Spaces, a registry, a load balancer and DNS; then the chart through a pipeline |

Object storage and push providers, which a running backd connects to, are under [Providers](../../providers/).
