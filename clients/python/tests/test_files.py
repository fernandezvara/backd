from __future__ import annotations

import hashlib
import json
from pathlib import Path
from typing import Any, cast

import httpx
import pytest

import backd
from backd import Upload

from .helpers import Server, error_response, go, json_response, make_client

MIB = 1024 * 1024
DETAILS: dict[str, Any] = {
    "id": "fl_1", "name": "a.txt", "size": 5, "type": "text/plain",
    "sha256": hashlib.sha256(b"hello").hexdigest(), "uploaded_at": "t",
}  # fmt: skip


def posts(mode: str) -> tuple[Any, Server, Any]:
    server = Server()
    client = make_client(mode, server, storage=backd.MemoryStorage())
    return client, server, client.db("main").collection("posts")


async def read_all(content: Any) -> bytes:
    return cast(bytes, await go(content.read()))


async def test_list_and_the_one_file_of_a_single_field(mode: str) -> None:
    _, server, posts_ = posts(mode)
    files = posts_.files("p1", "cover")
    server.on("GET /main/posts/p1", {"id": "p1", "cover": DETAILS})
    assert [f["id"] for f in await go(files.list())] == ["fl_1"]
    server.on("GET /main/posts/p1", {"id": "p1", "cover": None})
    assert await go(files.list()) == []
    with pytest.raises(backd.NotFoundError, match="holds no file"):
        await go(files.link())
    server.on("GET /main/posts/p1", {"id": "p1", "cover": [DETAILS, {**DETAILS, "id": "fl_2"}]})
    with pytest.raises(TypeError, match="several files"):
        await go(files.link())


async def test_get_downloads_and_checks_the_checksum(mode: str) -> None:
    _, server, posts_ = posts(mode)
    files = posts_.files("p1", "cover")
    server.on("GET /main/posts/p1", {"id": "p1", "cover": DETAILS})
    server.on(
        "GET /main/posts/p1/_files/cover/fl_1",
        httpx.Response(200, content=b"hello", headers={"Content-Type": "text/plain"}),
    )
    content = await go(files.get())
    assert content.file["id"] == "fl_1"
    assert await read_all(content) == b"hello"
    assert server.last().url.params.get("version") is None

    content = await go(files.get("fl_1"))  # no details looked up: nothing to check against
    assert content.file is None and await read_all(content) == b"hello"

    server.on("GET /main/posts/p1/_files/cover/fl_1", httpx.Response(200, content=b"HELLO"))
    with pytest.raises(backd.BackdError) as ei:
        await read_all(await go(files.get()))
    assert ei.value.code == "checksum_mismatch"
    assert await read_all(await go(files.get("fl_1", file=None))) == b"HELLO"

    await go(files.get("fl_1", version="thumb", file=DETAILS))  # a version is not the original: no check
    assert server.last().url.params["version"] == "thumb"

    server.on("GET /main/posts/p1/_files/cover/fl_1", lambda r: error_response(404, "file_missing"))
    with pytest.raises(backd.NotFoundError):
        await go(files.get("fl_1"))


async def test_save_streams_to_a_path_or_a_file_and_leaves_nothing_on_failure(mode: str, tmp_path: Path) -> None:
    _, server, posts_ = posts(mode)
    files = posts_.files("p1", "cover")
    server.on("GET /main/posts/p1/_files/cover/fl_1", httpx.Response(200, content=b"hello"))
    out = tmp_path / "out" / "a.txt"
    out.parent.mkdir()
    content = await go(files.get("fl_1", file=DETAILS))
    assert await go(content.save(out)) == 5
    assert out.read_bytes() == b"hello"
    with (tmp_path / "b.bin").open("wb") as f:
        assert await go((await go(files.get("fl_1"))).save(f)) == 5
    assert (tmp_path / "b.bin").read_bytes() == b"hello"

    server.on("GET /main/posts/p1/_files/cover/fl_1", httpx.Response(200, content=b"HELLO"))
    bad = tmp_path / "bad.txt"
    with pytest.raises(backd.BackdError, match="checksum"):
        await go((await go(files.get("fl_1", file=DETAILS))).save(bad))
    assert not bad.exists() and not (tmp_path / "bad.txt.part").exists()

    server.on("GET /main/posts/p1/_files/cover/fl_1", httpx.Response(200, content=b"hello"))
    assert await go(posts_.download_file("p1", "cover", "fl_1", tmp_path / "d.txt", file=DETAILS)) == 5
    assert (await go((await go(files.get("fl_1"))).text())) == "hello"


async def test_put_streams_the_file_through_backd(mode: str, tmp_path: Path) -> None:
    _, server, posts_ = posts(mode)
    seen: list[httpx.Request] = []

    def handler(req: httpx.Request) -> httpx.Response:
        seen.append(req)
        return json_response(200, {"id": "p1", "cover": DETAILS})

    server.on("POST /main/posts/p1/_files/cover", handler)
    files = posts_.files("p1", "cover")
    doc = await go(files.put(b"hello", name="a.txt", type="text/plain", if_match=2))
    assert doc["cover"]["id"] == "fl_1"
    r = seen[0]
    assert r.content == b"hello" and r.url.params["name"] == "a.txt"
    assert r.headers["content-type"] == "text/plain" and r.headers["if-match"] == '"2"'
    assert r.headers["content-length"] == "5"

    f = tmp_path / "big.bin"
    f.write_bytes(b"x" * (3 * 256 * 1024 + 7))
    progress: list[tuple[int, int | None]] = []
    await go(files.put(f, on_progress=lambda sent, total: progress.append((sent, total))))
    assert seen[1].content == f.read_bytes() and seen[1].url.params["name"] == "big.bin"
    assert progress[-1] == (f.stat().st_size, f.stat().st_size) and [p[0] for p in progress] == sorted(
        p[0] for p in progress
    )
    assert len(progress) == 4

    await go(files.put("plain text"))
    assert seen[2].content == b"plain text" and seen[2].url.params["name"] == "file"
    await go(files.put(Upload(b"zz", name="n.dat", type="x/y")))
    assert seen[3].headers["content-type"] == "x/y"


async def test_delete_one_or_all(mode: str) -> None:
    _, server, posts_ = posts(mode)
    server.on("DELETE /main/posts/p1/_files/cover/fl_1", {"id": "p1"})
    server.on("DELETE /main/posts/p1/_files/cover", {"id": "p1"})
    await go(posts_.delete_file("p1", "cover", "fl_1", if_match=3))
    assert server.last().headers["if-match"] == '"3"'
    await go(posts_.files("p1", "cover").delete())
    assert server.paths()[-1] == "DELETE /main/posts/p1/_files/cover"
    with pytest.raises(TypeError, match="required"):
        await go(posts_.delete_file("p1", "cover", ""))


async def test_link_and_file_url(mode: str) -> None:
    _, server, posts_ = posts(mode)
    server.on("GET /main/posts/p1/_files/cover/fl_1", {"url": "https://s.test/x", "expires_at": "2030-01-01T00:00:00Z"})
    link = await go(posts_.file_url("p1", "cover", "fl_1"))
    assert link == {"url": "https://s.test/x", "expires_at": "2030-01-01T00:00:00Z"}
    assert server.last().url.params["link"] == "json"
    n = len(server.calls)
    public = {
        **DETAILS,
        "public": True,
        "url": "https://cdn.test/a.txt",
        "versions": {"thumb": {"status": "ready", "url": "https://cdn.test/t.png"}, "big": {"status": "pending"}},
    }
    assert await go(posts_.file_url("p1", "cover", "fl_1", file=public)) == {
        "url": "https://cdn.test/a.txt",
        "expires_at": None,
    }
    assert await go(posts_.file_url("p1", "cover", "fl_1", file=public, version="thumb")) == {
        "url": "https://cdn.test/t.png",
        "expires_at": None,
    }
    assert await go(posts_.file_url("p1", "cover", "fl_1", file=public, version="big")) is None
    assert len(server.calls) == n  # no request for a public file

    server.on("GET /main/posts/p1/_files/cover/fl_1", lambda r: error_response(404, "version_unavailable"))
    assert await go(posts_.file_url("p1", "cover", "fl_1", version="thumb")) is None
    with pytest.raises(backd.NotFoundError):
        await go(posts_.file_url("p1", "cover", "fl_1"))  # without a version it is an error


def test_public_url_and_version_status() -> None:
    f: Any = {
        **DETAILS,
        "public": True,
        "url": "u",
        "versions": {
            "t": {"status": "ready", "url": "tu", "width": 10, "height": 5},
            "p": {"status": "pending"},
            "s": {"status": "skipped", "reason": "not_an_image"},
        },
    }
    assert backd.public_url(f) == "u" and backd.public_url(f, "t") == "tu"
    assert backd.public_url(f, "p") is None and backd.public_url(f, "nope") is None
    assert backd.public_url(DETAILS) is None and backd.public_url(None) is None  # type: ignore[arg-type]
    assert backd.version_status(f, "t") == {"status": "ready", "ready": True, "width": 10, "height": 5}
    assert backd.version_status(f, "p") == {"status": "pending", "ready": False}
    assert backd.version_status(f, "s") == {"status": "skipped", "ready": False, "reason": "not_an_image"}
    assert backd.version_status(f, "x") == {"status": "undeclared", "ready": False}


async def test_versions(mode: str) -> None:
    _, server, posts_ = posts(mode)
    server.on("GET /main/posts/p1", {"id": "p1", "cover": DETAILS})
    for method in ("POST", "DELETE"):
        server.on(
            f"{method} /main/posts/p1/_files/cover/fl_1/versions/thumb",
            {"status": "ready" if method == "POST" else "pending"},
        )
    v = posts_.files("p1", "cover").version("thumb")
    assert (await go(v.regenerate()))["status"] == "ready"
    assert server.last().content == b""
    await go(v.generate(max_width=100, fit="cover", upscale=False))
    assert server.body() == {"max_width": 100, "fit": "cover", "upscale": False}
    assert (await go(v.delete()))["status"] == "pending"
    server.on(
        "POST /main/posts/p1/_files/cover/fl_1/versions/thumb", lambda r: error_response(403, "version_not_writable")
    )
    with pytest.raises(backd.ForbiddenError) as ei:
        await go(posts_.files("p1", "cover").version("thumb", "fl_1").generate(max_width=1))
    assert ei.value.code == "version_not_writable"


FIELD_PROXY = {
    "name": "cover",
    "upload": "proxy",
    "download": "proxy",
    "multiple": False,
    "max_files": 1,
    "max_size": 1000,
    "types": [],
}
FIELD_DIRECT = {**FIELD_PROXY, "upload": "direct", "download": "presigned"}


async def test_file_field_is_asked_once(mode: str) -> None:
    client, server, posts_ = posts(mode)
    server.on("GET /main/posts/_files/cover", FIELD_PROXY)
    assert (await go(posts_.file_field("cover")))["max_size"] == 1000
    await go(posts_.file_field("cover"))
    assert server.paths().count("GET /main/posts/_files/cover") == 1
    assert client._file_fields

    server.on("GET /main/posts/_files/other", lambda r: error_response(404, "not_found"))
    for _ in range(2):  # a failure isn't remembered
        with pytest.raises(backd.NotFoundError):
            await go(posts_.file_field("other"))
    assert server.paths().count("GET /main/posts/_files/other") == 2


async def test_upload_file_through_the_proxy(mode: str) -> None:
    _, server, posts_ = posts(mode)
    server.on("GET /main/posts/_files/cover", FIELD_PROXY)
    server.on("POST /main/posts/p1/_files/cover", {"id": "p1", "cover": DETAILS})
    doc = await go(posts_.upload_file("p1", "cover", b"hello", name="a.txt"))
    assert doc["cover"]["id"] == "fl_1"
    assert server.last().content == b"hello"


def bucket(server: Server, store: dict[str, Any]) -> None:
    def put(req: httpx.Request) -> httpx.Response:
        store.setdefault("puts", []).append((req.url.path, req.content, dict(req.headers)))
        return httpx.Response(200 if not store.get("fail") else store["fail"])

    server.on("PUT /obj/one", put)
    for i in range(1, 4):
        server.on(f"PUT /obj/part{i}", put)


async def test_upload_file_directly_to_the_bucket(mode: str) -> None:
    _, server, posts_ = posts(mode)
    store: dict[str, Any] = {}
    bucket(server, store)
    server.on("GET /main/posts/_files/cover", FIELD_DIRECT)
    server.on(
        "POST /main/posts/p1/_files/cover/uploads",
        {
            "upload_id": "up1",
            "upload_token": "tk",
            "url": "https://bucket.test/obj/one",
            "headers": {"x-amz-meta": "m"},
        },
    )
    server.on("POST /main/posts/_files/cover/uploads/up1/complete", {"id": "p1", "cover": DETAILS})
    progress: list[tuple[int, int | None]] = []
    storage_key = backd.MemoryStorage()
    storage_key.set("bds_session")
    doc = await go(
        posts_.upload_file(
            "p1", "cover", b"hello", name="a.txt", if_match=4, on_progress=lambda s, t: progress.append((s, t))
        )
    )
    assert doc["cover"]["id"] == "fl_1"
    start = next(json.loads(r.content) for r in server.calls if r.url.path.endswith("/p1/_files/cover/uploads"))
    assert start == {"name": "a.txt", "size": 5, "type": "text/plain", "sha256": hashlib.sha256(b"hello").hexdigest()}
    (_, body, headers) = store["puts"][0]
    assert body == b"hello" and headers["x-amz-meta"] == "m"
    assert "authorization" not in headers  # the bucket never sees backd's credentials
    assert progress[-1] == (5, 5)
    completes = [r for r in server.calls if r.url.path.endswith("/complete")]
    assert json.loads(completes[0].content) == {"upload_token": "tk"}
    assert completes[0].headers["if-match"] == '"4"'

    store["fail"] = 403
    with pytest.raises(backd.BackdError) as ei:
        await go(posts_.upload_file("p1", "cover", b"hello", name="a.txt"))
    assert (ei.value.code, ei.value.status) == ("upload_failed", 403)

    with pytest.raises(ValueError, match="hashes the file"):
        import io

        class Pipe(io.RawIOBase):
            def readable(self) -> bool:
                return True

            def readinto(self, b: bytearray) -> int:  # type: ignore[override]
                return 0

        await go(posts_.upload_file("p1", "cover", Pipe()))


async def test_upload_in_parts(mode: str, monkeypatch: pytest.MonkeyPatch) -> None:
    _, server, posts_ = posts(mode)
    store: dict[str, Any] = {}
    bucket(server, store)
    data = bytes(range(256)) * 100  # 25,600 bytes
    monkeypatch.setattr("backd._async.files.part_size_for", lambda size: 10_000)
    monkeypatch.setattr("backd._sync.files.part_size_for", lambda size: 10_000)
    field = {**FIELD_DIRECT, "multipart_above": 20_000}
    server.on("GET /main/posts/_files/cover", field)
    parts = [
        {"number": i + 1, "url": f"https://bucket.test/obj/part{i + 1}", "headers": {"x-part": str(i + 1)}, "size": s}
        for i, s in enumerate([10_000, 10_000, 5_600])
    ]
    server.on(
        "POST /main/posts/p1/_files/cover/uploads",
        {"upload_id": "up1", "upload_token": "tk", "multipart": True, "part_size": 10_000, "parts": parts},
    )
    server.on("POST /main/posts/_files/cover/uploads/up1/complete", {"id": "p1", "cover": DETAILS})
    progress: list[int] = []
    await go(posts_.upload_file("p1", "cover", data, on_progress=lambda s, t: progress.append(s)))
    start = next(json.loads(r.content) for r in server.calls if r.url.path.endswith("/p1/_files/cover/uploads"))
    assert "sha256" not in start
    assert start["part_sha256"] == [
        hashlib.sha256(data[i : i + 10_000]).hexdigest() for i in range(0, len(data), 10_000)
    ]
    assert b"".join(body for _, body, _ in store["puts"]) == data
    assert [h["x-part"] for _, _, h in store["puts"]] == ["1", "2", "3"]
    assert progress[-1] == len(data) and progress == sorted(progress)


async def test_a_failing_part_is_sent_again_but_not_forever(mode: str, monkeypatch: pytest.MonkeyPatch) -> None:
    import backd._async.files as af
    import backd._sync.files as sf

    async def asleep(s: float) -> None:
        return None

    monkeypatch.setattr(af, "sleep", asleep)
    monkeypatch.setattr(sf, "sleep", lambda s: None)
    for mod in (af, sf):
        monkeypatch.setattr(mod, "part_size_for", lambda size: 4)
    _, server, posts_ = posts(mode)
    attempts = {"n": 0}

    def flaky(req: httpx.Request) -> httpx.Response:
        attempts["n"] += 1
        return httpx.Response(503 if attempts["n"] < 3 else 200)

    server.on("PUT /obj/part1", flaky)
    server.on("GET /main/posts/_files/cover", {**FIELD_DIRECT, "multipart_above": 1})
    server.on(
        "POST /main/posts/p1/_files/cover/uploads",
        {
            "upload_id": "u",
            "upload_token": "t",
            "multipart": True,
            "part_size": 4,
            "parts": [{"number": 1, "url": "https://bucket.test/obj/part1", "headers": {}, "size": 4}],
        },
    )
    server.on("POST /main/posts/_files/cover/uploads/u/complete", {"id": "p1"})
    await go(posts_.upload_file("p1", "cover", b"abcd"))
    assert attempts["n"] == 3

    attempts["n"] = -100  # never succeeds
    with pytest.raises(backd.BackdError) as ei:
        await go(posts_.upload_file("p1", "cover", b"abcd"))
    assert ei.value.status == 503 and ei.value.code == "upload_failed"

    server.on("PUT /obj/part1", lambda r: httpx.Response(403))
    n = len([r for r in server.calls if r.method == "PUT"])
    with pytest.raises(backd.BackdError):
        await go(posts_.upload_file("p1", "cover", b"abcd"))
    assert len([r for r in server.calls if r.method == "PUT"]) == n + 1  # a refusal is not repeated


async def test_prepare_upload(mode: str) -> None:
    _, server, posts_ = posts(mode)
    pending = {
        "upload_id": "up9",
        "upload_token": "tk9",
        "expires_at": "2030-01-01T00:00:00Z",
        "file": {"name": "a.txt", "size": 5, "type": "text/plain", "sha256": "x"},
    }
    server.on("GET /main/posts/_files/cover", FIELD_PROXY)
    server.on("POST /main/posts/_files/cover/uploads", pending)
    up = await go(posts_.prepare_upload("cover", b"hello", name="a.txt"))
    assert up["ref"] == {"upload": "up9", "token": "tk9"} and up["id"] == "up9" and up["file"]["size"] == 5
    assert server.last().url.params["name"] == "a.txt" and server.last().content == b"hello"

    _, server, posts_ = posts(mode)  # a client of its own: the field is remembered as a proxy one
    store: dict[str, Any] = {}
    bucket(server, store)
    server.on("GET /main/posts/_files/cover", FIELD_DIRECT)
    server.on("POST /main/posts/_files/cover/uploads", {**pending, "url": "https://bucket.test/obj/one", "headers": {}})
    server.on(
        "POST /main/posts/_files/cover/uploads/up9/complete",
        {"file": {"name": "a.txt", "size": 5, "type": "text/plain", "sha256": "y"}},
    )
    up = await go(posts_.prepare_upload("cover", b"hello", name="a.txt"))
    assert up["file"]["sha256"] == "y" and up["ref"]["upload"] == "up9"
    assert "if-match" not in server.last().headers
    server.on("POST /main/posts", {"id": "p9"}, 201)
    await go(posts_.create({"title": "T", "cover": [up["ref"]]}))
    assert server.body() == {"title": "T", "cover": [{"upload": "up9", "token": "tk9"}]}
