#!/usr/bin/env bash
# Checks the Python client the way a user gets it from PyPI: builds the sdist and the wheel, validates their metadata,
# checks what the wheel holds, installs it into a fresh virtualenv (from the wheel alone, with httpx from PyPI) and
# uses it from a script — sync and async, the errors, the sizes and the typed marker — like the README's quickstart.
# Needs Python 3.10 or later, and network access to PyPI for httpx and the build tools.
#
#   scripts/check-python-package.sh        # or: make python-package
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root/clients/python"
python=${PYTHON:-python3}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# The build tools live in a virtualenv of their own: the package is built from a clean copy of the sources.
"$python" -m venv "$work/tools"
"$work/tools/bin/pip" install -q build twine
mkdir "$work/src"
tar -C "$root/clients/python" --exclude=.venv --exclude=dist --exclude='__pycache__' --exclude='.*_cache' -cf - . | tar -C "$work/src" -xf -
(cd "$work/src" && "$work/tools/bin/python" -m build --outdir "$work/dist" >/dev/null)
"$work/tools/bin/twine" check "$work"/dist/*

wheel=$(ls "$work"/dist/*.whl)
sdist=$(ls "$work"/dist/*.tar.gz)
version=$("$python" - <<'PY'
import re, pathlib
print(re.search(r'__version__ = "([^"]+)"', pathlib.Path("src/backd/__init__.py").read_text()).group(1))
PY
)
echo "package: backd $version"

# What the wheel holds: the library, the generated sync half, the typed marker and the licence; no tests or examples.
"$work/tools/bin/python" - "$wheel" "$sdist" "$version" <<'PY'
import sys, tarfile, zipfile

wheel, sdist, version = sys.argv[1:4]
names = zipfile.ZipFile(wheel).namelist()
for want in ("backd/__init__.py", "backd/py.typed", "backd/errors.py", "backd/_sync/client.py", "backd/_async/client.py",
             "backd/_sync/realtime.py", "backd/_live.py", "backd/size.py"):
    if want not in names:
        sys.exit(f"the wheel lacks {want}")
if not any(n.endswith("/licenses/LICENSE") or n.endswith("LICENSE") for n in names):
    sys.exit("the wheel carries no LICENSE")
extra = [n for n in names if n.startswith(("tests/", "examples/", "scripts/")) or "__pycache__" in n]
if extra:
    sys.exit(f"the wheel holds files it shouldn't: {extra}")
meta = next(n for n in names if n.endswith("METADATA"))
text = zipfile.ZipFile(wheel).read(meta).decode()
if f"Version: {version}" not in text or "Requires-Dist: httpx" not in text or "Requires-Python: >=3.10" not in text:
    sys.exit("the wheel's metadata is not what pyproject.toml says:\n" + text[:600])
members = tarfile.open(sdist).getnames()
for want in ("pyproject.toml", "README.md", "src/backd/__init__.py", "src/backd/py.typed"):
    if not any(m.endswith("/" + want) for m in members):
        sys.exit(f"the sdist lacks {want}")
print(f"wheel: {len(names)} files, sdist: {len(members)} files")
PY

# A fresh virtualenv that installs it the way PyPI users will, and uses it.
"$python" -m venv "$work/app"
"$work/app/bin/pip" install -q "$wheel"
cat > "$work/use.py" <<'PY'
"""The package as a user sees it: both clients with a fake transport, the error hierarchy, the sizes."""
import asyncio

import httpx

import backd


def handler(request: httpx.Request) -> httpx.Response:
    if "/missing/" in request.url.path:
        return httpx.Response(404, json={"error": {"code": "not_found", "message": "nope"}})
    return httpx.Response(200, json={"items": [{"id": "1", "_meta": {"version": 1}}], "limit": 20, "skip": 0, "has_more": False})


transport = httpx.MockTransport(handler)

with backd.Client(url="https://api.example.com", realm="blog", transport=transport) as client:
    page = client.db("main").collection("posts").list(limit=1)
    assert [d["id"] for d in page] == ["1"], page
    try:
        client.db("main").collection("missing").get("x")
    except backd.NotFoundError as e:
        assert (e.status, e.code) == (404, "not_found")
    else:
        raise SystemExit("a 404 must raise NotFoundError")


async def main() -> None:
    async with backd.AsyncClient(url="https://api.example.com", realm="blog", transport=httpx.MockTransport(handler)) as c:
        page = await c.db("main").collection("posts").list()
        assert len(page) == 1
        try:
            await c.db("main").collection("missing").get("x")
        except backd.BackdError as e:
            assert isinstance(e, backd.NotFoundError)
        else:
            raise SystemExit("a 404 must raise NotFoundError")


asyncio.run(main())
assert backd.parse_size("1.5MiB") == 1572864 and backd.format_bytes(1536) == "1.5 KiB"
import importlib.resources
assert importlib.resources.files("backd").joinpath("py.typed").is_file(), "the package must ship its py.typed marker"
print("backd", backd.__version__, "works from the wheel")
PY
(cd "$work" && "$work/app/bin/python" use.py)
