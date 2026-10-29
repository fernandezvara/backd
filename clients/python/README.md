# backd (Python)

Python client for [backd](https://github.com/fernandezvara/backd): auth, sessions and data for services, scripts and notebooks.

```python
from backd import Client

with Client(url="http://localhost:8080", realm="shelf", api_key=key) as backd:
    ...
```

`AsyncClient` has the same calls with `async with` and `await`. One runtime dependency: [`httpx`](https://www.python-httpx.org/).
`pip install backd` (PyPI, from the first release; until then `pip install "backd @ git+https://github.com/fernandezvara/backd#subdirectory=clients/python"`). Python 3.10 or later. The client is under construction (milestone 38); the [docs page](https://fernandezvara.github.io/backd/docs/build/clients/python/) lists what exists. `backd types --lang python` generates `TypedDict`s and typed wrappers for a realm.

The admin API is not part of the client, for the same reason it isn't in the JavaScript, Dart and Go clients.

## Development

```sh
python -m venv .venv && .venv/bin/pip install -e '.[dev]'
.venv/bin/python scripts/unasync.py     # writes src/backd/_sync from src/backd/_async
.venv/bin/ruff check . && .venv/bin/ruff format --check . && .venv/bin/mypy && .venv/bin/pytest
```

The async client is the source: `src/backd/_sync` is generated from `src/backd/_async` and checked in, and a test fails when it is out of date. Edit the async file, then run `scripts/unasync.py`. Files with no twin (the realtime stream, where a thread and a task differ in more than syntax) are written by hand.
