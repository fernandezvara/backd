"""Auth against a real backd: sign up, read the account, sign out, sign back in."""

from __future__ import annotations

import pytest

import backd

from .conftest import PASSWORD, Users, go


async def test_signup_me_logout_login(users: Users) -> None:
    client, address = await users.sign_up("ada")
    me = await go(client.auth.me())
    assert me["email"] == address

    again = users.anonymous()  # a second client logs in with the same account
    await go(again.auth.login(address, PASSWORD))
    assert (await go(again.auth.me()))["email"] == address
    assert len(await go(again.auth.sessions())) >= 2

    await go(client.auth.logout())
    with pytest.raises(backd.AuthenticationError):
        await go(client.auth.me())


async def test_a_wrong_password_is_refused(users: Users) -> None:
    _, address = await users.sign_up("bob")
    other = users.anonymous()
    with pytest.raises(backd.AuthenticationError):
        await go(other.auth.login(address, "not-the-password"))
