# Operational runbooks

Task-focused procedures for operating a single-VM `hook-service` deployment. They
pair with the scripts in [`../../deploy/scripts`](../../deploy/scripts) and the
packaging in [`../../deploy`](../../deploy/README.md). Each runbook states what is
automated, what is manual, and the safety constraints that actually hold in this
codebase.

| Runbook | Covers |
| --- | --- |
| [Backup and restore](backup-restore.md) | Consistent PostgreSQL + object-store backup and restore |
| [Restore drill](restore-drill.md) | Recurring recovery-confidence drill on disposable databases |
| [Upgrade and rollback](upgrade-rollback.md) | Forward-only upgrades and schema-compatible rollback |
| [Key rotation](key-rotation.md) | KMS rewrap (zero-downtime) and static-key rotation safety |
| [PostgreSQL recovery](postgres-recovery.md) | Recovering the source-of-truth database |
| [Object store (MinIO) recovery](minio-recovery.md) | Recovering ciphertext payload objects |
| [Log rotation](log-rotation.md) | journald retention and file-based log rotation |
| [Troubleshooting](../troubleshooting.md) | Symptom-first diagnosis |

## Reaching a locked-down VM

The listener is loopback-first. When it is bound to `127.0.0.1` (the default),
operate it locally on the VM or over an SSH loopback tunnel from your
workstation:

```sh
ssh -N -L 8080:127.0.0.1:8080 operator@vm     # tunnel the loopback listener
hookshipctl diagnostics -url http://127.0.0.1:8080
```

If the service terminates TLS directly (`HOOKSHIP_TLS_CERT`/`HOOKSHIP_TLS_KEY`),
probe `https://` and expect a certificate for the bound name. If TLS is
terminated by a reverse proxy with `HOOKSHIP_PROXY_MODE=true`, reach the service
through the proxy and keep the process bound to loopback. Never expose a
non-loopback plaintext listener; configuration validation rejects it. See
[`../operations.md`](../operations.md) and [`../configuration.md`](../configuration.md).
