#!/usr/bin/env python3
"""Checks the {{< hint >}} callouts in docs/content against the docs conventions
(the project instructions, "Callouts"):

  * the style is one of note, tip, warning, danger (info and success are legacy);
  * a custom title is given with named parameters, style="..." title="...";
  * at most MAX_PER_PAGE callouts per page, none directly after another;
  * a body of at most MAX_LINES lines and MAX_CHARS characters.

    scripts/check-docs-callouts.py [docs/content]
"""
import os
import re
import sys

ROOT = sys.argv[1] if len(sys.argv) > 1 else os.path.join(os.path.dirname(__file__), "..", "docs", "content")
STYLES = {"note", "tip", "warning", "danger"}
LEGACY = {"info", "success"}
MAX_PER_PAGE, MAX_LINES, MAX_CHARS = 4, 6, 800
pattern = re.compile(r"\{\{<\s*hint\b([^>]*?)>\}\}(.*?)\{\{<\s*/hint\s*>\}\}", re.S)

problems = []
total = 0
for dirpath, _, files in os.walk(ROOT):
    for f in sorted(files):
        if not f.endswith(".md"):
            continue
        path = os.path.join(dirpath, f)
        text = open(path, encoding="utf-8").read()
        blocks = list(pattern.finditer(text))
        rel = os.path.relpath(path, ROOT)
        total += len(blocks)
        if len(blocks) > MAX_PER_PAGE:
            problems.append(f"{rel}: {len(blocks)} callouts (at most {MAX_PER_PAGE} per page)")
        prev_end = None
        for m in blocks:
            args, body = m.group(1).strip(), m.group(2).strip()
            line = text[: m.start()].count("\n") + 1
            named = re.match(r"style\s*=\s*\"([^\"]+)\"", args)
            if "=" in args and not named:
                problems.append(f"{rel}:{line}: named parameters must start with style=\"...\"")
                continue
            style = named.group(1) if named else (args.split() or ["info"])[0]
            if style in LEGACY:
                problems.append(f"{rel}:{line}: '{style}' is legacy; use note or tip")
            elif style not in STYLES:
                problems.append(f"{rel}:{line}: unknown callout style '{style}'")
            if not named and args.split()[1:]:
                problems.append(f"{rel}:{line}: a title needs named parameters: style=\"tip\" title=\"Best practice\"")
            lines = [l for l in body.splitlines() if l.strip()]
            if len(lines) > MAX_LINES or len(body) > MAX_CHARS:
                problems.append(f"{rel}:{line}: callout too long ({len(lines)} lines, {len(body)} characters): keep it to one idea and link to the detail")
            if prev_end is not None and text[prev_end : m.start()].strip() == "":
                problems.append(f"{rel}:{line}: callouts stacked directly after another")
            prev_end = m.end()

for p in problems:
    print(p)
print(f"{total} callouts checked, {len(problems)} problems")
sys.exit(1 if problems else 0)
