# Contributing

Thank you for improving HookShip.

## Before opening a change

1. Open or reference an issue for behavior changes.
2. Keep the change within the repository's current foundation scope.
3. Do not introduce delivery APIs or durability behavior before the
   specification gate in [`spec/README.md`](spec/README.md).
4. Record third-party provenance and licensing when external material is used.

## Development

Run:

```sh
make check
```

Pull requests should explain the motivation, security implications, tests, and
documentation impact. Avoid including webhook payloads, credentials, personal
data, or other sensitive material in code, fixtures, logs, issues, or pull
requests.

By contributing, you agree that your contribution is licensed under the
Apache License 2.0.
