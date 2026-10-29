# The Go client against the Shelf tutorial stack

`main.go` signs in to the tutorial's `shelf` realm, lists the published assets with the generated
types, saves a draft note and follows the assets live for ten seconds.

```sh
SHELF_EMAIL=ada@example.com SHELF_PASSWORD='dev-p4ssw0rd!' go run ./clients/go-examples/shelf
```

`BACKD_URL` is the address of the stack (`http://localhost:8080` by default). The session is kept in
`~/.backd-shelf.session`: only the first run signs in.

`backd/types.go` is generated and checked by CI (`make shelf-types`); after a change to the Shelf's
schema, `make shelf-types-update` rewrites it. The guide is [Go client](https://fernandezvara.github.io/backd/docs/build/clients/go/).
