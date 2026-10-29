import io
from pathlib import Path

import pytest

from backd import Upload, part_size_for

MIB = 1024 * 1024


def test_bytes_text_and_names() -> None:
    up = Upload(b"hello", name="a.txt")
    assert (up.size, up.name, up.type, up.replayable) == (5, "a.txt", "text/plain", True)
    assert up.read() == b"hello" and up.read() == b"hello"
    assert Upload("héllo").read() == "héllo".encode()
    assert Upload(b"x").name == "file" and Upload(b"x").type == "application/octet-stream"
    assert Upload(b"x", name="p.png", type="image/x-custom").type == "image/x-custom"
    assert Upload(bytearray(b"ab")).read() == b"ab" and Upload(memoryview(b"ab")).size == 2


def test_ranges_and_hashes() -> None:
    up = Upload(bytes(range(256)) * 5000)  # more than one chunk
    assert up.read(10, 5) == bytes(range(10, 15))
    assert len(up.read(1_000_000)) == up.size - 1_000_000  # type: ignore[operator]
    import hashlib

    assert up.sha256() == hashlib.sha256(up.read()).hexdigest()
    assert up.sha256(3, 4) == hashlib.sha256(bytes([3, 4, 5, 6])).hexdigest()
    part = up.view(100, 50)
    assert part.size == 50 and part.read() == up.read(100, 50)
    assert part.view(10, 1000).size == 40  # a view of a view stays inside its parent


def test_a_path_is_opened_each_time(tmp_path: Path) -> None:
    f = tmp_path / "report.pdf"
    f.write_bytes(b"0123456789")
    up = Upload(f)
    assert (up.size, up.name, up.type, up.replayable) == (10, "report.pdf", "application/pdf", True)
    assert up.read() == up.read() == b"0123456789"
    assert up.read(2, 3) == b"234"
    assert Upload(str(f)).read() == str(f).encode()  # a str is text, not a path


def test_a_file_object_is_read_from_where_it_is() -> None:
    f = io.BytesIO(b"abcdefghij")
    f.seek(3)
    up = Upload(f)
    assert up.size == 7 and up.replayable
    assert up.read() == b"defghij" and up.read(1, 2) == b"ef"


class Stream(io.RawIOBase):
    """A pipe: no size, no seeking."""

    def __init__(self, data: bytes) -> None:
        self._b = io.BytesIO(data)

    def readable(self) -> bool:
        return True

    def readinto(self, b: bytearray) -> int:  # type: ignore[override]
        return self._b.readinto(b)


def test_a_stream_can_be_read_once() -> None:
    up = Upload(Stream(b"stream"))  # type: ignore[arg-type]
    assert up.size is None and not up.replayable
    assert up.read() == b"stream"
    with pytest.raises(ValueError, match="can't be read again"):
        up.read()


def test_part_sizes() -> None:
    assert part_size_for(1) == 64 * MIB
    assert part_size_for(64 * MIB * 1000) == 64 * MIB
    assert part_size_for(64 * MIB * 1000 + 1) == 80 * MIB  # a multiple of 16 MiB
    assert part_size_for(5 * 1000**4) % (16 * MIB) == 0
