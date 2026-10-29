"""The sync client is generated from the async one; both are checked in."""

import importlib.util
from pathlib import Path

SCRIPT = Path(__file__).parents[1] / "scripts" / "unasync.py"


def test_sync_files_are_current() -> None:
    spec = importlib.util.spec_from_file_location("unasync", SCRIPT)
    assert spec is not None and spec.loader is not None
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    assert mod.main(["--check"]) == 0, "run: python scripts/unasync.py"
