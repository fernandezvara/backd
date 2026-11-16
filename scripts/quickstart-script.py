#!/usr/bin/env python3
"""Turns the Quickstart's commands (the block between <!-- quickstart:start --> and
<!-- quickstart:end --> in docs/content/docs/build/quickstart.md) into a script CI can run
unattended: the three commands that ask for the password get it on standard input, and
`docker compose exec` gets -T (CI has no terminal). Everything else is run as written.

    scripts/quickstart-script.py [repository root]     prints the script

It refuses when the block no longer has the three commands that ask for the password, so a
change to the Quickstart can't silently skip what CI is meant to check.
"""
import os
import re
import sys

ROOT = sys.argv[1] if len(sys.argv) > 1 else os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")
PAGE = os.path.join(ROOT, "docs", "content", "docs", "build", "quickstart.md")
BLOCK = re.compile(r"<!-- quickstart:start -->\s*```[a-z]*\n(.*?)```\s*<!-- quickstart:end -->", re.S)
PIPE = "printf '%s\\n' \"$QUICKSTART_PASSWORD\" | "


def commands(block):
    """The block's commands, a line continued with a backslash joined to the next."""
    out, current = [], ""
    for line in block.splitlines():
        if line.endswith("\\"):
            current += line[:-1] + " "
            continue
        out.append(current + line)
        current = ""
    return [c for c in out if c.strip()]


def script(text):
    m = BLOCK.search(text)
    if not m:
        raise SystemExit("quickstart.md: no code block between <!-- quickstart:start --> and <!-- quickstart:end -->")
    lines, asked = [], 0
    for cmd in commands(m.group(1)):
        if "backd-server bootstrap" in cmd:
            cmd = PIPE + cmd.replace("compose exec backd", "compose exec -T backd")
            asked += 1
        elif re.search(r"(^|\s)backd login\b", cmd):
            cmd = PIPE + cmd
            asked += 1
        elif "backd apikey create" in cmd:
            cmd = cmd.replace("$(backd apikey create", "$(" + PIPE + "backd apikey create")
            asked += 1
        lines.append(cmd)
    if asked != 3:
        raise SystemExit(f"quickstart.md: expected 3 commands that ask for the password (bootstrap, login, apikey create), found {asked}")
    return "\n".join(lines) + "\n"


if __name__ == "__main__":
    with open(PAGE, encoding="utf-8") as f:
        sys.stdout.write(script(f.read()))
