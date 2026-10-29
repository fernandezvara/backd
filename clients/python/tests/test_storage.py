import stat
from pathlib import Path

import backd
from backd.storage import SlotStorage


def test_memory_storage_and_slots() -> None:
    s = backd.MemoryStorage()
    assert s.get() is None
    s.set("t")
    assert s.get() == "t"
    s.slot("acme").set("a")
    s.slot("empty")
    assert s.slot("acme").get() == "a"
    assert s.slots() == ["acme"]
    s.remove()
    assert s.get() is None
    assert isinstance(s, SlotStorage)


def test_file_storage_round_trips_with_private_mode(tmp_path: Path) -> None:
    path = tmp_path / "state" / "session"
    s = backd.FileStorage(path)
    assert s.get() is None
    s.set("tok\n")
    assert backd.FileStorage(path).get() == "tok"  # a new process reads it
    assert stat.S_IMODE(path.stat().st_mode) == 0o600
    s.slot("acme").set("a")
    s.slot("beta").set("b")
    assert s.slots() == ["acme", "beta"]
    s.slot("acme").remove()
    assert s.slots() == ["beta"]
    s.remove()
    s.remove()  # twice is fine
    assert s.get() is None
