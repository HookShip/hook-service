# Troubleshooting

Symptom-first diagnosis for a single-VM deployment. Start with the probes and
logs, then match a symptom below.

```sh
curl -fsS http://127.0.0.1:8080/healthz   # process is serving
curl -fsS http://127.0.0.1:8080/readyz    # backend + migrations healthy
curl -fsS http://127.0.0.1:8080/statusz   # payload-free operator metrics
journalctl -u hookshipd -n 200 --no-pager
hookshipctl doctor                        # validate backend selection
```

## The process refuses to start

- **`invalid configuration: … must be …`** — a bounded setting is out of range;
  fix the named `HOOKSHIP_*` variable ([`configuration.md`](configuration.md)).
  Validate without serving: `hookshipd -check-config`.
- **Non-loopback plaintext rejected** — the listener is bound off-loopback with
  neither TLS nor proxy mode. Set `HOOKSHIP_TLS_CERT`+`HOOKSHIP_TLS_KEY`, or
  `HOOKSHIP_PROXY_MODE=true` behind a trusted proxy, or bind `127.0.0.1`.
- **Partial TLS pair** — set both `HOOKSHIP_TLS_CERT` and `HOOKSHIP_TLS_KEY` or
  neither; a partial pair fails fast.
- **systemd start fails immediately** — check `systemctl status hookshipd` and
  confirm `/etc/hookship/hookshipd.env` is readable and has no `REPLACE_ME` left.

## `/readyz` returns 503 (not_ready)

- **Backend unreachable** — verify `HOOKSHIP_DATABASE_URL` and that PostgreSQL is
  up; see [PostgreSQL recovery](runbooks/postgres-recovery.md).
- **Migration drift** — readiness fails on drift. Run `hookshipctl migrate`. If
  it reports "database contains forward migration … missing from source", the
  binary is older than the schema; deploy the matching newer binary (see
  [upgrade and rollback](runbooks/upgrade-rollback.md)).

## Migrations fail

- **PostgreSQL 17 or newer is required** — the server is too old; upgrade it.
- **checksum drift at version …** — the applied migration differs from the
  embedded one. Migrations are immutable; do not edit an applied migration.
  Restore from a backup that matches the deployed version.

## Deliveries never leave / all attempts fail

- **SSRF protections** — the transport rejects private, loopback, link-local, and
  metadata targets by default. For a trusted local/VM target, set the explicit
  opt-in: `HOOKSHIP_DELIVERY_ALLOW_PRIVATE`, `…_ALLOW_LOOPBACK`, or
  `…_ALLOW_PLAINTEXT`. These reduce SSRF protection — use only for trusted
  targets.
- **Circuit open / rate limited** — check `/statusz` for `circuit-open` skips and
  rate/concurrency deferrals. The worker defers (bounded backpressure) rather
  than dropping; raise the per-endpoint/assignment limits if intended.
- **Signature rejected downstream** — confirm the endpoint's Standard Webhooks
  `signingSecret` matches the receiver's expectation.

## Object-store payloads fail

- **Non-loopback plaintext object transport rejected** — set
  `HOOKSHIP_PAYLOAD_OBJECT_USE_TLS=true` (with a CA bundle if private), or, only
  on a trusted network, `HOOKSHIP_PAYLOAD_OBJECT_ALLOW_PLAINTEXT=true`.
- **Bucket/credentials** — verify `HOOKSHIP_PAYLOAD_OBJECT_*`; see
  [object store recovery](runbooks/minio-recovery.md).

## Payloads cannot be decrypted after a key change

You changed a static `HOOKSHIP_PAYLOAD_KEY` while live ciphertext still depended
on the previous key. Restore the previous key material, or restore from backup.
Prevent this with the [key rotation](runbooks/key-rotation.md) procedure
(`static-check` gate, or KMS rewrap).

## Reaching a loopback-only VM

Tunnel the loopback listener over SSH rather than exposing it:

```sh
ssh -N -L 8080:127.0.0.1:8080 operator@vm
hookshipctl diagnostics -url http://127.0.0.1:8080
```

If none of the above resolves the issue, capture `journalctl -u hookshipd`, the
`/statusz` output, and `hookshipctl doctor -output=json`, and open an issue per
[`../SUPPORT.md`](../SUPPORT.md).
