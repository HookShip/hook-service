# Key rotation

How payload keys work here determines what rotation is safe. Payloads are sealed
with AES-256-GCM under a 32-byte data encryption key (DEK). The DEK's stable
`reference`/`version` are **derived from the DEK bytes**, and the version is
stored beside every ciphertext
([`internal/keyprovider`](../../internal/keyprovider/keyprovider.go),
[`internal/payloadcrypto`](../../internal/payloadcrypto/payloadcrypto.go)). There
is one active key with no multi-key resolver, so a payload only opens under the
same DEK it was sealed with.

## KMS rewrap — recommended, zero-downtime

With `HOOKSHIP_KEY_PROVIDER=aws-kms`, the DEK is wrapped by a KMS key and
unwrapped at startup. Rotating the KMS key **re-wraps the same DEK**: the DEK
plaintext (and therefore its version) is unchanged, so every previously sealed
payload stays decryptable.

```sh
deploy/scripts/rotate-payload-key.sh kms-rewrap --new-key-id <new-kms-key-id>
sudo systemctl restart hookshipd.service
```

The script re-encrypts the wrapped data key under the new KMS key with
`aws kms re-encrypt`, updates `HOOKSHIP_KMS_WRAPPED_DATA_KEY` and
`HOOKSHIP_KMS_KEY_ID` in the env file, and leaves the DEK unchanged. This is the
supported way to rotate key material without re-encrypting stored payloads.

## Static-key rotation — constrained

Changing `HOOKSHIP_PAYLOAD_KEY` to new bytes produces a **new** DEK version, and
the single-key sealer cannot open ciphertext sealed under the old version. It is
therefore safe to change a static key only when no live ciphertext depends on the
current key. Check first:

```sh
deploy/scripts/rotate-payload-key.sh static-check --dsn "$HOOKSHIP_DATABASE_URL"
```

- **SAFE** (zero live payloads): set a new `HOOKSHIP_PAYLOAD_KEY` and restart.
- **NOT SAFE** (live payloads exist): do not change the key. Either wait for
  retention to reclaim terminal payloads (it nulls PostgreSQL ciphertext and
  deletes objects; see [`../operations.md`](../operations.md)), or migrate to the
  KMS provider and rotate with `kms-rewrap`.

### Migrating static → KMS without breaking history

Because the version is derived from the DEK bytes, wrapping the **same 32 bytes**
that `HOOKSHIP_PAYLOAD_KEY` currently holds produces the identical version, so
historical payloads keep opening after the switch. Wrap those bytes with your KMS
key, set the `HOOKSHIP_KMS_*` variables (and remove `HOOKSHIP_PAYLOAD_KEY`), then
restart. From then on, use `kms-rewrap` for rotation.

## Always

- Take a [backup](backup-restore.md) before any key change.
- Never write real key material to source control; edit
  `/etc/hookship/hookshipd.env` (mode 0600) in place.
