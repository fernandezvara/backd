#!/usr/bin/env python3
"""Lists what failed in CI lately and what failed at random.

  scripts/ci-failures.py              the last 15 days
  scripts/ci-failures.py --days 30
  scripts/ci-failures.py --json out.json
  scripts/ci-failures.py --days 3 --report     open or update one issue per flaky test (the daily workflow)
  scripts/ci-failures.py --days 3 --report --dry-run

Uses the authenticated `gh`. For every workflow run of the window it reads the attempts that
failed (a run that was re-run keeps its first attempt), then the log of each failed job, and
groups the failures by job and by what failed in it: a Go test, a Playwright test, a pytest
test, a check of the production reference, or an infrastructure message (a package mirror,
the Go module proxy, an image registry). "retried" means the attempt failed and a later
attempt of the same run passed (a run that ended failed is never "retried"): the failure had no cause in the commit, so it is a flake or
an infrastructure failure. Failures that were not retried are real or still unexplained; read
them before you call them flaky.
"""
import argparse, collections, concurrent.futures as cf, datetime, json, re, subprocess

REPO = "fernandezvara/backd"

# What to look for in the log of a failed job: (regular expression, label prefix).
TESTS = (
    (r"--- FAIL: (\S+) \(", "go test"),
    (r"FAIL (backd refuses[^\n]{0,60}|the refusal[^\n]{0,60}|[a-z][^\n]{0,70})", "production reference"),
    (r"✘\s+\d+\s+(e2e/[a-z-]+\.spec\.ts:\d+:\d+ › [^\n]*)", "playwright"),
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
                        "job": jid, "name": jname, "retried": a < attempt and conclusion == "success"})
    return res


WHOLE_SUITE = 6  # this many failing tests in one attempt is one cause (the stack, an install), not that many flaky tests


TIMING = re.compile(r"(\s*\(retry #\d+\))?\s*\([0-9.]+m?s\)?\s*$")


def clean(label, text):
    """The name of a failing test without its timing: the same test must have one name."""
    return label + ": " + TIMING.sub("", text.strip())[:100]


def collapse(found):
    """A whole suite failing at once is one failure, not one per test."""
    for label in ("playwright", "pytest", "go test"):
        names = {f for f in found if f.startswith(label + ": ")}
        if len(names) >= WHOLE_SUITE:
            found = (found - names) | {f"{label}: the whole suite failed (the stack or the install, not one test)"}
    return found


def what_failed(j):
    log = gh(f"repos/{REPO}/actions/jobs/{j['job']}/logs")
    found = set()
    for rx, label in TESTS:
        for m in re.finditer(rx, log, re.M):
            found.add(clean(label, m.group(1)))
    for rx, label in INFRA:
        if re.search(rx, log):
            found.add(f"infrastructure: {label}")
    found = collapse(found)
    if "building and starting the stack" in log and not re.search(r"check\(s\) (failed|passed)|\d+ passed", log):
        found.add("production reference: the stack did not come up (build or start)")
    j["what"] = sorted(found) or ["(see the log)"]
    return j


LABEL = "flaky-test"
UNKNOWN = "(see the log)"


def flaky_groups(jobs):
    """The failures that passed on a re-run, grouped by (job, what failed): {key: [job attempts]}."""
    groups = collections.defaultdict(list)
    for j in jobs:
        if not j["retried"]:
            continue
        for w in j["what"]:
            groups[(j["name"], w)].append(j)
    return groups


def title(job, what):
    return f"Flaky: {job}: {what}"[:200]


def run_url(j):
    return f"https://github.com/{REPO}/actions/runs/{j['run']}/attempts/{j['attempt']}"


def lines(items):
    seen, out = set(), []
    for j in items:
        if (j["run"], j["attempt"]) not in seen:
            seen.add((j["run"], j["attempt"]))
            out.append(f"- attempt {j['attempt']} of {j['workflow']} on `{j['branch']}`: {run_url(j)}")
    return out


def report(jobs, issues, dry_run, create, comment, reopen):
    """Opens one issue per flaky failure, or adds the new runs to the open one. `issues` are the
    existing issues with the flaky-test label ({number, title, state, body, comments}). Returns
    the actions taken, as text."""
    done = []
    by_title = {i["title"]: i for i in issues}
    for (job, what), items in sorted(flaky_groups(jobs).items()):
        if what == UNKNOWN:
            what = "failed, cause not recognized"
        t = title(job, what)
        known = by_title.get(t)
        if known:
            text = known.get("body", "") + "".join(c.get("body", "") for c in known.get("comments", []))
            items = [j for j in items if f"/runs/{j['run']}/attempts/{j['attempt']}" not in text]
            if not items:
                continue
            done.append(f"{'reopen and ' if known['state'] == 'CLOSED' else ''}comment on #{known['number']}: {t} (+{len(lines(items))})")
            if not dry_run:
                if known["state"] == "CLOSED":
                    reopen(known["number"])
                comment(known["number"], "Failed again and passed on a re-run:\n\n" + "\n".join(lines(items)))
        else:
            done.append(f"open: {t} ({len(lines(items))} attempt(s))")
            if not dry_run:
                create(t, FLAKY_BODY.format(job=job, what=what, runs="\n".join(lines(items))))
    return done


FLAKY_BODY = """This failed in CI and **passed on a re-run of the same commit**, so the commit did not cause it.

- Job: `{job}`
- What failed: `{what}`

Runs (the failed attempts):

{runs}

Find the cause (read the log of each attempt) and fix it in the test or in the code it exposes. A test that passes only when re-run is a defect, not an annoyance: see `design/ci-flaky-tests.md`. Opened by `scripts/ci-failures.py --report`; it adds new runs here and reopens this issue if it fails again."""


def gh_issue(*args):
    return subprocess.run(["gh", "issue", *args, "--repo", REPO], capture_output=True, text=True, check=True).stdout


def report_with_gh(jobs, dry_run):
    if not dry_run:
        subprocess.run(["gh", "label", "create", LABEL, "--color", "fbca04", "--description", "A test or step that failed and passed on a re-run", "--repo", REPO], capture_output=True, text=True)
    issues = json.loads(gh_issue("list", "--label", LABEL, "--state", "all", "--limit", "300", "--json", "number,title,state,body,comments") or "[]")
    return report(
        jobs, issues, dry_run,
        create=lambda t, b: gh_issue("create", "--title", t, "--body", b, "--label", LABEL),
        comment=lambda n, b: gh_issue("comment", str(n), "--body", b),
        reopen=lambda n: gh_issue("reopen", str(n)),
    )


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--days", type=int, default=15)
    ap.add_argument("--json")
    ap.add_argument("--report", action="store_true", help="open or update an issue per failure that passed on a re-run")
    ap.add_argument("--dry-run", action="store_true", help="with --report: say what would be done")
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
    if args.report:
        actions = report_with_gh(jobs, args.dry_run)
        print("\n".join(["", "report:"] + (actions or ["nothing new to report"])))


if __name__ == "__main__":
    main()
