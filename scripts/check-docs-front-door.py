#!/usr/bin/env python3
"""Checks that keep the front door of the documentation from drifting (the first things a newcomer reads).

  1. The README's quick start equals the Quickstart's: the code block between
     <!-- quickstart:start --> and <!-- quickstart:end --> is the same in README.md and in
     docs/content/docs/build/quickstart.md.
  2. A statement that something is planned or not built says where: every line of the docs and the
     README that says "Planned", "is planned", "planned for", "coming soon", "not yet built" or
     "design only" links to an issue (github.com/fernandezvara/backd/issues/N).
  3. The client lists name the same clients: README.md, the documentation home, getting-started.md
     and build/clients/_index.md each mention all four (JavaScript, Dart, Go, Python).

    scripts/check-docs-front-door.py [repository root]
"""
import os
import re
import sys

ROOT = sys.argv[1] if len(sys.argv) > 1 else os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")
DOCS = os.path.join("docs", "content")

BLOCK = re.compile(r"<!-- quickstart:start -->\s*```[a-z]*\n(.*?)```\s*<!-- quickstart:end -->", re.S)
# Status wording, not any use of the word: "Not planned" is a decision, "a planned rotation" is a plan of the reader's.
PLAN = re.compile(
    r"(?<![Nn]ot )\bPlanned\b(?! rotation)|\b(?:is|stays|remains) planned\b(?! (?:during|in|at|by)\b)|\bplanned for\b"
    r"|\b[Cc]oming soon\b|\b[Nn]ot yet (?:built|implemented)\b|\b[Dd]esign only\b|\b[Dd]esigned, not built\b"
)
ISSUE = re.compile(r"github\.com/fernandezvara/backd/issues/\d+")
# The four clients, each by the name it goes by in the lists.
CLIENTS = {
    "JavaScript": re.compile(r"backd-js|JavaScript", re.I),
    "Dart": re.compile(r"package:backd|Dart"),
    "Go": re.compile(r"pkg/client|\bGo\b"),
    "Python": re.compile(r"PyPI|Python"),
}
CLIENT_LISTS = ["README.md", f"{DOCS}/docs/_index.md", f"{DOCS}/docs/getting-started.md", f"{DOCS}/docs/build/clients/_index.md"]
QUICKSTART = f"{DOCS}/docs/build/quickstart.md"


def read(rel):
    with open(os.path.join(ROOT, rel), encoding="utf-8") as f:
        return f.read()


def quickstart_block(text):
    m = BLOCK.search(text)
    return m.group(1).strip() if m else None


def check_quickstart_equals_readme():
    out = []
    readme, page = quickstart_block(read("README.md")), quickstart_block(read(QUICKSTART))
    for name, block in (("README.md", readme), (QUICKSTART, page)):
        if block is None:
            out.append(f"{name}: no code block between <!-- quickstart:start --> and <!-- quickstart:end -->")
    if readme is not None and page is not None and readme != page:
        out.append("README.md: the quick start differs from the one in " + QUICKSTART + ": the commands must be the same")
    return out


def prose_lines(text):
    """(line number, line) outside code fences."""
    fenced = False
    for n, line in enumerate(text.splitlines(), 1):
        if line.lstrip().startswith("```"):
            fenced = not fenced
            continue
        if not fenced:
            yield n, line


def check_plans_say_where():
    out = []
    files = ["README.md"]
    for base, _, names in os.walk(os.path.join(ROOT, DOCS)):
        files += [os.path.relpath(os.path.join(base, n), ROOT) for n in names if n.endswith(".md")]
    for rel in sorted(files):
        if rel.endswith(os.path.join("operate", "_index.md")):
            continue  # the upgrade notes tell what each release did, in the past tense
        for n, line in prose_lines(read(rel)):
            if PLAN.search(line) and not ISSUE.search(line):
                out.append(f"{rel}:{n}: says something is planned or not built without linking an issue: {line.strip()[:110]}")
    return out


def check_client_lists():
    out = []
    for rel in CLIENT_LISTS:
        text = read(rel)
        missing = [name for name, rx in CLIENTS.items() if not rx.search(text)]
        if missing:
            out.append(f"{rel}: the clients it lists leave out {', '.join(missing)}")
    return out


CHECKS = [check_quickstart_equals_readme, check_plans_say_where, check_client_lists]


def main():
    problems = [p for check in CHECKS for p in check()]
    for p in problems:
        print(p)
    if problems:
        print(f"{len(problems)} problem(s)")
        sys.exit(1)
    print("the front door checks pass")


if __name__ == "__main__":
    main()
