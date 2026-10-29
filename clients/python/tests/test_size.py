import json
from pathlib import Path

import pytest

from backd import format_bytes, parse_size

VECTORS = json.loads((Path(__file__).parents[2] / "testdata" / "size-vectors.json").read_text())


@pytest.mark.parametrize("v", VECTORS["parse"], ids=lambda v: repr(v["in"]) + "/" + v["family"])
def test_parse_vectors(v: dict[str, object]) -> None:
    kw = {"family": v["family"], "allow_bytes": v["allowBytes"]}
    if v.get("error"):
        with pytest.raises(ValueError):
            parse_size(str(v["in"]), **kw)  # type: ignore[arg-type]
    else:
        assert parse_size(str(v["in"]), **kw) == v["bytes"]  # type: ignore[arg-type]


@pytest.mark.parametrize("v", VECTORS["format"], ids=lambda v: f"{v['bytes']}/{v['family']}")
def test_format_vectors(v: dict[str, object]) -> None:
    assert format_bytes(int(v["bytes"]), v["family"]) == v["out"]  # type: ignore[call-overload, arg-type]


def test_error_names_the_units() -> None:
    with pytest.raises(ValueError, match=r"B, KB, MB, GB, TB, KiB"):
        parse_size("12")
    with pytest.raises(ValueError, match="KiB is not accepted here"):
        parse_size("1KiB", family="decimal")
