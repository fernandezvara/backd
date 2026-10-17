## 0.0.1

- First development release: the package scaffold — `BackdClient` options, the
  injectable transport, the request pipeline (JSON and raw bodies, query
  encoding, retries of `429`/`503` on safe requests, the `X-Backd-Types`
  fingerprint headers), the `BackdException` hierarchy, `TokenStorage` and the
  `RevalidationCache`. Feature modules (auth, data, files, realtime, …) land
  in following releases.
