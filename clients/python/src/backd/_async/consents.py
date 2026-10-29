"""The consents the realm asks for: ``client.consents``."""

from __future__ import annotations

from collections.abc import Mapping
from typing import TYPE_CHECKING, Any, cast

from ..models import ConsentInfo, ConsentState, ConsentText

if TYPE_CHECKING:
    from .client import AsyncClient

__all__ = ["AsyncConsents"]


class AsyncConsents:
    """Consents the realm asks for: ``client.consents``. The texts are public (a person reads them before having an
    account); the rest needs a session. See ``client.auth.signup`` and ``client.auth.accept_consents`` for accepting at
    sign-up and at sign-in."""

    def __init__(self, client: AsyncClient) -> None:
        self._client = client

    async def current(self) -> list[ConsentInfo]:
        """The consents the realm asks for and the version in force of each."""
        res = await self._client.request("GET", ["_consents"], auth=False)
        return cast("list[ConsentInfo]", cast(dict[str, Any], res.data)["items"])

    async def text(self, name: str, version: str, locale: str | None = None) -> ConsentText:
        """The exact text of a version, in the best of the realm's languages for ``locale``."""
        res = await self._client.request("GET", ["_consents", name, version], query={"locale": locale}, auth=False)
        return cast(ConsentText, res.data)

    async def state(self) -> ConsentState:
        """What the signed-in user holds, what they still have to accept and their history."""
        return cast(ConsentState, (await self._client.request("GET", ["_auth", "consents"])).data)

    async def accept(self, consents: Mapping[str, str]) -> ConsentState:
        """The signed-in user accepts consents: ``{"terms": "2026-10", "marketing": "1"}`` (name to the version
        accepted). It works for a user who is refused with ``consent_required`` because a ``reaccept_by`` date
        passed, which is how they get back in."""
        res = await self._client.request("POST", ["_auth", "consents"], body={"consents": consents})
        return cast(ConsentState, res.data)

    async def withdraw(self, name: str) -> ConsentState:
        """Withdraws an optional consent. A required one is a :class:`ConflictError` with the code
        ``consent_required_for_account``: the way out is deleting the account."""
        return cast(ConsentState, (await self._client.request("DELETE", ["_auth", "consents", name])).data)
