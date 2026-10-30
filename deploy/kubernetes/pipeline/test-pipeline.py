#!/usr/bin/env python3
"""Checks the example workflow and the Dockerfile keep the order and the flags the design relies on."""
import os
import re
import sys

import yaml

here = os.path.dirname(os.path.abspath(__file__))


def fail(msg):
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


wf = yaml.safe_load(open(os.path.join(here, "release.yml")))
steps = wf["jobs"]["release"]["steps"]
text = [yaml.safe_dump(s) for s in steps]


def index(needle):
    for i, t in enumerate(text):
        if needle in t:
            return i
    fail(f"no step mentions {needle!r}")


guard, build, push, helm, prune = (index("check-no-auth-disabled.sh"), index("docker build"), index("docker push"), index("helm upgrade"), index("registry-prune.sh"))
if not guard < build < push < helm < prune:
    fail("the order must be: guard, build, push, upgrade, prune")

step = lambda needle: text[index(needle)]
if "--atomic" not in step("helm upgrade") or "--wait" not in step("helm upgrade"):
    fail("the upgrade must be --atomic and --wait")
if "image.tag" not in step("helm upgrade") or "image.repository" not in step("helm upgrade"):
    fail("the upgrade must set the image from the build")
if "KEEP_TAGS" not in step("registry-prune.sh"):
    fail("pruning must protect the running release")
if wf["jobs"]["release"].get("environment") != "production":
    fail("the release must run in a protected environment")
if wf.get("concurrency", {}).get("cancel-in-progress") is not False:
    fail("two releases must not run at once, and one must not cancel another half-way")
if wf["permissions"] != {"contents": "read"}:
    fail("the workflow needs read access to the repository and nothing else")
trigger = wf.get("on", wf.get(True))  # YAML reads a bare `on` as True
if "branches" in trigger.get("push", {}):
    fail("a release is a tag, not a branch push")

dockerfile = open(os.path.join(here, "Dockerfile")).read()
if not re.search(r'^FROM ghcr.io/fernandezvara/backd-executor:\$\{BACKD_VERSION\} AS build', dockerfile, re.M):
    fail("functions are bundled with the executor image of the same release")
if not re.search(r'^FROM ghcr.io/fernandezvara/backd:\$\{BACKD_VERSION\}$', dockerfile, re.M):
    fail("the deployable image is the released backd image")
build_stage, final = dockerfile.split("FROM ghcr.io/fernandezvara/backd:")
if "backd config check" not in build_stage or "backd functions build" not in build_stage:
    fail("the build stage bundles the functions and checks the configuration")
if '["/backd", "config", "check"]' not in final:
    fail("the final image checks the configuration again, as it will run")
if "USER root" in final or "deno" in final.lower():
    fail("the final image must not run as root nor carry Deno")

print("test-pipeline: ok")
