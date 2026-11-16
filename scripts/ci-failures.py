#!/usr/bin/env python3
"""Lists what failed in CI lately and what failed at random.

  scripts/ci-failures.py              the last 15 days
  scripts/ci-failures.py --days 30
  scripts/ci-failures.py --json out.json

Uses the authenticated `gh`. For every workflow run of the window it reads the attempts that
failed (a run that was re-run keeps its first attempt), then the log of each failed job, and
groups the failures by job and by what failed in it: a Go test, a Playwright test, a pytest
test, a check of the production reference, or an infrastructure message (a package mirror,
the Go module proxy, an image registry). "retried" means the attempt failed and a later
attempt of the same run passed: the failure had no cause in the commit, so it is a flake or
an infrastructure failure. Failures that were not retried are real or still unexplained; read
them before you call them flaky.
"""
import argparse, collections, concurrent.futures as cf, datetime, json, re, subprocess

REPO = "fernandezvara/backd"

# What to look for in the log of a failed job: (regular expression, label prefix).
TESTS = (
    (r"--- FAIL: (\S+) \(", "go test"),
    (r"FAIL (backd refuses[^\n]{0,60}|the refusal[^\n]{0,60}|[a-z][^\n]{0,70})", "production reference"),
    (r"(e2e/[a-z-]+\.spec\.ts:\d+:\d+ › [^\n]{0,70})", "playwright"),
    (r"FAILED (tests/\S+)", "pytest"),
)
INFRA = (
    (r"Failed to fetch|Unable to fetch some archives|Installation process exited with code: 100", "package mirror (apt)"),
    (r"read \"https://proxy\.golang\.org[^\n]*|proxy\.golang\.org[^\n]*(timeout|EOF|reset by peer|unexpected)", "Go module proxy"),
    (r"toomanyrequests|502 Bad Gateway|unexpected status[^\n]{0,40}50\d", "image registry"),
)


def gh(*args):
    return subprocess.run(["gh", "api", *args], capture_output=True, text=True).stdout


def runs(days):
    out = []
    for i in range(days + 1):
        day = (datetime.date.today() - datetime.timedelta(days=i)).isoformat()
        text = gh("--paginate", f"repos/{REPO}/actions/runs?created={day}&per_page=100", "--jq",
                  '.workflow_runs[]|[.id,.name,.head_branch,.run_attempt,.conclusion]|@tsv')
        out += [l.split("\t") for l in text.splitlines()]
    return out


def failed_jobs(run):
    rid, name, branch, attempt, conclusion = run
    attempt = int(attempt)
    attempts = list(range(1, attempt)) + ([attempt] if conclusion == "failure" else [])
    res = []
    for a in attempts:
        text = gh(f"repos/{REPO}/actions/runs/{rid}/attempts/{a}/jobs?per_page=100", "--jq",
                  '.jobs[]|select(.conclusion=="failure")|[.id,.name]|@tsv')
        for l in text.splitlines():
            jid, jname = l.split("\t")
            res.append({"run": rid, "workflow": name, "branch": branch, "attempt": a, "attempts": attempt,
                        "job": jid, "name": jname, "retried": a < attempt})
    return res


def what_failed(j):
    log = gh(f"repos/{REPO}/actions/jobs/{j['job']}/logs")
    found = set()
    for rx, label in TESTS:
        for m in re.finditer(rx, log, re.M):
            found.add(f"{label}: {m.group(1).strip()[:100]}")
    for rx, label in INFRA:
        if re.search(rx, log):
            found.add(f"infrastructure: {label}")
    if "building and starting the stack" in log and not re.search(r"check\(s\) (failed|passed)|\d+ passed", log):
        found.add("production reference: the stack did not come up (build or start)")
    j["what"] = sorted(found) or ["(see the log)"]
    return j


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--days", type=int, default=15)
    ap.add_argument("--json")
    args = ap.parse_args()
    with cf.ThreadPoolExecutor(8) as ex:
        allruns = runs(args.days)
        jobs = [j for r in ex.map(failed_jobs, allruns) for j in r]
        jobs = list(ex.map(what_failed, jobs))
    print(f"{len(allruns)} runs in {args.days} days, {sum(1 for r in allruns if r[4]=='failure')} ended failed, "
          f"{sum(1 for r in allruns if int(r[3])>1)} were re-run, {len(jobs)} failed job attempts")
    count = collections.defaultdict(lambda: [0, 0])
    for j in jobs:
        for w in j["what"]:
            count[(j["name"], w)][0 if j["retried"] else 1] += 1
    print(f"{'retried':>7} {'other':>5}  job: what failed")
    for (job, what), (r, o) in sorted(count.items(), key=lambda kv: -(kv[1][0] * 10 + kv[1][1])):
        print(f"{r:7d} {o:5d}  {job}: {what}")
    if args.json:
        json.dump(jobs, open(args.json, "w"), indent=1)


if __name__ == "__main__":
    main()
