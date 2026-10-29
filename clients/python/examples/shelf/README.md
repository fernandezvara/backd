# The Python client against the Shelf tutorial stack

`main.py` signs in to the tutorial's `shelf` realm, lists the published assets with the generated types, saves a note
and follows the assets live for ten seconds.

```sh
pip install -e clients/python
SHELF_EMAIL=ada@example.com SHELF_PASSWORD='dev-p4ssw0rd!' python clients/python/examples/shelf/main.py
```

`BACKD_URL` is the address of the stack (`http://localhost:8080` by default). The session is kept in
`~/.backd-shelf-python.session`: only the first run signs in.

`shelf_types/types.py` is generated and checked by CI (`make shelf-types`); after a change to the Shelf's schema,
`make shelf-types-update` rewrites it. The guide is
[Python client](https://fernandezvara.github.io/backd/docs/build/clients/python/).
