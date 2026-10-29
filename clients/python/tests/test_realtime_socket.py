"""The stream over a real socket: closing the client must wake a reader that is blocked waiting for the next event."""

from __future__ import annotations

import asyncio
import json
import threading
import time
from collections.abc import Iterator
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

import pytest

import backd


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    opened = 0

    def log_message(self, *args: Any) -> None:
        pass

    def do_POST(self) -> None:
        self.rfile.read(int(self.headers.get("Content-Length", 0)))
        type(self).opened += 1
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()
        try:
            self._chunk(b"event: ready\ndata: {}\n\n")
            while True:  # then quiet: a heartbeat only every 25 seconds, like the real thing
                time.sleep(25)
                self._chunk(b": heartbeat\n\n")
        except OSError:
            return

    def do_GET(self) -> None:
        body = json.dumps({"items": [], "limit": 100, "skip": 0, "has_more": False}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _chunk(self, data: bytes) -> None:
        self.wfile.write(f"{len(data):x}\r\n".encode() + data + b"\r\n")
        self.wfile.flush()


@pytest.fixture
def server() -> Iterator[str]:
    Handler.opened = 0
    httpd = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    httpd.daemon_threads = True
    threading.Thread(target=httpd.serve_forever, daemon=True).start()
    yield f"http://127.0.0.1:{httpd.server_address[1]}"
    httpd.shutdown()
    httpd.server_close()


SPEC: Any = {"db": "main", "collection": "items"}


def test_closing_wakes_a_thread_blocked_on_a_quiet_stream(server: str) -> None:
    client = backd.Client(url=server, realm="shelf", api_key="bdk_k")
    view = client.realtime.subscribe([SPEC])
    assert view.wait_ready(5).status == "live"
    time.sleep(0.2)  # the reader is now waiting for the next byte
    started = time.monotonic()
    client.close()
    assert time.monotonic() - started < 3  # not the 5 seconds a join gives up after
    assert view.closed
    assert not [t for t in threading.enumerate() if t.name.startswith("backd-")]


def test_closing_a_view_reopens_nothing(server: str) -> None:
    client = backd.Client(url=server, realm="shelf", api_key="bdk_k")
    view = client.realtime.subscribe([SPEC])
    view.wait_ready(5)
    view.close()
    time.sleep(0.3)
    assert Handler.opened == 1
    client.close()


async def test_the_async_client_over_a_real_socket(server: str) -> None:
    client = backd.AsyncClient(url=server, realm="shelf", api_key="bdk_k")
    view = client.realtime.subscribe([SPEC])
    assert (await view.wait_ready(5)).status == "live"
    await asyncio.sleep(0.2)
    before = len(asyncio.all_tasks())
    await client.aclose()
    assert view.closed
    assert len(asyncio.all_tasks()) < before
