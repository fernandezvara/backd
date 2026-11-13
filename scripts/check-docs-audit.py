#!/usr/bin/env python3
"""Checks that the audit trail page lists every action the server records, and nothing else:

  * every audit action in the Go code (an Audit... = "..." constant, or a string literal passed
    as the action to Audit or AuditAs) has a row in the "What is recorded" table of
    docs/content/docs/operate/administration/audit.md;
  * every action in that table is still recorded by the code;
  * every Audit... constant is used somewhere besides its declaration.

    scripts/check-docs-audit.py [repository root]
"""
import os
import re
import sys

ROOT = sys.argv[1] if len(sys.argv) > 1 else os.path.join(os.path.dirname(__file__), "..")
PAGE = os.path.join(ROOT, "docs", "content", "docs", "operate", "administration", "audit.md")
# Audit... constants that are not actions.
NOT_ACTIONS = {"AuditCollection"}

const_re = re.compile(r"\b(Audit[A-Za-z]+)\s*=\s*\"([a-z][a-z_.]*)\"")
call_re = re.compile(r"\.Audit\(\s*[\w.()]+,\s*\"([a-z][a-z_.]*)\"|\.AuditAs\(\s*[\w.()]+,\s*[^,()]+,\s*\"([a-z][a-z_.]*)\"")

sources = {}
for dirpath, _, files in os.walk(os.path.join(ROOT, "internal")):
    for f in files:
        if f.endswith(".go") and not f.endswith("_test.go"):
            path = os.path.join(dirpath, f)
            sources[os.path.relpath(path, ROOT)] = open(path, encoding="utf-8").read()

actions = {}  # action -> where it is defined
consts = {}  # constant -> (action, where)
for rel, text in sorted(sources.items()):
    for m in const_re.finditer(text):
        if m.group(1) in NOT_ACTIONS:
            continue
        consts[m.group(1)] = (m.group(2), rel)
        actions.setdefault(m.group(2), rel)
    for m in call_re.finditer(text):
        actions.setdefault(m.group(1) or m.group(2), rel)

problems = []
everything = "\n".join(sources.values())
for name, (action, rel) in sorted(consts.items()):
    if len(re.findall(r"\b" + name + r"\b", everything)) < 2:
        problems.append(f"{rel}: {name} ({action}) is declared but never recorded: write it or remove it")

documented = set()
for line in open(PAGE, encoding="utf-8"):
    if line.startswith("| `"):
        documented.update(re.findall(r"`([a-z][a-z_.]*)`", line.split("|")[1]))

page = os.path.relpath(PAGE, ROOT)
for action, rel in sorted(actions.items()):
    if action not in documented:
        problems.append(f"{page}: `{action}` (recorded in {rel}) has no row in the table")
for action in sorted(documented - set(actions)):
    problems.append(f"{page}: `{action}` is in the table but the code doesn't record it")

for p in problems:
    print(p)
print(f"{len(actions)} audit actions checked, {len(problems)} problems")
sys.exit(1 if problems else 0)
