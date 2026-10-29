import json

import pytest

import backd
from backd.errors import error_class_for, error_from_response, parse_retry_after


def err(status: int, code: str, message: str = "m", details: object = None, **headers: str) -> backd.BackdError:
    body = json.dumps({"error": {"code": code, "message": message, "details": details or [], "request_id": "r1"}})
    return error_from_response(status, headers, body.encode())


@pytest.mark.parametrize(
    ("status", "code", "cls", "parent"),
    [
        (400, "validation_error", backd.ValidationError, backd.BackdError),
        (401, "unauthenticated", backd.AuthenticationError, backd.BackdError),
        (403, "forbidden", backd.ForbiddenError, backd.BackdError),
        (404, "not_found", backd.NotFoundError, backd.BackdError),
        (409, "conflict", backd.ConflictError, backd.BackdError),
        (409, "id_taken", backd.IdTakenError, backd.ConflictError),
        (409, "last_org_manager", backd.LastOrgManagerError, backd.ConflictError),
        (412, "version_mismatch", backd.VersionMismatchError, backd.BackdError),
        (429, "rate_limited", backd.RetryableError, backd.BackdError),
        (503, "index_building", backd.IndexBuildingError, backd.RetryableError),
        (400, "invalid_id", backd.InvalidIdError, backd.ValidationError),
        (400, "id_required", backd.InvalidIdError, backd.ValidationError),
        (400, "team_required", backd.TeamError, backd.ValidationError),
        (400, "invalid_expand", backd.InvalidExpandError, backd.ValidationError),
        (400, "expand_too_large", backd.ExpandTooLargeError, backd.ValidationError),
        (400, "field_encrypted", backd.FieldEncryptedError, backd.ValidationError),
        (400, "sso_required", backd.SsoRequiredError, backd.ValidationError),
        (400, "realtime_not_enabled", backd.RealtimeNotEnabledError, backd.ValidationError),
        (403, "org_requires_mfa", backd.OrgRequiresMFAError, backd.ForbiddenError),
        (403, "not_org_member", backd.NotOrgMemberError, backd.ForbiddenError),
        (403, "sso_session", backd.SsoSessionError, backd.ForbiddenError),
        (422, "aggregate_too_expensive", backd.AggregateTooExpensiveError, backd.BackdError),
        (422, "search_too_expensive", backd.SearchTooExpensiveError, backd.BackdError),
    ],
)
def test_every_code_has_its_class(status: int, code: str, cls: type, parent: type) -> None:
    e = err(status, code)
    assert type(e) is cls
    assert isinstance(e, parent)
    assert (e.status, e.code, e.message, e.request_id) == (status, code, "m", "r1")


def test_unknown_code_falls_back_to_the_status() -> None:
    assert type(err(404, "something_new")) is backd.NotFoundError
    assert type(err(418, "teapot")) is backd.BackdError


def test_not_json_answer_keeps_the_defaults() -> None:
    e = error_from_response(502, {"x-request-id": "abc"}, b"<html>bad gateway</html>")
    assert (e.status, e.code, e.message, e.request_id) == (502, "http_502", "Bad Gateway", "abc")


def test_retry_after_in_seconds() -> None:
    e = err(429, "rate_limited", **{"retry-after": "3"})
    assert isinstance(e, backd.RetryableError)
    assert e.retry_after == 3.0
    assert parse_retry_after("Wed, 21 Oct 2015 07:28:00 GMT", now=1445412477.0) == 3.0
    assert parse_retry_after("garbage") is None
    assert parse_retry_after(None) is None


def test_consent_required_lists_what_to_accept() -> None:
    e = err(
        403,
        "consent_required",
        details=[{"path": "consents.terms", "reason": "accept version 2"}, {"path": "consents.x", "reason": "no"}],
    )
    assert isinstance(e, backd.ConsentRequiredError)
    assert e.consents == [{"name": "terms", "version": "2"}]


def test_password_expired_says_whether_the_email_was_queued() -> None:
    e = err(401, "password_expired", details=[{"path": "reset_email", "reason": "queued"}])
    assert isinstance(e, backd.PasswordExpiredError)
    assert isinstance(e, backd.AuthenticationError)
    assert e.reset_email_queued
    plain = err(401, "password_expired")
    assert isinstance(plain, backd.PasswordExpiredError)
    assert not plain.reset_email_queued


def test_error_class_for() -> None:
    assert error_class_for(412) is backd.VersionMismatchError
    assert error_class_for(500) is backd.BackdError
