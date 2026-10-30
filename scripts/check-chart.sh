#!/bin/sh
# Checks the Helm chart of deploy/kubernetes/backd without a cluster: helm lint and template with
# every values file of backd/ci/, kubeconform on what they render, and the invariants of
# scripts/test-chart.py (security contexts, who gets which secret, where the trigger lease is...).
#
# Uses helm and kubeconform when they are installed, else runs them from their container images
# (docker or podman). Usage: scripts/check-chart.sh
set -eu
cd "$(dirname "$0")/.."
chart=deploy/kubernetes/backd
k8s_version="${KUBERNETES_VERSION:-1.33.0}"

runtime=""
for r in docker podman; do
  if command -v "$r" >/dev/null 2>&1; then runtime="$r"; break; fi
done

helm_run() {
  if command -v helm >/dev/null 2>&1; then
    helm "$@"
  else
    [ -n "$runtime" ] || { echo "check-chart: neither helm nor docker/podman is installed" >&2; exit 1; }
    "$runtime" run --rm --entrypoint helm -v "$PWD":/w:Z -w /w docker.io/alpine/helm:3.17.3 "$@"
  fi
}

kubeconform_run() {
  if command -v kubeconform >/dev/null 2>&1; then
    kubeconform "$@"
  else
    [ -n "$runtime" ] || { echo "check-chart: neither kubeconform nor docker/podman is installed" >&2; exit 1; }
    "$runtime" run --rm -i ghcr.io/yannh/kubeconform:latest "$@"
  fi
}

out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT

for values in "$chart"/ci/*.yaml; do
  name=$(basename "$values" .yaml)
  echo "== $name"
  helm_run lint "$chart" -f "$values" --strict >/dev/null
  helm_run template backd "$chart" -f "$values" --namespace backd > "$out/$name.yaml"
  # The Traefik and cert-manager objects are custom resources: their schemas come from the CRDs
  # catalog; a kind it doesn't know is skipped, not failed.
  kubeconform_run -strict -summary -kubernetes-version "$k8s_version" \
    -schema-location default \
    -schema-location 'https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json' \
    -ignore-missing-schemas - < "$out/$name.yaml"
done

# A chart that needs values it wasn't given must say so instead of rendering something half-made.
if helm_run template backd "$chart" >/dev/null 2>"$out/err"; then
  echo "check-chart: the chart rendered without image.repository" >&2
  exit 1
fi
grep -q "image.repository is required" "$out/err" || { echo "check-chart: the missing-image error is not clear:" >&2; cat "$out/err" >&2; exit 1; }

# The admin host is never opened to everyone: no allowlist, no chart.
if helm_run template backd "$chart" -f "$chart/ci/default.yaml" --set 'ingress.admin.allowedCIDRs=null' >/dev/null 2>"$out/err"; then
  echo "check-chart: the chart rendered with an empty admin allowlist" >&2
  exit 1
fi
grep -q "ingress.admin.allowedCIDRs is required" "$out/err" || { echo "check-chart: the empty-allowlist error is not clear:" >&2; cat "$out/err" >&2; exit 1; }

python3 scripts/test-chart.py "$out"
echo "check-chart: ok"
