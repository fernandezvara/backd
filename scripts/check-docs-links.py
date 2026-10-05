#!/usr/bin/env python3
"""Checks the built docs site (docs/public): every internal link resolves to a
page, every #anchor to an id on it, and every image to a file.

    hugo --source docs --minify && scripts/check-docs-links.py [docs/public]

Exits 1 and lists the broken links. Links to other sites are not followed."""
import html.parser
import os
import re
import sys
import urllib.parse

ROOT = sys.argv[1] if len(sys.argv) > 1 else os.path.join(os.path.dirname(__file__), "..", "docs", "public")
ROOT = os.path.abspath(ROOT)
BASE = "/backd/"  # baseURL's path in docs/hugo.toml


class Page(html.parser.HTMLParser):
    def __init__(self):
        super().__init__()
        self.ids, self.links, self.images = set(), [], []

    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        if "id" in a:
            self.ids.add(a["id"])
        if tag == "a" and a.get("href"):
            self.links.append(a["href"])
        if tag == "img" and a.get("src"):
            self.images.append(a["src"])


# Files served as downloads (the tutorial's app and config), not documentation:
# their {{.Link}} placeholders and app-absolute paths are not links of this site.
DOWNLOADS = os.path.join(ROOT, "tutorial")

pages = {}
for dirpath, _, files in os.walk(ROOT):
    if dirpath == DOWNLOADS or dirpath.startswith(DOWNLOADS + os.sep):
        continue
    for f in files:
        if f.endswith(".html"):
            path = os.path.join(dirpath, f)
            p = Page()
            p.feed(open(path, encoding="utf-8").read())
            pages[path] = p


def resolve(page_path, href):
    u = urllib.parse.urlsplit(href)
    if u.scheme or u.netloc or href.startswith(("mailto:", "javascript:")):
        return None
    if u.path == "":
        return page_path, u.fragment
    if u.path.startswith("/"):
        if not u.path.startswith(BASE):
            return "outside", u.path
        rel = u.path[len(BASE):]
    else:
        rel = os.path.normpath(os.path.join(os.path.relpath(os.path.dirname(page_path), ROOT), u.path)).lstrip("./")
        if u.path.endswith("/") and not rel.endswith("/"):
            rel += "/"
    target = os.path.join(ROOT, rel)
    if rel == "" or rel.endswith("/") or os.path.isdir(target):
        target = os.path.join(target, "index.html")
    return target, u.fragment


bad = []
for path, page in pages.items():
    for href in page.links:
        r = resolve(path, href)
        if r is None:
            continue
        target, fragment = r
        if target == "outside":
            bad.append((path, href, "outside the site's base path"))
        elif not os.path.exists(target):
            bad.append((path, href, "no such page"))
        elif fragment and target in pages and urllib.parse.unquote(fragment) not in pages[target].ids:
            bad.append((path, href, "no such anchor"))

# Images (the admin UI's screenshots) must exist too.
for path, page in pages.items():
    for src in page.images:
        r = resolve(path, src)
        if r is not None and (r[0] == "outside" or not os.path.isfile(r[0])):
            bad.append((path, src, "no such image"))

for path, href, why in sorted(bad):
    print(f"{os.path.relpath(path, ROOT)}: {href}: {why}")
print(f"{len(pages)} pages checked, {len(bad)} broken links")
sys.exit(1 if bad else 0)
