"""Functions against a real backd and executor: sync calls, async jobs and a function's own error."""

from __future__ import annotations

import pytest

import backd

from .conftest import Users, go


async def test_echo_doubles_echo_async_waits_a_thrown_error_arrives(users: Users) -> None:
    client, _ = await users.sign_up("fn")
    db = client.db("app")

    out = await go(db.fn("echo", {"n": 21}))
    assert out["doubled"] == 42

    job = await go(db.fn("echo_async", {"n": 7}, respond_async=True))
    assert isinstance(job, (backd.Job, backd.AsyncJob))
    done = await go(job.wait(timeout=30))
    assert done["doubled"] == 14

    # The function's own error is a client error of the right kind.
    with pytest.raises(backd.ConflictError) as ei:
        await go(db.fn("echo", {"fail": True}))
    assert (ei.value.code, ei.value.status) == ("out_of_stock", 409)
