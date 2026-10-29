"""The Python client against the Shelf tutorial stack (docs: Tutorial): it signs in, lists the published assets, saves
a note and follows the assets live for a little while.

    SHELF_EMAIL=ada@example.com SHELF_PASSWORD='dev-p4ssw0rd!' python clients/python/examples/shelf/main.py

``BACKD_URL`` is the stack's address (http://localhost:8080 by default). The session is kept in
``~/.backd-shelf-python.session``, so only the first run signs in. ``shelf_types`` is generated:

    CONFIG_DIR=examples/config backd types --realm shelf --lang python --out clients/python/examples/shelf/shelf_types
"""

from __future__ import annotations

import os
import time
from pathlib import Path

from shelf_types.types import MainAssetsFields, MainAssetsInput, ShelfRealm

import backd
from backd import Client, FileStorage, LiveState, SecondStepRequiredError


def main() -> None:
    url = os.environ.get("BACKD_URL", "http://localhost:8080")
    storage = FileStorage(Path.home() / ".backd-shelf-python.session")
    # The token lives in the file above: only the first run signs in.
    with Client(url=url, realm="shelf", storage=storage) as client:
        if not client.auth.has_session():
            try:
                client.auth.login(os.environ["SHELF_EMAIL"], os.environ["SHELF_PASSWORD"])
            except SecondStepRequiredError as e:
                raise SystemExit(f"this account has a second step ({e.code}): sign in once with `backd login`") from e

        assets = ShelfRealm(client).main.assets

        # The newest published assets: a typed query, with the field names the config declares.
        page = assets.list(
            where={MainAssetsFields.PUBLISHED_AT: backd.not_null()},
            order_by=MainAssetsFields.PUBLISHED_AT_DESC,
            limit=20,
        )
        for asset in page:
            print(f"{asset['kind']:<8} {asset['title']:<40} {', '.join(asset.get('tags', []))}")

        # A draft note of the signed-in user.
        draft: MainAssetsInput = {"kind": "note", "title": "Hello from Python", "body": "Saved with the Python client."}
        note = assets.create(draft)
        print("saved", note["id"])

        # Follow the assets the user may read, live, for a few seconds.
        view = assets.subscribe(load={"sort": "-_meta.created_at", "limit": 20})

        def show(state: LiveState) -> None:
            print(f"live: {len(state.items)} assets ({state.status})")

        view.on("change", show)
        try:
            time.sleep(10)
        finally:
            view.close()
        if view.error is not None:
            raise view.error


if __name__ == "__main__":
    main()
