"""Sign-in with a provider by redirect: the PKCE pair, what is kept between the start and the end,
and what backd puts on the address it sends the person back to."""

from __future__ import annotations

import base64
import hashlib
import json
import secrets
import threading
import time
from dataclasses import asdict, dataclass
from typing import Protocol
from urllib.parse import parse_qs, urlsplit

from .errors import BackdError, NotAllowedError, error_class_for

__all__ = ["MemoryOAuthStorage", "OAuthReturn", "OAuthStorage", "challenge_of", "new_pkce", "parse_return"]

#: What backd adds to the address is prefixed, so it can't collide with the app's own parameters.
PARAM_PREFIX = "backd_"
PENDING_LIFE = 10 * 60  # seconds: the server's state lives ten minutes


class OAuthStorage(Protocol):
    """Keeps what a redirect sign-in needs between its start and its end: the PKCE verifier and where
    the sign-in began. A program that starts a sign-in and finishes it in the same process keeps it in
    memory; one that is restarted in between keeps it somewhere durable."""

    def get(self) -> str | None: ...
    def set(self, value: str) -> None: ...
    def remove(self) -> None: ...


class MemoryOAuthStorage:
    """Keeps the pending sign-in in memory; the default."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._held: str | None = None

    def get(self) -> str | None:
        with self._lock:
            return self._held

    def set(self, value: str) -> None:
        with self._lock:
            self._held = value

    def remove(self) -> None:
        with self._lock:
            self._held = None


def _b64url(raw: bytes) -> str:
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode()


def challenge_of(verifier: str) -> str:
    """The S256 challenge of a PKCE verifier."""
    return _b64url(hashlib.sha256(verifier.encode()).digest())


def new_pkce() -> tuple[str, str]:
    """A PKCE verifier (256 random bits) and its S256 challenge."""
    verifier = _b64url(secrets.token_bytes(32))
    return verifier, challenge_of(verifier)


@dataclass
class Pending:
    """The attempt this process started."""

    verifier: str
    provider: str
    intent: str
    request_id: str | None = None
    at: float = 0.0


def save_pending(storage: OAuthStorage, p: Pending) -> None:
    p.at = time.time()
    storage.set(json.dumps({k: v for k, v in asdict(p).items() if v is not None}))


def load_pending(storage: OAuthStorage) -> Pending | None:
    """The attempt this process started, or ``None`` when none is waiting (or it is too old to work)."""
    raw = storage.get()
    if not raw:
        return None
    try:
        d = json.loads(raw)
        p = Pending(
            d["verifier"], d.get("provider", ""), d.get("intent", "signin"), d.get("request_id"), float(d["at"])
        )
    except (ValueError, KeyError, TypeError):
        return None  # unreadable: as if there were none
    return p if time.time() - p.at < PENDING_LIFE else None


@dataclass
class OAuthReturn:
    """What backd put on the address it sent the person back to."""

    #: A one-time login code.
    code: str = ""
    #: Why the sign-in didn't work.
    error: str = ""
    #: The provider that was linked to the signed-in user.
    linked: str = ""
    #: With ``account_exists``: the provider that was tried.
    provider: str = ""
    #: With the code: the organization the session will be scoped to.
    org: str = ""


def parse_return(uri: str | None) -> OAuthReturn | None:
    """Reads the outcome of the redirect flow from the address the person returned to; ``None`` when it
    carries none (an ordinary visit)."""
    if not uri:
        return None
    try:
        q = parse_qs(urlsplit(uri).query)
    except ValueError:
        return None

    def get(k: str) -> str:
        v = q.get(PARAM_PREFIX + k)
        return v[0] if v else ""

    out = OAuthReturn(get("code"), get("error"), get("linked"), get("provider"), get("org"))
    return out if out.code or out.error or out.linked else None


_ERRORS: dict[str, tuple[int, str]] = {
    "cancelled": (401, "the sign-in was cancelled at the provider"),
    "account_exists": (
        409,
        "an account with that address exists: sign in another way and link this provider from there",
    ),
    "signup_closed": (403, "sign-up is not open in this realm"),
    "invitation_invalid": (400, "sign-up needs a valid invitation"),
    "link_conflict": (409, "that provider account can not be linked to this user"),
    "email_not_verified": (403, "the address of this account is not verified"),
    "email_required": (400, "the provider gave no email address"),
    "signin_refused": (403, "this account can not sign in"),
    "not_allowed": (403, "this account is not allowed to sign in through this organization"),
    "provider_error": (502, "the provider failed or answered something backd can not accept"),
}


def oauth_error(code: str, provider: str | None = None) -> BackdError:
    """The error for a ``backd_error`` code of the redirect flow, as the classes the rest of the client
    raises: ``account_exists`` and ``link_conflict`` are a ``ConflictError``, ``signup_closed``,
    ``email_not_verified`` and ``signin_refused`` a ``ForbiddenError``, ``not_allowed`` a
    ``NotAllowedError``, ``cancelled`` an ``AuthenticationError``; ``code`` is the error code."""
    if code not in _ERRORS:
        code = "provider_error"
    status, message = _ERRORS[code]
    cls = NotAllowedError if code == "not_allowed" else error_class_for(status)
    return cls(
        message, status=status, code=code, details=[{"path": "provider", "reason": provider}] if provider else []
    )
