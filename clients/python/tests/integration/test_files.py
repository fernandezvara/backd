"""Files against a real backd and MinIO: a document upload through backd, a direct upload to the bucket, the bytes
back, a signed link."""

from __future__ import annotations

import hashlib
from pathlib import Path

import pytest

import backd

from .conftest import Users, go


async def test_upload_through_backd_bytes_back_signed_link(users: Users, tmp_path: Path) -> None:
    client, _ = await users.sign_up("upload", "files")
    docs = client.db("app").collection("docs")

    doc = await go(docs.create({"title": "with-file"}))
    updated = await go(docs.upload_file(doc["id"], "attachment", b"attachment contents", name="note.txt"))

    # The document holds the file's details.
    details = updated["attachment"]
    assert details["name"] == "note.txt" and details["size"] == len(b"attachment contents")

    # The bytes come back, and match the checksum the details carry.
    content = await go(docs.files(doc["id"], "attachment").get(details["id"], file=details))
    assert await go(content.read()) == b"attachment contents"
    out = tmp_path / "out.txt"
    assert await go(docs.download_file(doc["id"], "attachment", details["id"], out, file=details)) == 19
    assert out.read_bytes() == b"attachment contents"

    # A signed link for the host's MinIO.
    link = await go(docs.file_url(doc["id"], "attachment", details["id"]))
    assert link is not None and link["url"]


async def test_a_direct_upload_goes_straight_to_the_bucket(users: Users, tmp_path: Path) -> None:
    client, _ = await users.sign_up("direct", "files")
    docs = client.db("app").collection("docs")

    # The `video` field is `upload: direct`: the client declares, sends to the signed link, completes.
    doc = await go(docs.create({"title": "direct"}))
    path = tmp_path / "clip.txt"
    path.write_bytes(b"plain text video")
    progress: list[int] = []
    updated = await go(
        docs.upload_file(doc["id"], "video", path, type="text/plain", on_progress=lambda s, t: progress.append(s))
    )
    details = updated["video"]
    assert details["sha256"] == hashlib.sha256(b"plain text video").hexdigest()
    assert progress and progress[-1] == len(b"plain text video")
    content = await go(docs.files(doc["id"], "video").get(details["id"]))
    assert await go(content.read()) == b"plain text video"


async def test_a_file_the_field_refuses(users: Users) -> None:
    client, _ = await users.sign_up("refused", "files")
    docs = client.db("app").collection("docs")
    doc = await go(docs.create({"title": "too big"}))
    with pytest.raises(backd.BackdError):
        await go(docs.upload_file(doc["id"], "attachment", b"x" * (65 * 1024), name="big.bin"))
