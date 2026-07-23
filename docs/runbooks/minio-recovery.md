# Object store (MinIO) recovery

This applies only when `HOOKSHIP_PAYLOAD_BACKEND=object-store`. In that mode the
S3-compatible object store holds AES-256-GCM **ciphertext** payload objects,
while PostgreSQL keeps the object locators, checksums, and key metadata — never
raw bytes. The database remains the source of truth and the acceptance barrier;
the object store holds only sealed payload bodies.

## Consistency model

- An accepted (202) payload requires both the immutable object PUT and the
  durable PostgreSQL commit. The database therefore references objects that were
  successfully written.
- Retention reclaims a payload's object only after the payload is terminal and
  its idempotency ledger is tombstoned, so a missing reclaimed object is expected
  and harmless.
- A restore mirrors objects that the database still references; objects reclaimed
  before the backup are intentionally absent.

## Recover the bucket

```sh
# From a backup that captured objects/ (backup.sh with mc configured):
sudo deploy/scripts/restore.sh --input /var/lib/hookship/backups/hookship-<stamp> \
  --dsn "$HOOKSHIP_DATABASE_URL"
```

`restore.sh` restores the database first, then mirrors `objects/` back into the
configured bucket with `mc`. To mirror manually:

```sh
mc alias set store https://<endpoint> <access-key> <secret-key>
mc mb --ignore-existing store/<bucket>
mc mirror --overwrite /var/lib/hookship/backups/hookship-<stamp>/objects store/<bucket>
```

## Reconcile database vs bucket

After recovery, ciphertext for a still-live payload must exist in the bucket. A
payload whose object is missing but whose ledger is not yet terminal indicates an
inconsistent restore — restore from a backup where both datasets are present.

```sh
# Payloads that still expect ciphertext in the object store:
psql "$HOOKSHIP_DATABASE_URL" -tAc \
  "SELECT count(*) FROM payload_records WHERE backend='object-store' AND deleted_at IS NULL AND final_object_key IS NOT NULL"
```

## Transport and TLS

Prefer TLS to the object store (`HOOKSHIP_PAYLOAD_OBJECT_USE_TLS=true`, with
`HOOKSHIP_PAYLOAD_OBJECT_CA_BUNDLE` for a private CA). A non-loopback plaintext
endpoint fails closed unless `HOOKSHIP_PAYLOAD_OBJECT_ALLOW_PLAINTEXT=true`
asserts a trusted network; use that only on a trusted local/VM network, never
across untrusted links. For local administration, reach a loopback-bound MinIO
over an SSH tunnel rather than exposing it. See
[`../configuration.md`](../configuration.md).
