"""What an upload reads from: bytes, a path or an open binary file, with a size, a name and a type."""

from __future__ import annotations

import copy
import hashlib
import io
import mimetypes
import os
from collections.abc import Callable, Iterator
from pathlib import Path
from typing import BinaryIO

__all__ = ["Progress", "Upload", "UploadSource", "part_size_for"]

#: ``on_progress(sent, total)``: the bytes sent so far, and the bytes in all when known (``None`` otherwise).
Progress = Callable[[int, "int | None"], None]

#: What ``Upload`` takes: bytes, text (sent as UTF-8), a path, or a binary file object.
UploadSource = bytes | bytearray | memoryview | str | os.PathLike[str] | BinaryIO

CHUNK = 256 * 1024
_MIB = 1024 * 1024
MAX_PARTS = 1000


def part_size_for(size: int) -> int:
    """The size of the parts of a file sent in parts: at least 64 MiB, and a multiple of 16 MiB when the file needs
    more to stay within 1000 parts. backd checks it, so it is the same on both sides."""
    per_part = -(-size // MAX_PARTS)  # rounded up
    need = -(-per_part // (16 * _MIB)) * 16 * _MIB
    return max(64 * _MIB, need)


class Upload:
    """A file to send. ``Upload(path)``, ``Upload(b"bytes", name="a.txt")`` or ``Upload(open(path, "rb"))``.

    A string is the **text** to send (UTF-8), not a path: wrap a path in :class:`pathlib.Path`. ``name`` and
    ``type`` default to the file's own (its name, and the type that name suggests).
    """

    def __init__(self, source: UploadSource, *, name: str | None = None, type: str | None = None) -> None:
        self._data: bytes | None = None
        self._path: Path | None = None
        self._file: BinaryIO | None = None
        self._base = 0
        self.size: int | None
        own: str | None = None
        if isinstance(source, str):
            source = source.encode()
        if isinstance(source, bytes | bytearray | memoryview):
            self._data = bytes(source)
            self.size = len(self._data)
        elif isinstance(source, os.PathLike):
            self._path = Path(source)
            own = self._path.name
            self.size = self._path.stat().st_size
        else:
            self._file = source
            raw = getattr(source, "name", None)
            own = os.path.basename(raw) if isinstance(raw, str) else None
            try:
                self._base = source.tell()
                end = source.seek(0, io.SEEK_END)
                source.seek(self._base)
                self.size = end - self._base
            except (OSError, ValueError, AttributeError):
                self.size = None  # a stream: its size is not known
        self.name = name or own or "file"
        self.type = type or mimetypes.guess_type(self.name)[0] or "application/octet-stream"
        self._used = False
        #: A view of a part of another upload (see :meth:`view`): where it starts and how long it is.
        self._offset = 0
        self._limit: int | None = None

    @property
    def replayable(self) -> bool:
        """Whether the bytes can be read again (a retry, a hash before the send)."""
        if self._file is None:
            return True
        return self.size is not None and self._file.seekable()

    def view(self, start: int, length: int) -> Upload:
        """The bytes ``start`` to ``start + length`` as an upload of their own: a part of a file sent in parts."""
        part = copy.copy(self)
        part._offset = self._offset + start
        part._limit = length if self._limit is None else min(length, self._limit - start)
        part.size = part._limit
        return part

    def chunks(self, start: int = 0, length: int | None = None) -> Iterator[bytes]:
        """The bytes from ``start`` for ``length`` (to the end when omitted), in pieces."""
        if self._limit is not None:
            length = self._limit - start if length is None else min(length, self._limit - start)
        start += self._offset
        if self._data is not None:
            end = len(self._data) if length is None else min(len(self._data), start + length)
            for at in range(start, end, CHUNK):
                yield self._data[at : min(at + CHUNK, end)]
            return
        if self._path is not None:
            with self._path.open("rb") as f:
                f.seek(start)
                yield from _read(f, length)
            return
        assert self._file is not None
        if self.replayable:
            self._file.seek(self._base + start)
        elif self._used or start:
            raise ValueError(
                "this upload reads from a stream that can't be read again: give a path, bytes or a seekable file"
            )
        self._used = True
        yield from _read(self._file, length)

    def sha256(self, start: int = 0, length: int | None = None) -> str:
        """The SHA-256 (hex) of the bytes ``chunks(start, length)`` gives."""
        h = hashlib.sha256()
        for chunk in self.chunks(start, length):
            h.update(chunk)
        return h.hexdigest()

    def read(self, start: int = 0, length: int | None = None) -> bytes:
        """The bytes ``chunks(start, length)`` gives, in one piece."""
        return b"".join(self.chunks(start, length))


def _read(f: BinaryIO, length: int | None) -> Iterator[bytes]:
    left = length
    while left is None or left > 0:
        chunk = f.read(CHUNK if left is None else min(CHUNK, left))
        if not chunk:
            return
        if left is not None:
            left -= len(chunk)
        yield chunk
