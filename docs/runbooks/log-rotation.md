# Log rotation

`hookshipd` writes structured JSON lifecycle and server-error events to standard
error. There is **no HTTP request or payload logging**; attempts, outbox rows,
status, and `/statusz` metrics are payload-free
([`../operations.md`](../operations.md)). Rotation therefore only manages
operational logs, never sensitive bodies.

## Under systemd (default): journald

The systemd units send stderr to the journal. Control retention with journald,
not logrotate:

```sh
journalctl -u hookshipd -f                     # follow
journalctl -u hookshipd --since "1 hour ago"   # window

# Cap journal size / age (in /etc/systemd/journald.conf):
#   SystemMaxUse=500M
#   MaxRetentionSec=14day
sudo systemctl restart systemd-journald

# One-off vacuum:
sudo journalctl --vacuum-time=14d
sudo journalctl --vacuum-size=500M
```

## File-based logs

If a deployment routes hookshipd's stderr to a file, or uses a file-based
metadata outbox sink (`HOOKSHIP_OUTBOX_SINK=/var/log/hookship/outbox.log`), use
the provided logrotate config
[`../../deploy/logrotate/hookship`](../../deploy/logrotate/hookship):

```sh
sudo install -m 0644 deploy/logrotate/hookship /etc/logrotate.d/hookship
sudo logrotate --debug /etc/logrotate.d/hookship   # dry run
```

It rotates `/var/log/hookship/*.log` daily, keeps 14 compressed generations, and
recreates files `0640 hookship:hookship`. `copytruncate` avoids needing the
process to reopen its file descriptor.

## Compose

Configure the Docker logging driver (for example `json-file` with
`max-size`/`max-file`, or `journald`) on the host so container stdout/stderr does
not grow unbounded.
