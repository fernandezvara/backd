"""Errors. Every failure of a call is a :class:`BackdError`; the subclasses follow the API's error
codes, with the names ``backd-js`` uses, so ``except ConflictError`` also catches ``IdTakenError``."""

from __future__ import annotations

import json
import re
import time
from email.utils import parsedate_to_datetime
from typing import Any, TypedDict

__all__ = [
    "AggregateNotEnabledError",
    "AggregateTooExpensiveError",
    "AuthenticationError",
    "BackdError",
    "ConflictError",
    "ConsentRequiredError",
    "ErrorDetail",
    "ExpandTooLargeError",
    "FieldEncryptedError",
    "ForbiddenError",
    "IdTakenError",
    "IndexBuildingError",
    "InvalidAggregateError",
    "InvalidExpandError",
    "InvalidIdError",
    "InvalidResumeTokenError",
    "JobTimeoutError",
    "LastOrgManagerError",
    "NetworkError",
    "NotAllowedError",
    "NotFoundError",
    "NotOrgMemberError",
    "NotTeamMemberError",
    "OrgRequiresMFAError",
    "OrgRoleRequiresMFAError",
    "PasswordExpiredError",
    "RealtimeNotEnabledError",
    "RetryableError",
    "SearchNotEnabledError",
    "SearchTooExpensiveError",
    "SecondStepRequiredError",
    "SessionEndedError",
    "SsoRequiredError",
    "SsoSessionError",
    "TeamError",
    "TooManySubscriptionsError",
    "ValidationError",
    "VerificationRequiredError",
    "VersionMismatchError",
    "error_class_for",
    "error_from_response",
    "parse_retry_after",
]


class _Detail(TypedDict):
    path: str
    reason: str


class ErrorDetail(_Detail, total=False):
    """One problem inside an error, such as a field that failed validation. ``code`` names a
    problem that has a code of its own, such as ``field_encrypted``."""

    code: str


class BackdError(Exception):
    """An error answered by backd, or a network failure (``status`` 0).

    ``code`` is stable and meant for programs; ``message`` is for people.
    """

    def __init__(
        self,
        message: str,
        *,
        status: int = 0,
        code: str = "",
        details: list[ErrorDetail] | None = None,
        request_id: str | None = None,
        headers: dict[str, str] | None = None,
    ) -> None:
        super().__init__(message)
        self.message = message
        self.status = status
        self.code = code
        self.details: list[ErrorDetail] = details or []
        self.request_id = request_id
        #: Response headers of the failed call (empty when there was no answer).
        self.headers: dict[str, str] = headers or {}
        #: The path after ``/v1/{realm}`` of the call that failed, such as ``_admin/jobs``.
        self.path = ""

    def __str__(self) -> str:
        return self.message


class ValidationError(BackdError):
    """400: invalid body, query or header."""


class AuthenticationError(BackdError):
    """401: missing, invalid or expired credentials, or a wrong email or password."""


class RealtimeNotEnabledError(ValidationError):
    """400 ``realtime_not_enabled``: the collection doesn't declare ``realtime: true``."""


class TooManySubscriptionsError(ValidationError):
    """400 ``too_many_subscriptions``: a stream may hold only so many subscriptions."""


class InvalidResumeTokenError(ValidationError):
    """400 ``invalid_resume_token``: a resume token is not valid for this caller."""


class PasswordExpiredError(AuthenticationError):
    """401 ``password_expired``: the password is right but an administrator expired it.

    ``reset_email_queued`` says backd queued the email with the link to choose a new one.
    """

    def __init__(self, message: str, **kw: Any) -> None:
        super().__init__(message, **kw)
        self.reset_email_queued = any(d["path"] == "reset_email" for d in self.details)


class SessionEndedError(AuthenticationError):
    """The stream's session, user or key is gone. Sign in again and subscribe again."""


class ForbiddenError(BackdError):
    """403: the caller isn't allowed to do this."""


class VerificationRequiredError(BackdError):
    """Not an HTTP error: a sign-up in a realm that requires verified addresses answers ``202``
    (the account exists but there is no session)."""


class SecondStepRequiredError(BackdError):
    """Not an HTTP error: a password login that needs a second step before it gives a session.

    ``code`` is ``mfa_required`` (finish with ``auth.verify_mfa``; ``methods`` says which),
    ``mfa_enrollment_required`` or ``consent_required`` (accept ``consents``).
    """

    def __init__(
        self,
        message: str,
        *,
        challenge: str,
        methods: list[str] | None = None,
        consents: list[dict[str, str]] | None = None,
        **kw: Any,
    ) -> None:
        super().__init__(message, **kw)
        self.challenge = challenge
        self.methods = methods or []
        self.consents = consents or []


_CONSENT_NAME = re.compile(r"^consents\.(.+)$")
_CONSENT_VERSION = re.compile(r"version (\S+)$")


class ConsentRequiredError(BackdError):
    """``consent_required``: consents have to be accepted; ``consents`` lists ``{name, version}``."""

    def __init__(self, message: str, **kw: Any) -> None:
        super().__init__(message, **kw)
        self.consents: list[dict[str, str]] = []
        for d in self.details:
            name, version = _CONSENT_NAME.match(d["path"]), _CONSENT_VERSION.search(d["reason"])
            if name and version:
                self.consents.append({"name": name.group(1), "version": version.group(1)})


class InvalidIdError(ValidationError):
    """400 ``id_required``, ``invalid_id`` or ``id_not_allowed`` (``code`` says which)."""


class OrgRequiresMFAError(ForbiddenError):
    """403 ``org_requires_mfa``: the organization requires a second factor and this session used none."""


class OrgRoleRequiresMFAError(ForbiddenError):
    """403 ``org_role_requires_mfa``: the action needs a role that requires a second factor."""


class NotTeamMemberError(ForbiddenError):
    """403 ``not_team_member``: the create names a team the caller doesn't belong to."""


class TeamError(ValidationError):
    """400 ``team_required`` or ``team_not_allowed``."""


class InvalidExpandError(ValidationError):
    """400 ``invalid_expand``: the ``expand`` of a read isn't acceptable; the details name the part."""


class ExpandTooLargeError(ValidationError):
    """400 ``expand_too_large``: the expansion would return more documents than the server allows."""


class InvalidAggregateError(ValidationError):
    """400 ``invalid_aggregate``: an aggregation names something the collection doesn't declare."""


class FieldEncryptedError(ValidationError):
    """400 ``field_encrypted``: a ``where`` or ``order_by`` asks more of an encrypted field than equality."""


class AggregateNotEnabledError(ValidationError):
    """400 ``aggregate_not_enabled``: the collection declares no ``aggregate:``."""


class AggregateTooExpensiveError(BackdError):
    """422 ``aggregate_too_expensive``: narrow ``where`` or group by fewer fields."""


class SearchNotEnabledError(ValidationError):
    """400 ``search_not_enabled``: a ``$search`` in a collection that declares no text index."""


class NotAllowedError(ForbiddenError):
    """403 ``not_allowed``: the person is not of the organization the connection admits."""


class SsoRequiredError(ValidationError):
    """400 ``sso_required``: the organization's people sign in through its connection only."""


class SsoSessionError(ForbiddenError):
    """403 ``sso_session``: a session opened through an organization's connection can't do this."""


class NotOrgMemberError(ForbiddenError):
    """403 ``not_org_member``: not a member of the organization, or there is no such organization."""


class NotFoundError(BackdError):
    """404: unknown realm, collection, document, user or session."""


class ConflictError(BackdError):
    """409: a unique value already exists, or concurrent writes."""


class IdTakenError(ConflictError):
    """409 ``id_taken``: a document with the id of a create exists. Nothing was overwritten."""


class LastOrgManagerError(ConflictError):
    """409 ``last_org_manager``: the organization needs at least one member who can manage it."""


class VersionMismatchError(BackdError):
    """412: ``If-Match`` didn't match the current version."""


class RetryableError(BackdError):
    """429 and 503: try again later; ``retry_after`` is in seconds when the server said."""

    def __init__(self, message: str, *, retry_after: float | None = None, **kw: Any) -> None:
        super().__init__(message, **kw)
        self.retry_after = retry_after


class SearchTooExpensiveError(BackdError):
    """422 ``search_too_expensive``: use fewer or rarer words, or narrow ``where``."""


class IndexBuildingError(RetryableError):
    """503 ``index_building``: a search while the text index is being rebuilt; the client retries it."""


class NetworkError(BackdError):
    """The call didn't get an answer (network failure, abort, timeout). ``__cause__`` has the reason."""


class JobTimeoutError(BackdError):
    """A job did not finish within the time given to ``Job.wait``."""


_BY_STATUS: dict[int, type[BackdError]] = {
    400: ValidationError,
    401: AuthenticationError,
    403: ForbiddenError,
    404: NotFoundError,
    409: ConflictError,
    412: VersionMismatchError,
    429: RetryableError,
    503: RetryableError,
}

_BY_CODE: dict[str, type[BackdError]] = {
    "consent_required": ConsentRequiredError,
    "password_expired": PasswordExpiredError,
    "realtime_not_enabled": RealtimeNotEnabledError,
    "too_many_subscriptions": TooManySubscriptionsError,
    "invalid_resume_token": InvalidResumeTokenError,
    "id_required": InvalidIdError,
    "invalid_id": InvalidIdError,
    "id_not_allowed": InvalidIdError,
    "id_taken": IdTakenError,
    "org_requires_mfa": OrgRequiresMFAError,
    "org_role_requires_mfa": OrgRoleRequiresMFAError,
    "not_team_member": NotTeamMemberError,
    "team_required": TeamError,
    "team_not_allowed": TeamError,
    "not_org_member": NotOrgMemberError,
    "not_allowed": NotAllowedError,
    "sso_required": SsoRequiredError,
    "sso_session": SsoSessionError,
    "last_org_manager": LastOrgManagerError,
    "invalid_aggregate": InvalidAggregateError,
    "aggregate_not_enabled": AggregateNotEnabledError,
    "aggregate_too_expensive": AggregateTooExpensiveError,
    "search_not_enabled": SearchNotEnabledError,
    "search_too_expensive": SearchTooExpensiveError,
    "index_building": IndexBuildingError,
    "invalid_expand": InvalidExpandError,
    "field_encrypted": FieldEncryptedError,
    "expand_too_large": ExpandTooLargeError,
}


def error_class_for(status: int) -> type[BackdError]:
    """The error class for an HTTP status, or :class:`BackdError` when none is specific to it."""
    return _BY_STATUS.get(status, BackdError)


def parse_retry_after(value: str | None, now: float | None = None) -> float | None:
    """Reads a ``Retry-After`` header (seconds or an HTTP date) as seconds."""
    if not value:
        return None
    value = value.strip()
    if value.isdigit():
        return float(value)
    try:
        at = parsedate_to_datetime(value).timestamp()
    except (TypeError, ValueError):
        return None
    return max(0.0, at - (time.time() if now is None else now))


def error_from_response(status: int, headers: dict[str, str], body: bytes) -> BackdError:
    """Builds the error of a non-2xx answer. ``headers`` have lower-case names."""
    err: dict[str, Any] = {}
    try:
        parsed = json.loads(body)
        if isinstance(parsed, dict) and isinstance(parsed.get("error"), dict):
            err = parsed["error"]
    except ValueError:
        pass  # not JSON, for example from a proxy: keep the defaults below
    code = err.get("code") or f"http_{status}"
    cls = _BY_CODE.get(code) or error_class_for(status)
    message = err.get("message") or _reason(status)
    kw: dict[str, Any] = {
        "status": status,
        "code": code,
        "details": err.get("details") or [],
        "request_id": err.get("request_id") or headers.get("x-request-id"),
        "headers": headers,
    }
    if issubclass(cls, RetryableError):
        kw["retry_after"] = parse_retry_after(headers.get("retry-after"))
    return cls(message, **kw)


def _reason(status: int) -> str:
    from http import HTTPStatus

    try:
        return HTTPStatus(status).phrase
    except ValueError:
        return "request failed"
