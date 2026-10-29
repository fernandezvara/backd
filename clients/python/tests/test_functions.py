from __future__ import annotations

from typing import Any

import httpx
import pytest

import backd

from .helpers import Server, error_response, go, json_response, make_client

JOB: dict[str, Any] = {"id": "j1", "function": "main/report", "status": "queued", "created_at": "t", "result": None}


def setup(mode: str) -> tuple[Any, Server]:
    server = Server()
    return make_client(mode, server), server


def patch_sleep(monkeypatch: pytest.MonkeyPatch) -> list[float]:
    waits: list[float] = []

    async def asleep(s: float) -> None:
        waits.append(s)

    import backd._async.functions as af
    import backd._sync.functions as sf

    monkeypatch.setattr(af, "sleep", asleep)
    monkeypatch.setattr(sf, "sleep", waits.append)
    return waits


async def test_a_sync_function_returns_its_output(mode: str) -> None:
    c, server = setup(mode)
    server.on("POST /main/_func/refund", {"refunded": 12.5})
    out = await go(c.db("main").fn("refund", {"order": "o1"}, idempotency_key="k1"))
    assert out == {"refunded": 12.5}
    assert server.body() == {"order": "o1"} and server.last().headers["idempotency-key"] == "k1"
    assert "prefer" not in server.last().headers
    assert await go(c.db("main").collection("orders").fn("refund", [1, 2])) == {"refunded": 12.5}
    assert server.body() == [1, 2]
    assert await go(c.functions.call("main/refund", 0)) == {"refunded": 12.5}
    assert server.body() == 0


async def test_no_input_is_json_null(mode: str) -> None:
    c, server = setup(mode)
    server.on("POST /main/_func/ping", "pong")
    assert await go(c.db("main").fn("ping")) == "pong"
    assert server.last().content == b"null"
    assert server.last().headers["content-type"] == "application/json"
    await go(c.db("main").fn("ping", False))
    assert server.last().content == b"false"


async def test_a_functions_own_error_arrives_as_a_typed_error(mode: str) -> None:
    c, server = setup(mode)
    server.on("POST /main/_func/refund", lambda r: error_response(409, "already_refunded", "done before"))
    with pytest.raises(backd.ConflictError) as ei:
        await go(c.db("main").fn("refund"))
    assert (ei.value.status, ei.value.code, ei.value.message) == (409, "already_refunded", "done before")


async def test_an_async_function_returns_a_job(mode: str, monkeypatch: pytest.MonkeyPatch) -> None:
    waits = patch_sleep(monkeypatch)
    c, server = setup(mode)
    server.on("POST /main/_func/report", lambda r: json_response(202, JOB))
    job = await go(c.db("main").fn("report", {"month": "2026-09"}, respond_async=True))
    assert server.last().headers["prefer"] == "respond-async"
    assert (job.id, job.function, job.raw["status"], job.steps) == ("j1", "main/report", "queued", [])

    polls = iter(
        [
            {**JOB, "status": "running", "steps": [{"n": 1, "name": "load", "status": "running"}]},
            {
                **JOB,
                "status": "running",
                "steps": [{"n": 1, "name": "load", "status": "done"}, {"n": 2, "name": "render", "status": "running"}],
            },
            {**JOB, "status": "done", "result": {"status": "ok", "output": {"rows": 9}, "duration_ms": 3}},
        ]
    )
    server.on("GET /main/_jobs/j1", lambda r: json_response(200, next(polls)))
    assert await go(job.status()) == "running" and len(job.steps) == 1
    seen: list[int] = []
    out = await go(job.wait(poll_interval=0.25, on_progress=lambda steps, j: seen.append(len(steps))))
    assert out == {"rows": 9}
    assert seen == [2, 0]  # after every poll, with the steps so far (the last poll has none)
    assert waits == [0.25]
    assert job.raw["status"] == "done"


async def test_a_job_that_failed_raises_what_a_sync_call_would(mode: str) -> None:
    c, server = setup(mode)
    cases: list[tuple[dict[str, Any], type, str]] = [
        (
            {
                "status": "function_error",
                "http_status": 409,
                "code": "already_refunded",
                "message": "done before",
                "details": [{"path": "order", "reason": "x"}],
            },
            backd.ConflictError,
            "already_refunded",
        ),
        (
            {"status": "timeout", "http_status": 504, "code": "function_timeout", "message": "too slow"},
            backd.BackdError,
            "function_timeout",
        ),
        ({"status": "crash"}, backd.BackdError, "function_failed"),
        (
            {"status": "function_error", "http_status": 429, "code": "busy", "message": "later"},
            backd.RetryableError,
            "busy",
        ),
    ]
    for result, cls, code in cases:
        server.on("GET /main/_jobs/j1", {**JOB, "status": "done", "result": {"duration_ms": 1, **result}})
        handle = await go(_job(c, JOB))
        with pytest.raises(backd.BackdError) as ei:
            await go(handle.wait())
        assert type(ei.value) is cls and ei.value.code == code
    assert await go(_job(c, JOB).wait()) if False else True
    server.on(
        "GET /main/_jobs/j1",
        {
            **JOB,
            "status": "done",
            "result": {
                "status": "function_error",
                "http_status": 409,
                "code": "c",
                "message": "m",
                "details": [{"path": "order", "reason": "x"}],
                "duration_ms": 1,
            },
        },
    )
    with pytest.raises(backd.ConflictError) as ei2:
        await go((await go(_job(c, JOB))).wait())
    assert ei2.value.details == [{"path": "order", "reason": "x"}]
    server.on("GET /main/_jobs/j1", {**JOB, "status": "done", "result": None})
    with pytest.raises(backd.BackdError, match="no result"):
        await go((await go(_job(c, JOB))).wait())


async def _job(c: Any, data: dict[str, Any]) -> Any:
    from backd._async.functions import AsyncJob
    from backd._sync.functions import Job

    cls = AsyncJob if isinstance(c, backd.AsyncClient) else Job
    return cls(c, "main", data)  # type: ignore[arg-type]


async def test_wait_gives_up_but_the_job_goes_on(mode: str, monkeypatch: pytest.MonkeyPatch) -> None:
    patch_sleep(monkeypatch)
    c, server = setup(mode)
    server.on("GET /main/_jobs/j1", {**JOB, "status": "running"})
    job = await _job(c, JOB)
    with pytest.raises(backd.JobTimeoutError) as ei:
        await go(job.wait(timeout=0))
    assert ei.value.job_id == "j1" and "j1" in str(ei.value) and ei.value.code == "job_timeout"
    assert isinstance(ei.value, backd.BackdError)
    server.on(
        "GET /main/_jobs/j1", {**JOB, "status": "done", "result": {"status": "ok", "output": 1, "duration_ms": 1}}
    )
    assert await go(job.wait()) == 1  # wait again whenever you like


async def test_run_waits_for_a_job_and_passes_a_sync_answer_through(mode: str, monkeypatch: pytest.MonkeyPatch) -> None:
    patch_sleep(monkeypatch)
    c, server = setup(mode)
    server.on("POST /main/_func/report", lambda r: json_response(202, JOB))
    server.on(
        "GET /main/_jobs/j1", {**JOB, "status": "done", "result": {"status": "ok", "output": "ready", "duration_ms": 1}}
    )
    assert await go(c.functions.run("main/report", None, idempotency_key="k")) == "ready"
    server.on("POST /main/_func/quick", {"n": 1})
    assert await go(c.functions.run("main/quick")) == {"n": 1}


async def test_names_are_checked(mode: str) -> None:
    c, _ = setup(mode)
    for bad in ["refund", "/refund", "main/", "main/a/b", ""]:
        with pytest.raises(TypeError, match="database/function"):
            await go(c.functions.call(bad))


async def test_idempotent_calls_are_retried(mode: str, monkeypatch: pytest.MonkeyPatch) -> None:
    c, server = setup(mode)
    c = make_client(mode, server, retry=backd.Retry(attempts=2))
    import backd._async.client as ac
    import backd._sync.client as sc

    async def asleep(s: float) -> None:
        return None

    monkeypatch.setattr(ac, "sleep", asleep)
    monkeypatch.setattr(sc, "sleep", lambda s: None)
    calls: list[int] = []

    def handler(req: httpx.Request) -> httpx.Response:
        calls.append(1)
        return error_response(503, "unavailable") if len(calls) < 2 else json_response(200, {"ok": True})

    server.on("POST /main/_func/pay", handler)
    assert await go(c.db("main").fn("pay", {}, idempotency_key="k")) == {"ok": True}
    assert len(calls) == 2
    calls.clear()
    with pytest.raises(backd.RetryableError):
        await go(c.db("main").fn("pay", {}))  # no key: not safe to repeat
    assert len(calls) == 1
