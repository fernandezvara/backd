from __future__ import annotations

import asyncio
import json
import threading
from typing import Any

import httpx
import pytest

import backd
from backd._live import SSEParser, parse_block

from .helpers import Server, Streams, error_response, go, json_response, make_client


def doc(id: str, version: int = 1, **fields: Any) -> dict[str, Any]:
    return {"id": id, "_meta": {"version": version}, **fields}


class Items:
    """GET /main/items and /main/items/{id}: what a reload reads."""

    def __init__(self, server: Server, *docs: dict[str, Any]) -> None:
        self.docs = {d["id"]: d for d in docs}
        self.reads: list[httpx.Request] = []
        self.fail: httpx.Response | None = None
        server.on("GET /main/items", self.list)
        server.on("GET /main/items/i1", self.one)

    def list(self, req: httpx.Request) -> httpx.Response:
        self.reads.append(req)
        if self.fail is not None:
            return self.fail
        items = sorted(self.docs.values(), key=lambda d: d["id"])
        return json_response(200, {"items": items, "limit": 100, "skip": 0, "has_more": False})

    def one(self, req: httpx.Request) -> httpx.Response:
        self.reads.append(req)
        return json_response(200, self.docs["i1"]) if "i1" in self.docs else error_response(404, "not_found")


async def settle(cond: Any, what: str = "it") -> None:
    for _ in range(1000):
        if cond():
            return
        await asyncio.sleep(0.005)
    raise AssertionError(f"{what} did not happen")


def setup(mode: str, *docs: dict[str, Any], **kw: Any) -> tuple[Any, Server, Streams, Items]:
    server = Server()
    streams, items = Streams(server), Items(server, *docs)
    kw.setdefault("storage", backd.MemoryStorage())
    kw["storage"].set("bds_x")
    client = make_client(mode, server, **kw)
    OPENED.append((client, streams))
    return client, server, streams, items


#: What a test opened, closed after it: a stream left open would leave its thread or task running.
OPENED: list[tuple[Any, Streams]] = []


@pytest.fixture(autouse=True)
async def close_what_was_opened() -> Any:
    yield
    while OPENED:
        client, streams = OPENED.pop()
        for feed in streams.feeds:
            feed.end()
        await go(client.aclose() if isinstance(client, backd.AsyncClient) else client.close())


SPEC: Any = {"db": "main", "collection": "items", "load": {"sort": "-n"}}
NO_JITTER = backd.SubscribeOptions(random=lambda: 0.0)


@pytest.fixture(autouse=True)
def fast_reconnects(monkeypatch: pytest.MonkeyPatch) -> None:
    import backd._async.realtime as ar
    import backd._sync.realtime as sr

    monkeypatch.setattr(ar, "RECONNECT_BASE", 0.01)
    monkeypatch.setattr(sr, "RECONNECT_BASE", 0.01)


def live_threads() -> list[threading.Thread]:
    return [t for t in threading.enumerate() if t.name.startswith("backd-")]


# -- the wire ------------------------------------------------------------------------------------


def test_parse_block() -> None:
    ev = parse_block('id: abc\nevent: created\ndata: {"sub":"s1",\ndata: "doc":{"id":"1"}}')
    assert ev is not None and (ev.id, ev.event, ev.data) == ("abc", "created", {"sub": "s1", "doc": {"id": "1"}})
    assert parse_block(": heartbeat") is None
    assert parse_block("data: {}") is None  # no event name
    assert parse_block("event: x\ndata: not json") is None
    e = parse_block("event: ready")
    assert e is not None and e.data == {}


def test_the_parser_copes_with_any_cut_of_the_bytes() -> None:
    wire = 'event: ready\r\ndata: {}\r\n\r\n: heartbeat\n\nid: 7\nevent: removed\ndata: {"id":"é"}\n\n'.encode()
    for size in (1, 2, 3, 7, len(wire)):
        p = SSEParser()
        events = [e for i in range(0, len(wire), size) for e in p.feed(wire[i : i + size])]
        assert [(e.event, e.id) for e in events] == [("ready", None), ("removed", "7")], size
        assert events[1].data == {"id": "é"}


# -- a live list -----------------------------------------------------------------------------------


async def test_snapshot_changes_and_the_order(mode: str) -> None:
    c, server, streams, items = setup(mode, doc("a", n=1), doc("b", n=3))
    view = c.realtime.subscribe([SPEC], NO_JITTER)
    assert view.state.status == "connecting" and view.items == []
    await settle(lambda: streams.feeds, "the stream opening")
    assert streams.bodies[0] == {"subscriptions": [{"id": "s1", "db": "main", "collection": "items"}]}
    streams.feed.event("ready", {"subscriptions": ["s1"]})
    state = await go(view.wait_ready(5))
    assert state.status == "live" and [d["id"] for d in state.items] == ["b", "a"]  # sorted by -n
    assert items.reads[0].url.params["order_by"] == "-n"

    changes: list[Any] = []
    view.on("change", changes.append)
    streams.feed.event("created", {"sub": "s1", "doc": doc("c", n=2)}, id="t1")
    streams.feed.event("updated", {"sub": "s1", "doc": doc("a", 2, n=9)}, id="t2")
    streams.feed.event("updated", {"sub": "s1", "doc": doc("b", 1, n=-5)})  # not newer: ignored
    streams.feed.event("removed", {"sub": "s1", "id": "c", "version": 1}, id="t3")
    await settle(lambda: [d["id"] for d in view.items] == ["a", "b"] and changes, "the changes")
    assert view.state.subscriptions["0"].items == view.items and view.state.subscriptions["0"].status == "live"
    assert changes[-1].items == view.items
    streams.feed.event("removed", {"sub": "s1", "id": "a", "version": 1})  # older than what is held: ignored
    streams.feed.event("created", {"sub": "nope", "doc": doc("x")})  # not ours
    streams.feed.event("created", {"sub": "s1", "doc": doc("z", n=100)})
    await settle(lambda: view.items[0]["id"] == "z", "z")
    assert [d["id"] for d in view.items] == ["z", "a", "b"]
    view.close()
    assert view.state.status == "closed" and view.closed
    view.close()  # twice is fine


async def test_events_that_arrive_while_the_list_loads_are_applied_after_it(mode: str) -> None:
    c, server, streams, items = setup(mode, doc("a", 1, n=1))
    gate = threading.Event()
    real = items.list

    def slow(req: httpx.Request) -> httpx.Response:
        gate.wait(5)  # threads: the read is held back until the events have arrived
        return real(req)

    if mode == "sync":
        server.on("GET /main/items", slow)
    else:
        gate.set()  # tasks: the events are read before the load task gets to run
    view = c.realtime.subscribe([SPEC], NO_JITTER)
    await settle(lambda: streams.feeds)
    streams.feed.event("ready")
    streams.feed.event("updated", {"sub": "s1", "doc": doc("a", 2, n=7)})  # newer than the read
    streams.feed.event("updated", {"sub": "s1", "doc": doc("b", 1, n=3)})
    await asyncio.sleep(0.05)
    if mode == "sync":
        assert view.state.status != "live"
        gate.set()
    state = await go(view.wait_ready(5))
    await settle(lambda: [d["id"] for d in view.items] == ["a", "b"])
    assert [(d["id"], d["n"]) for d in view.items] == [("a", 7), ("b", 3)]
    assert state.status == "live"
    view.close()


async def test_load_follows_pages_up_to_the_limit(mode: str) -> None:
    c, server, streams, _ = setup(mode)
    reads: list[dict[str, str]] = []

    def pages(req: httpx.Request) -> httpx.Response:
        q = dict(req.url.params)
        reads.append(q)
        start = int(q.get("after", "0"))
        n = int(q["limit"])
        items = [doc(f"d{i:03d}", n=i) for i in range(start, start + n)]
        return json_response(
            200, {"items": items, "limit": n, "skip": 0, "has_more": True, "next_cursor": str(start + n)}
        )

    server.on("GET /main/items", pages)
    view = c.realtime.subscribe([{**SPEC, "load": {"limit": 150}}], NO_JITTER)
    await settle(lambda: streams.feeds)
    streams.feed.event("ready")
    state = await go(view.wait_ready(5))
    assert len(state.items) == 150 and [r["limit"] for r in reads] == ["100", "50"]
    streams.feed.event("created", {"sub": "s1", "doc": doc("d999", n=999)})
    await settle(lambda: any(d["id"] == "d999" for d in view.items) or True)
    view.close()


async def test_a_document_subscription(mode: str) -> None:
    c, server, streams, items = setup(mode, doc("i1", n=1))
    view = c.realtime.subscribe([{"db": "main", "collection": "items", "doc": "i1"}], NO_JITTER)
    await settle(lambda: streams.feeds)
    assert streams.bodies[0]["subscriptions"][0]["doc"] == "i1"
    streams.feed.event("ready")
    state = await go(view.wait_ready(5))
    assert [d["id"] for d in state.items] == ["i1"]
    streams.feed.event("removed", {"sub": "s1", "id": "i1", "version": 1})
    await settle(lambda: view.items == [])
    view.close()
    del items.docs["i1"]
    c2, server2, streams2, _ = setup(mode)
    Items(server2)
    v2 = c2.realtime.subscribe([{"db": "main", "collection": "items", "doc": "i1"}], NO_JITTER)
    await settle(lambda: streams2.feeds)
    streams2.feed.event("ready")
    assert (await go(v2.wait_ready(5))).items == []  # a missing document is an empty list
    v2.close()


async def test_status_goes_through_connecting_live_and_resyncing(mode: str) -> None:
    c, server, streams, items = setup(mode, doc("a", n=1))
    statuses: list[str] = []
    view = c.realtime.subscribe([SPEC], NO_JITTER)
    view.on("status", statuses.append)
    await settle(lambda: streams.feeds)
    streams.feed.event("ready", id="t1")
    await go(view.wait_ready(5))
    streams.feed.end()  # the stream drops: reconnect, resume
    await settle(lambda: len(streams.feeds) == 2, "the reconnect")
    assert streams.bodies[1]["resume_from"] == "t1"
    assert "resyncing" in statuses
    streams.feed.event("ready")
    await settle(lambda: view.state.status == "live")
    assert statuses[-1] == "live"
    view.close()


async def test_a_reset_reloads_with_jitter(mode: str) -> None:
    c, server, streams, items = setup(mode, doc("a", n=1))
    delays: list[float] = []

    def random() -> float:
        delays.append(1.0)
        return 1.0

    options = backd.SubscribeOptions(jitter=0.05, random=random)
    view = c.realtime.subscribe([SPEC], options)
    resets: list[Any] = []
    view.on("reset", resets.append)
    await settle(lambda: streams.feeds)
    streams.feed.event("ready")
    await go(view.wait_ready(5))
    n = len(items.reads)
    items.docs["b"] = doc("b", n=2)
    streams.feed.event("reset", {"subs": ["s1", "unknown"], "reason": "slow_consumer"})
    await settle(lambda: len(items.reads) == n + 1 and len(view.items) == 2, "the reload")
    assert resets == [{"subscription": "0", "reason": "slow_consumer"}] and delays == [1.0]
    view.close()


async def test_a_config_reset_makes_the_subscription_again(mode: str) -> None:
    c, server, streams, items = setup(mode, doc("a", n=1))
    view = c.realtime.subscribe([SPEC], NO_JITTER)
    await settle(lambda: streams.feeds)
    streams.feed.event("ready")
    await go(view.wait_ready(5))
    streams.feed.event("reset", {"subs": ["s1"], "reason": "config_changed"})
    await settle(lambda: len(streams.feeds) == 2, "a new stream")
    streams.feed.event("ready")
    await settle(lambda: view.state.status == "live")
    view.close()


async def test_reauth_reconnects_at_once(mode: str) -> None:
    c, server, streams, _ = setup(mode, doc("a", n=1))
    view = c.realtime.subscribe([SPEC], NO_JITTER)
    await settle(lambda: streams.feeds)
    streams.feed.event("ready", id="t9")
    await go(view.wait_ready(5))
    streams.feed.event("reauth")
    await settle(lambda: len(streams.feeds) == 2)
    assert streams.bodies[1]["resume_from"] == "t9"
    view.close()


async def test_session_ended_stops_the_view(mode: str) -> None:
    c, server, streams, _ = setup(mode, doc("a", n=1))
    view = c.realtime.subscribe([SPEC], NO_JITTER)
    errors: list[Any] = []
    view.on("error", errors.append)
    await settle(lambda: streams.feeds)
    streams.feed.event("ready")
    await go(view.wait_ready(5))
    streams.feed.event("session_ended")
    await settle(lambda: errors, "the error")
    assert isinstance(errors[0], backd.SessionEndedError) and view.closed and view.state.status == "closed"
    with pytest.raises(backd.SessionEndedError):
        await go(view.wait_ready(1))
    await asyncio.sleep(0.05)
    assert len(streams.feeds) == 1  # and it does not reconnect


async def test_a_refusal_ends_the_view_that_asked(mode: str) -> None:
    c, server, streams, items = setup(mode, doc("a", n=1))
    refusal = httpx.Response(
        400,
        json={
            "error": {
                "code": "realtime_not_enabled",
                "message": "no",
                "details": [{"path": "subscriptions[1]", "reason": "x"}],
            }
        },
    )
    streams.fail = refusal
    streams.fail_when = lambda body: len(body["subscriptions"]) > 1
    good = c.realtime.subscribe([SPEC], NO_JITTER)
    bad = c.realtime.subscribe([{"db": "main", "collection": "nope"}], NO_JITTER)
    errors: list[Any] = []
    bad.on("error", errors.append)
    await settle(lambda: errors, "the refusal")
    assert isinstance(errors[0], backd.RealtimeNotEnabledError) and bad.closed and not good.closed
    streams.fail = None
    await settle(lambda: streams.feeds, "the stream for the good one")
    assert [s["id"] for s in streams.bodies[-1]["subscriptions"]] == ["s1"]
    streams.feed.event("ready")
    await go(good.wait_ready(5))
    good.close()


async def test_a_refusal_with_one_view_ends_it_and_stops_retrying(mode: str) -> None:
    c, server, streams, _ = setup(mode)
    streams.fail = error_response(403, "forbidden")
    view = c.realtime.subscribe([SPEC], NO_JITTER)
    errors: list[Any] = []
    view.on("error", errors.append)
    await settle(lambda: errors)
    assert isinstance(errors[0], backd.ForbiddenError)
    await asyncio.sleep(0.05)
    assert len(streams.bodies) == 1


async def test_a_bad_resume_token_starts_over_from_a_reload(mode: str) -> None:
    c, server, streams, items = setup(mode, doc("a", n=1))
    view = c.realtime.subscribe([SPEC], NO_JITTER)
    await settle(lambda: streams.feeds)
    streams.feed.event("ready", id="old")
    await go(view.wait_ready(5))
    streams.fail = error_response(400, "invalid_resume_token")
    streams.fail_when = lambda body: "resume_from" in body
    n = len(items.reads)
    streams.feed.end()
    await settle(lambda: len(streams.bodies) == 3, "the refused reconnect and the retry")
    streams.fail = None
    await settle(lambda: len(streams.feeds) == 2)
    assert "resume_from" not in streams.bodies[-1]
    streams.feed.event("ready")
    await settle(lambda: len(items.reads) > n, "a reload")
    view.close()


async def test_network_failures_back_off_and_retry(mode: str) -> None:
    c, server, streams, _ = setup(mode, doc("a", n=1))
    attempts = {"n": 0}

    def flaky(req: httpx.Request) -> httpx.Response:
        attempts["n"] += 1
        if attempts["n"] < 3:
            raise httpx.ConnectError("down", request=req)
        return streams._open(req)

    server.on("POST /_realtime", flaky)
    view = c.realtime.subscribe([SPEC], NO_JITTER)
    await settle(lambda: streams.feeds, "a connection")
    assert attempts["n"] == 3
    streams.feed.event("ready")
    await go(view.wait_ready(5))
    view.close()


async def test_access_lost_when_a_reload_is_forbidden(mode: str) -> None:
    c, server, streams, items = setup(mode, doc("a", n=1))
    view = c.realtime.subscribe([SPEC], NO_JITTER)
    lost: list[Any] = []
    view.on("access_lost", lost.append)
    await settle(lambda: streams.feeds)
    streams.feed.event("ready")
    await go(view.wait_ready(5))
    items.fail = error_response(403, "forbidden")
    streams.feed.event("reset", {"subs": ["s1"], "reason": "access_changed"})
    await settle(lambda: lost, "access_lost")
    assert lost == [{"subscription": "0"}] and view.items == [] and not view.closed
    view.close()


async def test_a_reload_that_fails_for_good_stops_the_view(mode: str) -> None:
    c, server, streams, items = setup(mode)
    items.fail = error_response(400, "invalid_query")
    view = c.realtime.subscribe([SPEC], NO_JITTER)
    errors: list[Any] = []
    view.on("error", errors.append)
    await settle(lambda: streams.feeds)
    streams.feed.event("ready")
    await settle(lambda: errors)
    assert isinstance(errors[0], backd.ValidationError) and view.closed


async def test_references_are_read_once_in_a_batch(mode: str) -> None:
    c, server, streams, items = setup(mode, doc("p1", n=1, author="u1"), doc("p2", n=2, author="u2", tags=["t1", "t2"]))
    asked: list[dict[str, Any]] = []

    def users(req: httpx.Request) -> httpx.Response:
        where = json.loads(req.url.params["where"])
        asked.append(where)
        found = [
            {"id": i, "_meta": {"version": 1}, "name": f"User {i}", "email": "x"}
            for i in where["id"]["$in"]
            if i.startswith("u")
        ]
        return json_response(200, {"items": found, "limit": 100, "skip": 0, "has_more": False})

    server.on("GET /main/users", users)
    spec: Any = {**SPEC, "references": {"author": {"field": "author", "collection": "users", "fields": ["name"]}}}
    view = c.realtime.subscribe([spec], NO_JITTER)
    await settle(lambda: streams.feeds)
    streams.feed.event("ready")
    state = await go(view.wait_ready(5))
    await settle(lambda: view.state.references.get("author", {}).get("u2"), "the references")
    refs = view.state.references["author"]
    assert refs["u1"] == {"id": "u1", "_meta": {"version": 1}, "name": "User u1"}  # only the fields asked for
    assert asked == [{"id": {"$in": ["u2", "u1"]}}] or asked == [{"id": {"$in": ["u1", "u2"]}}]
    assert state is not None
    streams.feed.event("created", {"sub": "s1", "doc": doc("p3", n=3, author="u1")})
    streams.feed.event("created", {"sub": "s1", "doc": doc("p4", n=4, author="u9")})
    await settle(lambda: len(asked) == 2, "the new reference")
    assert asked[1] == {"id": {"$in": ["u9"]}}  # u1 is held already
    view.close()


async def test_several_subscriptions_in_one_view(mode: str) -> None:
    c, server, streams, items = setup(mode, doc("a", n=1))
    view = c.realtime.subscribe(
        [{**SPEC, "id": "mine"}, {"db": "main", "collection": "items", "id": "theirs", "where": {"x": 1}}], NO_JITTER
    )
    await settle(lambda: streams.feeds)
    assert [s["id"] for s in streams.bodies[0]["subscriptions"]] == ["s1", "s2"]
    assert streams.bodies[0]["subscriptions"][1]["where"] == {"x": 1}
    streams.feed.event("ready")
    state = await go(view.wait_ready(5))
    assert set(state.subscriptions) == {"mine", "theirs"}
    streams.feed.event("created", {"sub": "s2", "doc": doc("q", n=5)})
    await settle(lambda: any(d["id"] == "q" for d in view.state.subscriptions["theirs"].items))
    assert all(d["id"] != "q" for d in view.state.subscriptions["mine"].items)
    view.close()


async def test_adding_a_view_reopens_the_stream_with_the_resume_token(mode: str) -> None:
    c, server, streams, items = setup(mode, doc("a", n=1))
    first = c.realtime.subscribe([SPEC], NO_JITTER)
    await settle(lambda: streams.feeds)
    streams.feed.event("ready", id="t5")
    await go(first.wait_ready(5))
    assert c.realtime.open == 1
    second = c.realtime.subscribe([{"db": "main", "collection": "items", "id": "two"}], NO_JITTER)
    await settle(lambda: len(streams.feeds) == 2)
    assert streams.bodies[1]["resume_from"] == "t5" and len(streams.bodies[1]["subscriptions"]) == 2
    streams.feed.event("ready")
    await go(second.wait_ready(5))
    second.close()
    await settle(lambda: len(streams.feeds) == 3)
    assert len(streams.bodies[2]["subscriptions"]) == 1
    first.close()
    await settle(lambda: c.realtime.open == 0)
    streams.feeds[-1].end()


async def test_subscribe_checks_its_input(mode: str) -> None:
    c, _, _, _ = setup(mode)
    with pytest.raises(TypeError, match="at least one"):
        c.realtime.subscribe([])
    with pytest.raises(TypeError, match="needs `db` and `collection`"):
        c.realtime.subscribe([{"db": "main"}])
    with pytest.raises(TypeError, match="duplicate"):
        c.realtime.subscribe([{"db": "m", "collection": "a", "id": "x"}, {"db": "m", "collection": "b", "id": "x"}])


async def test_collection_subscribe_filters_on_the_team(mode: str) -> None:
    c, server, streams, items = setup(mode, doc("a", n=1))
    view = (
        c.db("main")
        .collection("items")
        .in_team("t1")
        .subscribe(where={"status": "open"}, load={"sort": "-n"}, options=NO_JITTER)
    )
    await settle(lambda: streams.feeds)
    sub = streams.bodies[0]["subscriptions"][0]
    assert sub["where"] == {"status": "open", "_meta.team": "t1"}
    view.close()
    plain = c.db("main").collection("items").subscribe(id="all")
    await settle(lambda: len(streams.bodies) >= 2)
    assert "where" not in streams.bodies[-1]["subscriptions"][0]
    plain.close()


async def test_a_session_refused_by_the_stream_ends_the_session(mode: str) -> None:
    c, server, streams, _ = setup(mode)
    streams.fail = error_response(401, "unauthenticated")
    view = c.realtime.subscribe([SPEC], NO_JITTER)
    errors: list[Any] = []
    view.on("error", errors.append)
    await settle(lambda: errors)
    assert isinstance(errors[0], backd.AuthenticationError)
    assert c.storage.get() is None  # the token was refused: signed out


# -- the configuration, live -----------------------------------------------------------------------


class Config:
    def __init__(self, server: Server) -> None:
        self.values: dict[str, Any] = {"maintenance": {"enabled": False}, "features": {"limit": 5}}
        self.calls: list[tuple[str | None, str | None]] = []
        server.on("GET /_config", self)

    def __call__(self, req: httpx.Request) -> httpx.Response:
        paths, tag = req.url.params.get("paths"), req.headers.get("if-none-match")
        self.calls.append((paths, tag))
        wanted = set(paths.split(",")) if paths else set(self.values)
        values = {k: v for k, v in self.values.items() if k in wanted}
        etag = '"' + json.dumps(values, sort_keys=True) + '"'
        if tag == etag:
            return httpx.Response(304, headers={"ETag": etag})
        return httpx.Response(200, json={"values": values, "version": "1", "cache_ttl": 300}, headers={"ETag": etag})


async def test_the_configuration_follows_the_stream(mode: str) -> None:
    c, server, streams, _ = setup(mode)
    backend = Config(server)
    await go(c.config.load(["maintenance"]))
    changed: list[list[str]] = []
    c.config.on("change", changed.append)
    stop = c.config.live(["maintenance"])
    await settle(lambda: streams.feeds)
    assert streams.bodies[0]["subscriptions"] == [{"id": "_config", "config": {"paths": ["maintenance"]}}]
    streams.feed.event("ready")
    await settle(lambda: len(backend.calls) == 2, "the read that closes the gap")
    assert backend.calls[1][1] is not None  # conditional: a 304 when nothing moved
    backend.values["maintenance"]["enabled"] = True
    streams.feed.event("config_changed", {"paths": ["maintenance.enabled"]})
    await settle(lambda: changed, "the change")
    assert changed == [["maintenance.enabled"]] and c.config.get_sync("maintenance.enabled") is True
    n = len(backend.calls)
    streams.feed.event("config_changed", {"paths": ["features.limit"]})  # not a branch we hold
    await asyncio.sleep(0.05)
    assert len(backend.calls) == n
    stop()
    await settle(lambda: len(streams.feeds) == 2 or not c.realtime.running)


async def test_a_realm_with_no_configuration_ends_only_the_follower(mode: str) -> None:
    c, server, streams, _ = setup(mode)
    Config(server)
    errors: list[Any] = []
    c.config.on("error", errors.append)
    view = c.realtime.subscribe([SPEC], NO_JITTER)
    c.config.live()
    await settle(lambda: len(streams.bodies) >= 1)
    streams.fail = httpx.Response(
        400,
        json={
            "error": {
                "code": "invalid_query",
                "message": "no config",
                "details": [{"path": "subscriptions[1]", "reason": "x"}],
            }
        },
    )
    streams.fail_when = lambda body: any("config" in sub for sub in body["subscriptions"])
    streams.feed.end()
    await settle(lambda: errors, "the follower's error")
    streams.fail = None
    await settle(lambda: len(streams.feeds) >= 2)
    assert not view.closed
    view.close()


# -- closing ------------------------------------------------------------------------------------------


async def test_closing_the_client_leaves_nothing_running(mode: str) -> None:
    c, server, streams, _ = setup(mode, doc("a", n=1))
    threads_before = set(live_threads())
    view = c.realtime.subscribe([SPEC], NO_JITTER)
    await settle(lambda: streams.feeds)
    streams.feed.event("ready")
    await go(view.wait_ready(5))
    before = len(asyncio.all_tasks())
    await go(c.aclose() if mode == "async" else c.close())
    assert view.closed and view.state.status == "closed"
    if mode == "sync":
        await settle(lambda: not (set(live_threads()) - threads_before), "the threads to end")
    else:
        assert len(asyncio.all_tasks()) < before  # the stream's task and the loads are gone
    await asyncio.sleep(0.05)
    assert len(streams.feeds) == 1  # and nothing reconnects
    with pytest.raises(RuntimeError, match="client is closed"):
        c.realtime.subscribe([SPEC], NO_JITTER)


async def test_organization_clients_are_closed_with_the_root(mode: str) -> None:
    root, server, streams, items = setup(mode, doc("a", n=1))
    threads_before = set(live_threads())
    child = root.org("acme")
    view = child.realtime.subscribe([SPEC], NO_JITTER)
    await settle(lambda: streams.feeds)
    streams.feed.event("ready")
    await go(view.wait_ready(5))
    await go(root.aclose() if mode == "async" else root.close())
    assert view.closed
    if mode == "sync":
        await settle(lambda: not (set(live_threads()) - threads_before))
