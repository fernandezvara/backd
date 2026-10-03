# backd's Grafana dashboards

| File | What it shows |
|---|---|
| `dashboards/backd-api.json` | Requests: rate, 5xx ratio, latency, in flight, refusals |
| `dashboards/backd-mongodb.json` | MongoDB command latency, rate and errors, reachability |
| `dashboards/backd-accounts.json` | Sessions, what the limits stop, email and erasure |
| `dashboards/backd-functions.json` | Function runs, job queues, the executor and egress |
| `dashboards/backd-processes.json` | Up or down, versions, memory, CPU, descriptors, Go runtime |
| `provisioning/` | The Prometheus data source (uid `prometheus`) and the dashboard provider |

The local stack (`make example`) runs a Grafana with all of this loaded, at
http://localhost:3000. For your own Grafana, see Operations → Metrics →
Dashboards in the documentation. `scripts/check-dashboards.py` checks the files.
