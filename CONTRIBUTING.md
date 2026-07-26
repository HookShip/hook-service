# Contributing

Thank you for improving HookShip.

## Before opening a change

1. Open or reference an issue for behavior changes.
2. Keep the change within this repository's scope — the outbound delivery data
   plane, protocol, and deployment surface. Control-plane or hosted-platform
   work belongs in another repository (see the topology in
   [`README.md`](README.md)).
3. Do not weaken the security boundaries (payload-free logging, SSRF-safe
   transport, fail-closed non-loopback plaintext, ciphertext-only stored
   payloads).
4. Record third-party provenance and licensing when external material is used.

## Development

Run:

```sh
make check   # format, vet, lint (golangci-lint), tests, race, shell + repo checks
make ci      # check plus govulncheck (the network-dependent vulnerability gate)
```

`make lint` uses golangci-lint (v2) with the pinned set in `.golangci.yml`;
install it locally for a fast inner loop (the target otherwise falls back to a
version-pinned `go run`). See [`docs/development.md`](docs/development.md).

Pull requests should explain the motivation, security implications, tests, and
documentation impact. Avoid including webhook payloads, credentials, personal
data, or other sensitive material in code, fixtures, logs, issues, or pull
requests.

By contributing, you agree that your contribution is licensed under the
Apache License 2.0.
