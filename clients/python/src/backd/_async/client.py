"""The client. This file is the source of ``_sync/client.py`` (``scripts/unasync.py``): write it async."""

from __future__ import annotations

import json
import logging
import time
from collections.abc import Awaitable, Callable, Mapping
from types import TracebackType
from typing import Any

import httpx

from .._aio import sleep
from .._core import Request, Response, endpoint, is_safe, normalize_org, session_gone
from .._retry import Retry, backoff
from ..errors import BackdError, NetworkError, RetryableError, error_from_response
from ..oauth import MemoryOAuthStorage, OAuthStorage
from ..storage import MemoryStorage, SlotStorage, TokenStorage
from .auth import AsyncAuth
from .data import AsyncDatabase

__all__ = ["AsyncClient"]

_log = logging.getLogger("backd")


class AsyncClient:
    """Talks to one realm of a backd server.

    ``url`` and ``realm`` are required. Use it as a context manager (``async with``), or call ``aclose()``.

    :param api_key: a server-side credential (``bdk_…``) with full access to the realm. Keep it out of
        anything a user's device can read.
    :param org: bind the client to an organization, by id or by the slug people know.
    :param storage: where the session token lives; memory by default.
    :param oauth_storage: where the sign-in with a provider keeps its PKCE verifier between its start and
        its end; memory by default.
    :param retry: retries for 429 and 503; off by default.
    :param headers: sent with every call.
    :param transport: an ``httpx`` transport, for tests (``httpx.MockTransport``) and unusual deployments.
    :param timeout: seconds before a call without progress fails; ``None`` waits for ever.
    :param types: the fingerprint of the generated types the program was written against (the
        ``fingerprint`` of ``realm.json``). It is sent as ``X-Backd-Types``; when the server's config has
        moved on it answers ``X-Backd-Types-Stale`` and ``on_stale_types`` is called, once. Leave it out in
        production: nothing is sent then.
    :param on_stale_types: called once when the types are stale; a warning is logged by default.
    """

    def __init__(
        self,
        *,
        url: str,
        realm: str,
        api_key: str | None = None,
        org: str | None = None,
        storage: TokenStorage | None = None,
        oauth_storage: OAuthStorage | None = None,
        retry: Retry | None = None,
        headers: Mapping[str, str] | None = None,
        transport: httpx.AsyncBaseTransport | None = None,
        timeout: float | None = 30.0,
        types: str | None = None,
        on_stale_types: Callable[[], None] | None = None,
        _root: AsyncClient | None = None,
    ) -> None:
        if not url:
            raise TypeError("Client: `url` is required")
        if not realm:
            raise TypeError("Client: `realm` is required")
        self._opts: dict[str, Any] = {
            "url": url,
            "realm": realm,
            "api_key": api_key,
            "org": org,
            "storage": storage,
            "oauth_storage": oauth_storage,
            "retry": retry,
            "headers": headers,
            "transport": transport,
            "timeout": timeout,
            "types": types,
            "on_stale_types": on_stale_types,
        }
        self.url = url.rstrip("/")
        self.realm = realm
        #: The organization the client is bound to, as it was given, or "".
        self.org_ref = normalize_org(org)
        self._api_key = api_key
        self.storage: TokenStorage = storage if storage is not None else MemoryStorage()
        self._retry = retry or Retry()
        self._headers = dict(headers or {})
        self._types = types
        self._on_stale_types = on_stale_types
        self._stale_reported = False
        #: Called when the server refuses the session token.
        self._expired: Callable[[], Awaitable[None]] | None = None
        self._closers: list[Callable[[], None]] = []
        self._closed = False
        self._root = _root
        self._org_clients: dict[str, AsyncClient] = {}
        self._credential_hooks: list[Callable[[], None]] = []
        #: The fresh proof of identity ``auth.reauth`` gave: the token and when it stops being good (epoch seconds).
        self._proof: tuple[str, float] | None = None
        self.oauth_storage: OAuthStorage = oauth_storage if oauth_storage is not None else MemoryOAuthStorage()
        self._http: httpx.AsyncClient = (
            _root._http if _root is not None else httpx.AsyncClient(transport=transport, timeout=timeout)
        )
        #: Sign-up, login and sessions.
        self.auth = AsyncAuth(self)
        self._expired = self.auth._expired

    def db(self, name: str) -> AsyncDatabase:
        """A database of the realm, to reach its collections: ``client.db("main").collection("posts")``."""
        return AsyncDatabase(self, name)

    @property
    def has_api_key(self) -> bool:
        """Whether the client was created with an API key."""
        return bool(self._api_key)

    @property
    def root(self) -> AsyncClient:
        """The client this one was made from by :meth:`org`, or this client."""
        return self._root or self

    def org(self, ref: str) -> AsyncClient:
        """The client of one organization, for a program that is signed in to several at once: it has the
        same address, realm and settings and a token of its own, so its calls reach that organization and no
        other, and signing out of it leaves the others signed in. The organization is its id or the slug
        people know; asking again for the same gives the same client. It needs a storage that can keep a
        token per organization (:class:`MemoryStorage` and :class:`FileStorage` can)."""
        key = normalize_org(ref)
        if not key:
            raise TypeError("client.org: the organization (an id or a slug) is required")
        root = self.root
        if root.org_ref == key:
            return root
        found = root._org_clients.get(key)
        if found is None:
            if not isinstance(root.storage, SlotStorage):
                raise TypeError(
                    "client.org: this storage cannot keep a token per organization; use MemoryStorage or FileStorage"
                )
            opts = {k: v for k, v in root._opts.items() if k not in ("org", "storage")}
            found = root._org_clients[key] = AsyncClient(**opts, org=key, storage=root.storage.slot(key), _root=root)
        return found

    def on_credential_change(self, fn: Callable[[], None]) -> None:
        """Registers ``fn`` to run when the signed-in caller changes (a sign-in, a sign-out, an expired
        session): whatever was read as the previous caller is not the new one's. The caches use it."""
        self._credential_hooks.append(fn)

    def _credentials_changed(self) -> None:
        for fn in list(self._credential_hooks):
            fn()

    def on_close(self, fn: Callable[[], None]) -> None:
        """Registers ``fn`` to run when the client is closed; live views use it to end."""
        root = self.root
        if root._closed:
            fn()
        else:
            root._closers.append(fn)

    async def aclose(self) -> None:
        """Ends what the client keeps running and releases its connections. Safe to call twice. On the client
        of an organization it closes the client it was made from."""
        root = self.root
        if root._closed:
            return
        root._closed = True
        closers, root._closers = root._closers, []
        for fn in closers:
            fn()
        await root._http.aclose()

    async def __aenter__(self) -> AsyncClient:
        return self

    async def __aexit__(
        self, exc_type: type[BaseException] | None, exc: BaseException | None, tb: TracebackType | None
    ) -> None:
        await self.aclose()

    async def _credentials(self) -> tuple[str | None, bool]:
        """The ``Authorization`` value (``None`` for none) and whether a session token was part of it."""
        if self._api_key:
            return "Bearer " + self._api_key, False
        token = self.storage.get()
        if token:
            return "Bearer " + token, True
        return None, False

    async def request(
        self,
        method: str,
        path: list[str],
        *,
        query: Mapping[str, Any] | None = None,
        body: Any = None,
        content: bytes | None = None,
        content_type: str | None = None,
        headers: Mapping[str, str] | None = None,
        raw: bool = False,
        auth: bool = True,
        expire: bool = True,
        reauth: bool = False,
        cache: bool = True,
        retry: Retry | None = None,
    ) -> Response:
        """Sends one call to ``/v1/{realm}/{path…}`` and returns the answer: the extension point the
        resources build on. It adds the credentials, applies retries, maps failures to
        :class:`BackdError` and reports stale types. ``body`` is sent as JSON; ``content`` as it is."""
        return await self._send(
            Request(
                method=method,
                path=path,
                query=query,
                body=body,
                content=content,
                content_type=content_type,
                headers=headers or {},
                raw=raw,
                auth=auth,
                expire=expire,
                reauth=reauth,
                cache=cache,
                retry=retry,
            )
        )

    async def _send(self, req: Request) -> Response:
        url = endpoint(self.url, self.realm, req.path, req.query)
        headers: dict[str, str] = {"Accept": "*/*" if req.raw else "application/json", **self._headers, **req.headers}
        payload: bytes | None = None
        if req.content is not None:
            payload = req.content
            headers["Content-Type"] = req.content_type or "application/octet-stream"
        elif req.body is not None:
            payload = _json(req.body)
            headers["Content-Type"] = req.content_type or "application/json"
        if self._types:
            headers["X-Backd-Types"] = self._types
        if req.reauth and self._proof is not None and self._proof[1] > time.time():
            headers["X-Backd-Reauth"] = self._proof[0]
        sent_session = False
        if req.auth:
            value, sent_session = await self._credentials()
            if value:
                headers["Authorization"] = value

        retry = req.retry or self._retry
        attempts = max(0, retry.attempts) if is_safe(req.method, headers) else 0
        attempt = 0
        while True:
            try:
                http_req = self._http.build_request(req.method, url, headers=headers, content=payload)
                res = await self._http.send(http_req, stream=True)
            except httpx.TimeoutException as e:
                raise _network("timeout", "request timed out") from e
            except httpx.HTTPError as e:
                raise _network("network_error", f"network error: {e}") from e
            if self._types and not self._stale_reported and res.headers.get("X-Backd-Types-Stale") == "true":
                self._stale_reported = True
                if self._on_stale_types:
                    self._on_stale_types()
                else:
                    _log.warning(
                        "backd: the generated types are out of date with the server: "
                        "run `backd types` and commit the result"
                    )
            if res.is_success:
                if req.raw:
                    return Response(res.status_code, res.headers, response=res)
                data = await _read_json(res)
                return Response(res.status_code, res.headers, data=data)
            await res.aread()
            await res.aclose()
            err = error_from_response(res.status_code, {k.lower(): v for k, v in res.headers.items()}, res.content)
            err.path = "/".join(req.path)
            if session_gone(err.code) and sent_session and req.expire and self._expired is not None:
                await self._expired()
            if isinstance(err, RetryableError) and attempt < attempts:
                await sleep(backoff(attempt, err.retry_after, retry.max_delay))
                attempt += 1
                continue
            raise err


def _network(code: str, message: str) -> NetworkError:
    return NetworkError(message, code=code)


def _json(body: Any) -> bytes:
    return json.dumps(body, separators=(",", ":"), ensure_ascii=False).encode()


async def _read_json(res: httpx.Response) -> Any:
    await res.aread()
    await res.aclose()
    if not res.content:
        return None
    try:
        return json.loads(res.content)
    except ValueError as e:
        raise BackdError(
            "the answer is not the JSON expected: " + str(e), status=res.status_code, code="invalid_response"
        ) from e
