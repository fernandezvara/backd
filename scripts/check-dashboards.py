#!/usr/bin/env python3
"""Checks backd's Grafana dashboards (deploy/grafana/dashboards).

  scripts/check-dashboards.py                          static checks
  scripts/check-dashboards.py --prometheus http://localhost:9090
                                                       also run every query against a Prometheus

Static checks: every file is valid JSON with a unique uid and title; every panel
uses the "prometheus" data source and has a query; every backd_ metric a query
uses is documented in docs/content/docs/operations/metrics.md; every alert rule
in deploy/production/prometheus/alerts.yml is named in some panel's description
("Alert <Name>"), so each alert has a panel showing what it watches.
With --prometheus, each query must also be accepted by Prometheus (it doesn't
need data), with the dashboard variables replaced by "match everything".
"""
import argparse, glob, json, os, re, sys, urllib.parse, urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DASH = os.path.join(ROOT, "deploy/grafana/dashboards")
DOCS = os.path.join(ROOT, "docs/content/docs/operations/metrics.md")
ALERTS = os.path.join(ROOT, "deploy/production/prometheus/alerts.yml")

errors = []
def err(msg): errors.append(msg)

def panels(doc):
    for p in doc.get("panels", []):
        yield p
        yield from p.get("panels", [])

def exprs(doc):
    for p in panels(doc):
        for t in p.get("targets", []):
            yield p, t.get("expr", "")
    for v in doc.get("templating", {}).get("list", []):
        yield None, v.get("definition", "")

documented = set(re.findall(r"^\| `([a-z_]+)`", open(DOCS).read(), re.M))
alerts = re.findall(r"- alert: (\w+)", open(ALERTS).read())

seen_uid, seen_title, descriptions, queries = set(), set(), [], []
files = sorted(glob.glob(os.path.join(DASH, "*.json")))
if not files:
    err("no dashboards in deploy/grafana/dashboards")
for path in files:
    name = os.path.basename(path)
    try:
        doc = json.load(open(path))
    except ValueError as e:
        err(f"{name}: not valid JSON: {e}")
        continue
    for key in ("uid", "title"):
        if not doc.get(key):
            err(f"{name}: no {key}")
    if doc.get("uid") in seen_uid:
        err(f"{name}: uid {doc.get('uid')} is used twice")
    if doc.get("title") in seen_title:
        err(f"{name}: title {doc.get('title')!r} is used twice")
    seen_uid.add(doc.get("uid")); seen_title.add(doc.get("title"))
    for p in panels(doc):
        if p.get("type") == "row":
            continue
        descriptions.append(p.get("description", ""))
        if p.get("datasource", {}).get("uid") != "prometheus":
            err(f"{name}: panel {p.get('title')!r} doesn't use the prometheus data source")
        if not p.get("targets"):
            err(f"{name}: panel {p.get('title')!r} has no query")
    for p, e in exprs(doc):
        where = f"{name}: {p.get('title')!r}" if p else f"{name}: a variable"
        if not e.strip():
            err(f"{where}: empty query")
            continue
        queries.append((where, e))
        for metric in set(re.findall(r"\bbackd_[a-z_]+", e)):
            base = re.sub(r"_(bucket|count|sum)$", "", metric)
            short = base[len("backd_"):]
            if short not in documented and metric[len("backd_"):] not in documented:
                err(f"{where}: {metric} is not in the Metrics page of the documentation")

text = " ".join(descriptions)
for a in alerts:
    if f"Alert {a}" not in text:
        err(f"alert {a} has no panel whose description says \"Alert {a}\"")

ap = argparse.ArgumentParser()
ap.add_argument("--prometheus", help="Prometheus URL to run every query against")
args = ap.parse_args()
if args.prometheus:
    def prepare(e):
        e = e.replace("$__rate_interval", "5m").replace("$__interval", "1m")
        return re.sub(r"\$(route|realm|function|operation|role)\b", ".*", e)
    for where, e in queries:
        if e.startswith("label_values("):
            continue
        url = args.prometheus.rstrip("/") + "/api/v1/query?" + urllib.parse.urlencode({"query": prepare(e)})
        try:
            body = json.load(urllib.request.urlopen(url, timeout=20))
        except Exception as ex:  # an HTTP 400 carries Prometheus' message
            msg = getattr(ex, "read", lambda: b"")().decode(errors="replace") or str(ex)
            err(f"{where}: Prometheus refused the query: {msg[:200]}")
            continue
        if body.get("status") != "success":
            err(f"{where}: {body}")

if errors:
    print("\n".join(errors), file=sys.stderr)
    print(f"{len(errors)} problem(s)", file=sys.stderr)
    sys.exit(1)
print(f"{len(files)} dashboards, {len(queries)} queries checked" + (" (and run against Prometheus)" if args.prometheus else ""))
