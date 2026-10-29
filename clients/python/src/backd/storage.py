"""Where the client keeps the session token."""

from __future__ import annotations

import os
import threading
from pathlib import Path
from typing import Protocol, runtime_checkable

__all__ = ["FileStorage", "MemoryStorage", "SlotStorage", "TokenStorage"]


class TokenStorage(Protocol):
    """Keeps the session token between calls (and, for a file, between runs).

    ``get`` returns ``None`` (or an empty string) when signed out.
    """

    def get(self) -> str | None: ...
    def set(self, token: str) -> None: ...
    def remove(self) -> None: ...


@runtime_checkable
class SlotStorage(TokenStorage, Protocol):
    """A storage that can keep a token per organization, which ``Client.org`` needs."""

    def slot(self, name: str) -> TokenStorage: ...
    def slots(self) -> list[str]: ...


class MemoryStorage:
    """Keeps the token in memory; the default. Safe for concurrent use."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._token: str | None = None
        self._slots: dict[str, MemoryStorage] = {}

    def get(self) -> str | None:
        with self._lock:
            return self._token

    def set(self, token: str) -> None:
        with self._lock:
            self._token = token

    def remove(self) -> None:
        with self._lock:
            self._token = None

    def slot(self, name: str) -> MemoryStorage:
        with self._lock:
            return self._slots.setdefault(name, MemoryStorage())

    def slots(self) -> list[str]:
        with self._lock:
            return sorted(n for n, s in self._slots.items() if s.get())


class FileStorage:
    """Keeps the token in a file (mode 0600), so a script or a tool stays signed in between runs.

    Slots are files next to it, named ``<path>.<organization>``.
    """

    def __init__(self, path: str | os.PathLike[str]) -> None:
        self._path = Path(path)
        self._lock = threading.Lock()

    def get(self) -> str | None:
        with self._lock:
            try:
                return self._path.read_text().strip()
            except FileNotFoundError:
                return None

    def set(self, token: str) -> None:
        with self._lock:
            self._path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
            tmp = self._path.with_name(self._path.name + ".tmp")
            fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
            with os.fdopen(fd, "w") as f:
                f.write(token)
            os.replace(tmp, self._path)

    def remove(self) -> None:
        with self._lock:
            self._path.unlink(missing_ok=True)

    def slot(self, name: str) -> FileStorage:
        return FileStorage(self._path.with_name(f"{self._path.name}.{name}"))

    def slots(self) -> list[str]:
        prefix = self._path.name + "."
        names = [p.name[len(prefix) :] for p in self._path.parent.glob(prefix + "*")]
        return sorted(n for n in names if n and not n.endswith(".tmp"))
