import json
from datetime import date, datetime, timezone

import pytest

import backd
from backd.query import bucket_key, csv, if_match_value, where_json


def test_operators_build_the_query_language() -> None:
    assert backd.eq(1) == {"$eq": 1}
    assert backd.in_("a", "b") == {"$in": ["a", "b"]}
    assert backd.nin(1) == {"$nin": [1]}
    assert backd.between(10, 20) == {"$between": [10, 20]}
    assert backd.is_null() == {"$isNull": True}
    assert backd.not_null() == {"$notNull": True}
    assert backd.icontains("go") == {"$icontains": "go"}
    assert backd.search('red "running shoes"') == {"$search": 'red "running shoes"'}
    w = backd.or_({"a": backd.gt(1)}, backd.and_({"b": backd.lte(2)}, {"c": backd.starts_with("x")}))
    assert w == {"$or": [{"a": {"$gt": 1}}, {"$and": [{"b": {"$lte": 2}}, {"c": {"$startsWith": "x"}}]}]}
    for op, key in [
        (backd.ne, "$ne"),
        (backd.gte, "$gte"),
        (backd.lt, "$lt"),
        (backd.like, "$like"),
        (backd.ilike, "$ilike"),
        (backd.istarts_with, "$istartsWith"),
        (backd.ends_with, "$endsWith"),
        (backd.iends_with, "$iendsWith"),
        (backd.contains, "$contains"),
    ]:
        assert op("v") == {key: "v"}


def test_where_json() -> None:
    assert where_json(None) is None
    assert where_json('{"a":1}') == '{"a":1}'
    assert where_json({"a": backd.gt(1)}) == '{"a":{"$gt":1}}'
    assert where_json({"at": datetime(2026, 1, 2, 3, 4, 5, tzinfo=timezone.utc), "d": date(2026, 1, 2)}) == (
        '{"at":"2026-01-02T03:04:05Z","d":"2026-01-02"}'
    )
    assert where_json({"é": "ü"}) == '{"é":"ü"}'
    with pytest.raises(TypeError, match="can't be encoded"):
        where_json({"a": object()})


def test_small_helpers() -> None:
    assert csv(None) is None and csv("a,b") == "a,b" and csv(["-a", "b"]) == "-a,b"
    assert if_match_value(3) == '"3"' and if_match_value("*") == "*" and if_match_value('"2", "3"') == '"2", "3"'


def test_expand_builder() -> None:
    assert backd.expand("tags") == "tags"
    assert backd.expand(("author", ["name", "avatar"]), "tags") == "author(name,avatar),tags"
    assert backd.expand("author.company") == "author.company"
    for bad in ["", "1x", "a b", "a(b)"]:
        with pytest.raises(TypeError, match="not a reference name"):
            backd.expand(bad)
    with pytest.raises(TypeError, match="non-empty list"):
        backd.expand(("author", []))
    with pytest.raises(TypeError, match="non-empty list"):
        backd.expand(("author", ["ok", "no good"]))


def test_bucket_keys() -> None:
    d = date(2026, 1, 1)  # a Thursday: ISO week 1
    assert bucket_key(d, "day") == "2026-01-01"
    assert bucket_key(d, "week") == "2026-W01"
    assert bucket_key(date(2027, 1, 1), "week") == "2026-W53"  # a Friday of the last week of 2026
    assert bucket_key(date(2024, 12, 30), "week") == "2025-W01"
    assert bucket_key(d, "month") == "2026-01"
    assert bucket_key(d, "year") == "2026"
    assert bucket_key(datetime(2026, 1, 1, 23, 30, tzinfo=timezone.utc), "day") == "2026-01-01"
    with pytest.raises(TypeError):
        bucket_key(d, "hour")  # type: ignore[arg-type]


def group(name: str, key: str, **m: object) -> dict[str, object]:
    return {"key": {name: key}, **m}


def test_fill_buckets_adds_the_empty_ones() -> None:
    name = "paid_at:month"
    groups = [group(name, "2026-01", count=2, sum_total="5"), group(name, "2026-03", count=1, sum_total="1")]
    out = backd.fill_buckets(
        groups, field="paid_at", unit="month", from_="2025-12", to="2026-04", empty={"sum_total": "0"}
    )
    assert [g["key"][name] for g in out] == ["2025-12", "2026-01", "2026-02", "2026-03", "2026-04"]
    assert out[1]["count"] == 2
    assert out[2] == {"key": {name: "2026-02"}, "count": 0, "sum_total": "0"}


def test_fill_buckets_keeps_the_null_group_first_and_takes_dates() -> None:
    name = "at:day"
    groups = [group(name, "2026-02-02", count=1), {"key": {name: None}, "count": 4}]
    out = backd.fill_buckets(
        groups, field="at", unit="day", from_=date(2026, 2, 1), to=datetime(2026, 2, 3, 12, tzinfo=timezone.utc)
    )
    assert [g["key"][name] for g in out] == [None, "2026-02-01", "2026-02-02", "2026-02-03"]


@pytest.mark.parametrize(
    ("unit", "start", "end", "want"),
    [
        ("week", "2025-W51", "2026-W02", ["2025-W51", "2025-W52", "2026-W01", "2026-W02"]),
        ("year", "2024", "2026", ["2024", "2025", "2026"]),
        ("month", "2025-11", "2026-02", ["2025-11", "2025-12", "2026-01", "2026-02"]),
        ("day", "2026-02-27", "2026-03-02", ["2026-02-27", "2026-02-28", "2026-03-01", "2026-03-02"]),
    ],
)
def test_fill_buckets_walks_every_unit(unit: str, start: str, end: str, want: list[str]) -> None:
    out = backd.fill_buckets([], field="f", unit=unit, from_=start, to=end)  # type: ignore[arg-type]
    assert [g["key"][f"f:{unit}"] for g in out] == want


def test_fill_buckets_checks_its_input() -> None:
    g = [{"key": {"a": 1, "b": 2}}]
    with pytest.raises(TypeError, match="grouped only by"):
        backd.fill_buckets(g, field="a", unit="day", from_="2026-01-01", to="2026-01-02")
    with pytest.raises(TypeError, match="is after"):
        backd.fill_buckets([], field="a", unit="day", from_="2026-01-02", to="2026-01-01")
    with pytest.raises(TypeError, match="not a date"):
        backd.fill_buckets([], field="a", unit="day", from_="yesterday", to="2026-01-01")
    with pytest.raises(TypeError, match="unit must be"):
        backd.fill_buckets([], field="a", unit="hour", from_="1", to="2")  # type: ignore[arg-type]
    with pytest.raises(ValueError, match="more than 5000 buckets"):
        backd.fill_buckets([], field="a", unit="day", from_="1990-01-01", to="2026-01-01")
    json.dumps(backd.fill_buckets([], field="a", unit="year", from_="2020", to="2021"))
