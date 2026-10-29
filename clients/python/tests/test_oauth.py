import time

from backd.oauth import (
    MemoryOAuthStorage,
    Pending,
    challenge_of,
    load_pending,
    new_pkce,
    parse_return,
    save_pending,
)


def test_pkce_matches_the_rfc_7636_example() -> None:
    assert challenge_of("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk") == "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
    verifier, challenge = new_pkce()
    assert len(verifier) == 43 and challenge == challenge_of(verifier)
    assert new_pkce()[0] != verifier


def test_pending_expires_and_survives_garbage() -> None:
    s = MemoryOAuthStorage()
    assert load_pending(s) is None
    save_pending(s, Pending("v", "google", "signin"))
    p = load_pending(s)
    assert p is not None and (p.verifier, p.provider, p.intent) == ("v", "google", "signin")
    s.set("not json")
    assert load_pending(s) is None
    s.set('{"verifier": "v", "at": %f}' % (time.time() - 11 * 60))
    assert load_pending(s) is None
    s.remove()
    assert s.get() is None


def test_parse_return() -> None:
    assert parse_return(None) is None
    assert parse_return("http://x/cb") is None
    assert parse_return("http://x/cb?code=app-own") is None  # only backd_ parameters count
    r = parse_return("http://x/cb?backd_code=c&backd_org=acme&keep=1")
    assert r is not None and (r.code, r.org, r.error) == ("c", "acme", "")
    r = parse_return("http://x/cb?backd_error=account_exists&backd_provider=google")
    assert r is not None and (r.error, r.provider) == ("account_exists", "google")
