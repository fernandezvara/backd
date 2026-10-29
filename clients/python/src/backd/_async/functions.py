"""Calling functions and following their jobs."""

from __future__ import annotations

import time
from collections.abc import Callable
from typing import TYPE_CHECKING, Any, cast

from .._aio import sleep
from ..errors import BackdError, JobTimeoutError, error_class_for
from ..models import JobData, JobStep

if TYPE_CHECKING:
    from .client import AsyncClient

__all__ = ["AsyncFunctions", "AsyncJob", "call_function"]


class AsyncJob:
    """A handle to an ``async`` function's job: ``db.fn(name, input)`` returns one instead of the output directly
    when the function is ``async``. Poll with :meth:`status`, or use :meth:`wait` to get the output the same way a
    ``sync`` call's return value works."""

    def __init__(self, client: AsyncClient, database: str, data: JobData) -> None:
        self._client = client
        self._database = database
        self.id = data["id"]
        #: The function this job runs, as ``<database>/<name>``.
        self.function = data["function"]
        self._data = data

    @property
    def raw(self) -> JobData:
        """The job's data as of the last ``status()`` or ``wait()`` call, or when it was created."""
        return self._data

    @property
    def steps(self) -> list[JobStep]:
        """The steps the function reported, as of the last poll (empty before one, or when it reports none): the
        last is the current step."""
        return self._data.get("steps", [])

    async def status(self) -> str:
        """Polls the job's current status: ``queued``, ``running`` or ``done``."""
        self._data = await self._fetch()
        return self._data["status"]

    async def wait(
        self,
        *,
        poll_interval: float = 0.5,
        timeout: float | None = None,
        on_progress: Callable[[list[JobStep], AsyncJob], None] | None = None,
    ) -> Any:
        """Polls until the job is done, then returns its output (the same value a ``sync`` call would have
        returned) or raises a :class:`BackdError` matching what a ``sync`` call would have raised for the same
        outcome (the function's own ``code``, when it raised ``ctx.error(...)``). ``poll_interval`` is in seconds
        (0.5 by default). Without ``timeout`` it waits indefinitely; with it, :class:`JobTimeoutError` when the job
        has not finished in that time: the job itself is unaffected, wait again whenever you like. ``on_progress``
        is called after every poll with the steps reported so far."""
        deadline = None if timeout is None else time.monotonic() + timeout
        while True:
            self._data = await self._fetch()
            if on_progress is not None:
                on_progress(self.steps, self)
            if self._data["status"] == "done":
                return _output_or_raise(self._data.get("result"), self.id)
            if deadline is not None and time.monotonic() >= deadline:
                raise JobTimeoutError(self.id)
            await sleep(poll_interval)

    async def _fetch(self) -> JobData:
        res = await self._client.request("GET", [self._database, "_jobs", self.id])
        return cast(JobData, res.data)


def _output_or_raise(result: Any, job_id: str) -> Any:
    if not result:
        raise BackdError(f"job {job_id} is done but has no result", code="invalid_response")
    if result["status"] == "ok":
        return result.get("output")
    status = result.get("http_status") or 500
    raise error_class_for(status)(
        result.get("message") or "the function failed",
        status=status,
        code=result.get("code") or "function_failed",
        details=result.get("details") or [],
    )


async def call_function(
    client: AsyncClient,
    database: str,
    name: str,
    input: Any = None,
    *,
    idempotency_key: str | None = None,
    respond_async: bool = False,
) -> Any:
    headers: dict[str, str] = {}
    if idempotency_key is not None:
        headers["Idempotency-Key"] = idempotency_key
    if respond_async:
        headers["Prefer"] = "respond-async"
    path = [database, "_func", name]
    if input is None:  # nothing to send is JSON null, not an empty body
        res = await client.request("POST", path, content=b"null", content_type="application/json", headers=headers)
    else:
        res = await client.request("POST", path, body=input, headers=headers)
    if res.status == 202:
        return AsyncJob(client, database, cast(JobData, res.data))
    return res.data


class AsyncFunctions:
    """Calls functions by their full name: ``client.functions.call("main/refund", input)``."""

    def __init__(self, client: AsyncClient) -> None:
        self._client = client

    async def call(
        self,
        name: str,
        input: Any = None,
        *,
        idempotency_key: str | None = None,
        respond_async: bool = False,
    ) -> Any:
        """Calls the function ``database/name``. Returns its output directly for a ``sync`` function; for ``async``,
        a :class:`AsyncJob` handle to poll instead of the output (``job.status()``, or ``job.wait()`` for the
        output). A function's own error (``ctx.error(status, code, message)``) arrives as a :class:`BackdError` with
        that status and code (for ``async``, only once ``job.wait()`` returns, not from this call itself).

        ``respond_async=True`` (``Prefer: respond-async``) queues a ``sync`` function as a job too, and returns its
        job instead of the output: for calls that can take long, or that nobody waits for. It runs once, with the
        function's own limits. With an ``idempotency_key`` a repeated request returns the first one's answer.
        ``webhook`` functions can't be called through this method: their caller is whatever service sends the
        webhook, never this client."""
        database, sep, fn = name.partition("/")
        if not sep or not database or not fn or "/" in fn:
            raise TypeError(f'functions.call: the name is "database/function", got {name!r}')
        return await call_function(
            self._client, database, fn, input, idempotency_key=idempotency_key, respond_async=respond_async
        )

    async def run(
        self,
        name: str,
        input: Any = None,
        *,
        poll_interval: float = 0.5,
        timeout: float | None = None,
        idempotency_key: str | None = None,
    ) -> Any:
        """Calls the function and returns its output whether it is ``sync`` or ``async``: a job is waited for."""
        out = await self.call(name, input, idempotency_key=idempotency_key)
        if isinstance(out, AsyncJob):
            return await out.wait(poll_interval=poll_interval, timeout=timeout)
        return out
