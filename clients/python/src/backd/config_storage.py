"""Where the app configuration is kept between runs."""

from __future__ import annotations

import hashlib
import os
import threading
from pathlib import Path
from typing import Protocol

__all__ = ["ConfigStorage", "FileConfigStorage", "MemoryConfigStorage"]


class ConfigStorage(Protocol):
    """A key-value store for ``client.config``'s copies, one value per realm and user."""

    def get(self, key: str) -> str | None: ...
    def set(self, key: str, value: str) -> None: ...
    def remove(self, key: str) -> None: ...
    def all_keys(self) -> list[str]: ...


class MemoryConfigStorage:
    """Keeps the copies in memory."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._items: dict[str, str] = {}

    def get(self, key: str) -> str | None:
        with self._lock:
            return self._items.get(key)

    def set(self, key: str, value: str) -> None:
        with self._lock:
            self._items[key] = value

    def remove(self, key: str) -> None:
        with self._lock:
            self._items.pop(key, None)

    def all_keys(self) -> list[str]:
        with self._lock:
            return list(self._items)


class FileConfigStorage:
    """Keeps the copies in a directory, one file per key (mode 0600), so a script starts with the last
    configuration it saw."""

    def __init__(self, directory: str | os.PathLike[str]) -> None:
        self._dir = Path(directory)
        self._lock = threading.Lock()

    def _file(self, key: str) -> Path:
        # The key holds ":" and the realm's name; its digest makes a safe file name, the key itself is kept inside.
        return self._dir / (hashlib.sha256(key.encode()).hexdigest()[:32] + ".json")

    def get(self, key: str) -> str | None:
        with self._lock:
            try:
                text = self._file(key).read_text()
            except FileNotFoundError:
                return None
        stored_key, _, value = text.partition("\n")
        return value if stored_key == key else None

    def set(self, key: str, value: str) -> None:
        with self._lock:
            self._dir.mkdir(parents=True, exist_ok=True, mode=0o700)
            path = self._file(key)
            tmp = path.with_suffix(".tmp")
            fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
            with os.fdopen(fd, "w") as f:
                f.write(key + "\n" + value)
            os.replace(tmp, path)

    def remove(self, key: str) -> None:
        with self._lock:
            self._file(key).unlink(missing_ok=True)

    def all_keys(self) -> list[str]:
        with self._lock:
            out: list[str] = []
            for p in self._dir.glob("*.json") if self._dir.exists() else []:
                try:
                    out.append(p.read_text().partition("\n")[0])
                except OSError:
                    continue
            return out
