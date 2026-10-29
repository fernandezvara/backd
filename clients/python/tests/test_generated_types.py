"""What ``backd types --lang python`` writes works with the client: the typed realm and the field constants. The
file under test is the Shelf example's (``make shelf-types-update`` writes it), and ``mypy --strict`` checks it
against the client along with this one."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest
from shelf_types.types import (
    AsyncShelfRealm,
    MainAssets,
    MainAssetsFields,
    MainAssetsInput,
    MainPinsAggregate,
    ShelfConfigPath,
    ShelfRealm,
)

import backd

from .helpers import Server, go, make_client

INFO = json.loads((Path(__file__).parents[1] / "examples" / "shelf" / "shelf_types" / "realm.json").read_text())


def asset(id: str, title: str) -> dict[str, Any]:
    return {"id": id, "_meta": {"created_at": "t", "updated_at": "t", "version": 1}, "kind": "note", "title": title}


async def test_the_typed_realm_hands_out_typed_collections(mode: str) -> None:
    server = Server()
    server.on("GET /main/assets", {"items": [asset("a1", "First")], "limit": 20, "skip": 0, "has_more": False})
    server.on("POST /main/assets", asset("a2", "Second"), 201)
    client = make_client(mode, server, types=INFO["fingerprint"])
    realm = AsyncShelfRealm(client) if mode == "async" else ShelfRealm(client)

    page = await go(realm.main.assets.list(where={"kind": "note"}, order_by=MainAssetsFields.META_CREATED_AT_DESC))
    first: MainAssets = page.items[0]  # the checker knows the document
    assert first["title"] == "First" and first["id"] == "a1"
    assert server.last().url.params["order_by"] == "-_meta.created_at"
    assert server.last().headers["x-backd-types"] == INFO["fingerprint"]  # the drift check the stale-types header makes

    new: MainAssetsInput = {"kind": "note", "title": "Second"}
    created = await go(realm.main.assets.create(new))
    assert created["title"] == "Second"
    assert server.body() == {"kind": "note", "title": "Second"}


async def test_functions_and_the_constants(mode: str) -> None:
    server = Server()
    server.on("POST /main/_func/pin", {"id": "p1", "limit": 10, "pins": 3})
    server.on("GET /_config", {"values": {"features": {"export": {"enabled": True}}}, "version": "1", "cache_ttl": 60})
    client = make_client(mode, server)
    realm = AsyncShelfRealm(client) if mode == "async" else ShelfRealm(client)
    out = await go(realm.main.pin({"org": "o1", "title": "Docs", "url": "https://example.com"}))
    assert out["pins"] == 3
    assert server.body() == {"org": "o1", "title": "Docs", "url": "https://example.com"}
    config = await go(realm.load_config())
    assert config["features"]["export"]["enabled"] is True
    assert ShelfConfigPath.FEATURES_EXPORT_ENABLED == "features.export.enabled"
    assert MainAssetsFields.TITLE == "title" and MainAssetsFields.TITLE_DESC == "-title"
    assert isinstance(MainPinsAggregate.GROUP_BY, tuple)


def test_the_fingerprint_is_the_one_every_client_sends() -> None:
    assert len(INFO["fingerprint"]) == 64 and INFO["realm"] == "shelf"
    with pytest.raises(AttributeError):
        _ = backd.MissingName  # type: ignore[attr-defined]
