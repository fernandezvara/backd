"""Shared helpers for the integration tests (run with ``make python-integration``: they expect the realms and keys
that ``tests/integration/run.sh`` prepares). Without ``BACKD_URL`` set they are skipped."""

from __future__ import annotations

import inspect
import itertools
import os
import time
from collections.abc import AsyncIterator
from typing import Any

import pytest

import backd

URL = os.environ.get("BACKD_URL", "")
API_KEY = os.environ.get("BACKD_API_KEY", "")
FILES_API_KEY = os.environ.get("BACKD_FILES_API_KEY", "")
PASSWORD = "dev-p4ssw0rd!"

_RUN = format(int(time.time() * 1000), "x")
_COUNTER = itertools.count()


def pytest_collection_modifyitems(config: pytest.Config, items: list[pytest.Item]) -> None:
    if URL:
        return
    skip = pytest.mark.skip(reason="BACKD_URL not set; run `make python-integration`")
    for item in items:
        if "integration" in str(item.fspath):
            item.add_marker(skip)


def email(name: str) -> str:
    """A unique email for this run."""
    return f"{name}.{_RUN}.{next(_COUNTER)}@example.com"


async def go(value: Any) -> Any:
    """The result of a call of either client."""
    return await value if inspect.isawaitable(value) else value


def new_client(mode: str, realm: str, **kw: Any) -> Any:
    """A client of the realm: sync or async, sessions in memory."""
    cls = backd.AsyncClient if mode == "async" else backd.Client
    return cls(url=URL, realm=realm, **kw)


async def close(client: Any) -> None:
    await go(client.aclose() if isinstance(client, backd.AsyncClient) else client.close())


@pytest.fixture(params=["sync", "async"])
def mode(request: pytest.FixtureRequest) -> str:
    return str(request.param)


class Users:
    """Signs people up and closes their clients when the test ends."""

    def __init__(self, mode: str) -> None:
        self.mode = mode
        self.clients: list[Any] = []

    async def sign_up(self, name: str, realm: str = "itest") -> tuple[Any, str]:
        client = new_client(self.mode, realm)
        self.clients.append(client)
        address = email(name)
        await go(client.auth.signup(address, PASSWORD))
        return client, address

    def anonymous(self, realm: str = "itest", **kw: Any) -> Any:
        client = new_client(self.mode, realm, **kw)
        self.clients.append(client)
        return client


@pytest.fixture
async def users(mode: str) -> AsyncIterator[Users]:
    u = Users(mode)
    yield u
    for client in u.clients:
        await close(client)
