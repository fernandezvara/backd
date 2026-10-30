#!/usr/bin/env python3
"""Invariants of the rendered Helm chart (scripts/check-chart.sh renders it into a directory).

These are the properties the design promises (design/kubernetes.md): pods are restricted, each
workload gets only the secrets it uses, the database is reached only by the pods that need it,
the trigger lease and the change stream are on the realtime pods, and a rollback re-provisions.
"""
import os
import sys

import yaml


def load(path):
    with open(path) as f:
        return [d for d in yaml.safe_load_all(f) if d]


def by_kind(docs, kind):
    return [d for d in docs if d["kind"] == kind]


def named(docs, kind, name):
    for d in by_kind(docs, kind):
        if d["metadata"]["name"] == name:
            return d
    raise AssertionError(f"no {kind} named {name}")


def env_of(deployment):
    c = deployment["spec"]["template"]["spec"]["containers"][0]
    return {e["name"]: e for e in c.get("env", [])}


def check(cond, message):
    if not cond:
        print(f"FAIL: {message}", file=sys.stderr)
        sys.exit(1)


def secret_names(env):
    return {n for n, e in env.items() if "valueFrom" in e and "secretKeyRef" in e["valueFrom"]}


SERVING = ["backd", "backd-realtime", "backd-admin"]
WORKLOADS = SERVING + ["worker", "executor", "egress"]


def main(out):
    default = load(os.path.join(out, "default.yaml"))
    small = load(os.path.join(out, "small.yaml"))
    scaled = load(os.path.join(out, "scaled.yaml"))

    # --- every pod is restricted
    for name in WORKLOADS:
        d = named(default, "Deployment", name)
        spec = d["spec"]["template"]["spec"]
        check(spec["securityContext"]["runAsNonRoot"] is True, f"{name} must run as non-root")
        check(spec["securityContext"]["seccompProfile"]["type"] == "RuntimeDefault", f"{name} needs the RuntimeDefault seccomp profile")
        check(spec["automountServiceAccountToken"] is False, f"{name} must not mount a service account token")
        for c in spec["containers"]:
            sc = c["securityContext"]
            check(sc["readOnlyRootFilesystem"] is True, f"{name} needs a read-only root filesystem")
            check(sc["allowPrivilegeEscalation"] is False, f"{name} must not allow privilege escalation")
            check(sc["capabilities"]["drop"] == ["ALL"], f"{name} must drop all capabilities")
            check("limits" in c["resources"] and "requests" in c["resources"], f"{name} needs requests and limits")
            check(not c["image"].endswith(":latest"), f"{name} must not use :latest")

    # --- the configuration image only where the configuration is read
    config_image = "registry.example.com/acme/acme:2026-10-30-1"
    for name in SERVING + ["worker"]:
        image = named(default, "Deployment", name)["spec"]["template"]["spec"]["containers"][0]["image"]
        check(image == config_image, f"{name} must run the configuration image, got {image}")
    check(named(default, "Deployment", "executor")["spec"]["template"]["spec"]["containers"][0]["image"] == "ghcr.io/fernandezvara/backd-executor:v0.9.6", "the executor runs the released executor image")
    check(named(default, "Deployment", "egress")["spec"]["template"]["spec"]["containers"][0]["image"] == "ghcr.io/fernandezvara/backd:v0.9.6", "egress runs the released backd image")

    # --- what each role is, from the environment
    def flag(name, key):
        return env_of(named(default, "Deployment", name))[key]["value"]

    check(flag("backd", "BACKD_ADMIN_API") == "false" and flag("backd-realtime", "BACKD_ADMIN_API") == "false" and flag("backd-admin", "BACKD_ADMIN_API") == "true", "only backd-admin serves the admin API")
    check(flag("backd-admin", "BACKD_ADMIN_UI") == "true" and flag("backd", "BACKD_ADMIN_UI") == "false", "only backd-admin serves the interface")
    check(flag("backd-realtime", "BACKD_EVENTS") == "true" and flag("backd-realtime", "BACKD_TRIGGERS") == "true", "the realtime pods read the change stream and hold the trigger lease")
    for name in ["backd", "backd-admin"]:
        check(flag(name, "BACKD_EVENTS") == "false" and flag(name, "BACKD_TRIGGERS") == "false", f"{name} must not read the change stream or hold the trigger lease")
    for name in SERVING + ["worker"]:
        check(flag(name, "PROVISION_MODE") == "verify", f"{name} must run in verify mode")
    for name in SERVING:
        check(flag(name, "BACKD_CALLBACK_URL") == f"http://{name}:8081", f"{name} must call back on its own internal listener")

    # --- each workload gets the secrets it uses and no more
    keys = lambda name: {env_of(named(default, "Deployment", name))[n]["valueFrom"]["secretKeyRef"]["key"] for n in secret_names(env_of(named(default, "Deployment", name)))}
    check({"MONGO_URI", "BACKD_SECRETS_KEY", "BACKD_CALLBACK_KEY", "BACKD_EXECUTOR_TOKEN", "BACKD_METRICS_TOKEN"} == keys("backd"), f"backd secrets: {keys('backd')}")
    check("BACKD_EGRESS_KEY" in keys("worker") and "MONGO_URI" in keys("worker"), "the worker needs the egress key and the database")
    check(keys("executor") == {"BACKD_EXECUTOR_TOKEN", "BACKD_EGRESS_KEY", "BACKD_METRICS_TOKEN"}, f"the executor must never get the database, the callback key or the secrets key: {keys('executor')}")
    check(keys("egress") == {"BACKD_EGRESS_KEY", "BACKD_METRICS_TOKEN"}, f"egress secrets: {keys('egress')}")
    for name in WORKLOADS:
        for e in env_of(named(default, "Deployment", name)).values():
            check("MONGO_ADMIN_URI" not in str(e), f"{name} must not see the administrative database user")

    # --- no literal secret anywhere
    for doc in default:
        text = yaml.safe_dump(doc)
        for needle in ("mongodb://", "mongodb+srv://", "BEGIN CERTIFICATE"):
            check(needle not in text, f"{doc['kind']}/{doc['metadata']['name']} contains {needle}")

    # --- the provisioning Job: the admin user, and it runs before an install, an upgrade and a rollback
    job = named(default, "Job", "backd-provision")
    hooks = job["metadata"]["annotations"]["helm.sh/hook"].split(",")
    check(set(hooks) == {"pre-install", "pre-upgrade", "pre-rollback"}, f"the provision hook runs on {hooks}")
    jenv = {e["name"]: e for e in job["spec"]["template"]["spec"]["containers"][0]["env"]}
    check(jenv["MONGO_URI"]["valueFrom"]["secretKeyRef"]["key"] == "MONGO_ADMIN_URI", "provisioning uses the administrative user")
    check("PROVISION_MODE" not in jenv, "provisioning applies, it does not verify")

    # --- replicas: the default, the overrides
    rep = lambda docs, name: named(docs, "Deployment", name)["spec"]["replicas"]
    check((rep(default, "backd"), rep(default, "backd-realtime"), rep(default, "backd-admin"), rep(default, "worker"), rep(default, "executor"), rep(default, "egress")) == (2, 2, 1, 1, 2, 1), "default replicas")
    check((rep(small, "backd"), rep(small, "executor"), rep(small, "egress")) == (1, 1, 1), "replicas.default applies to every component without its own number")
    check((rep(scaled, "backd"), rep(scaled, "backd-realtime"), rep(scaled, "backd-admin"), rep(scaled, "worker"), rep(scaled, "executor"), rep(scaled, "egress")) == (6, 3, 2, 4, 4, 2), "per-component replicas")

    # --- disruption budgets only where there is more than one pod
    pdbs = {d["metadata"]["name"] for d in by_kind(default, "PodDisruptionBudget")}
    check(pdbs == {"backd", "backd-realtime", "executor"}, f"PodDisruptionBudgets: {pdbs}")
    check(not by_kind(small, "PodDisruptionBudget"), "no PodDisruptionBudget in the small values")

    # --- scaled values reach the pods
    check(env_of(named(scaled, "Deployment", "backd"))["BACKD_AGGREGATE_TIME"]["value"] == "10s", "env reaches the backd pods")
    check(env_of(named(scaled, "Deployment", "executor"))["EXECUTOR_MAX_PROCESSES"]["value"] == "20", "executor.maxProcesses")
    check(env_of(named(scaled, "Deployment", "worker"))["WORKER_CONCURRENCY"]["value"] == "20", "workerConcurrency")

    # --- the executor works in memory
    vol = named(default, "Deployment", "executor")["spec"]["template"]["spec"]["volumes"][0]
    check(vol["emptyDir"]["medium"] == "Memory", "the executor's working directory is in memory")

    # --- network policies
    pols = {d["metadata"]["name"]: d for d in by_kind(default, "NetworkPolicy")}
    check("backd-default-deny" in pols and pols["backd-default-deny"]["spec"]["podSelector"] == {}, "a default-deny policy for the namespace")
    check(set(pols["backd-default-deny"]["spec"]["policyTypes"]) == {"Ingress", "Egress"}, "default-deny covers both directions")
    db = pols["backd-allow-database-and-https"]["spec"]
    selected = db["podSelector"]["matchExpressions"][0]["values"]
    check("executor" not in selected and "egress" not in selected, f"the executor and egress must not reach MongoDB: {selected}")
    check(set(selected) == {"backd", "backd-realtime", "backd-admin", "worker", "provision"}, f"database pods: {selected}")
    check(db["egress"][0]["to"][0]["ipBlock"]["cidr"] == "10.30.0.0/16", "the database range comes from the values")
    ex = pols["backd-executor-egress"]["spec"]["egress"]
    check(all("ipBlock" not in t for r in ex for t in r["to"]), "the executor has no route outside the cluster")
    check("backd-allow-metrics" not in pols, "no metrics policy without a monitoring namespace")
    check("backd-allow-metrics" in {d["metadata"]["name"] for d in by_kind(scaled, "NetworkPolicy")}, "a monitoring namespace adds the metrics policy")
    check(not by_kind(small, "NetworkPolicy"), "networkPolicy.enabled=false renders no policy")

    # --- the Services
    svc = {d["metadata"]["name"]: d for d in by_kind(default, "Service")}
    check(set(svc) == {"backd", "backd-realtime", "backd-admin", "executor", "egress"}, f"Services: {sorted(svc)}")
    check(all(s["spec"]["type"] == "ClusterIP" for s in svc.values()), "no Service is exposed outside the cluster")

    print("test-chart: ok")


if __name__ == "__main__":
    main(sys.argv[1])
