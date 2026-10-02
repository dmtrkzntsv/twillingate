# Backing up with litestream

Everything twillingate keeps is in one SQLite file,
`/var/lib/twillingate/twillingate.db`. [Litestream](https://litestream.io)
streams that file's write-ahead log to S3-compatible storage as it is
written, so the off-host copy is seconds behind. It runs beside the
collector, not inside it: twillingate never reads its settings, and neither
the installer nor the compose file sets it up. This page does, for systemd
and for docker compose. The examples use Cloudflare R2 and litestream 0.5;
any S3-compatible store works.

- [Bucket and credentials](#bucket-and-credentials)
- [The configuration](#the-configuration)
- [systemd](#systemd)
- [docker compose](#docker-compose)
- [Check the backup monthly](#check-the-backup-monthly)
- [Restore after losing the host](#restore-after-losing-the-host)

## Bucket and credentials

Create a bucket, an **Object Read & Write** token for the server, and an
**Object Read** token for restore drills run anywhere else (a token that
cannot write cannot damage the backup). Put the four values in the same
environment file the collector reads: `/etc/twillingate/twillingate.env`
for systemd, `.env` beside the compose file for docker. Twillingate itself
ignores them.

```sh
LITESTREAM_ACCESS_KEY_ID=…
LITESTREAM_SECRET_ACCESS_KEY=…
R2_BUCKET=twillingate-backup
R2_ENDPOINT=https://<account_id>.r2.cloudflarestorage.com
```

## The configuration

`litestream.yml` is the same for both setups:

```yaml
dbs:
  - path: /var/lib/twillingate/twillingate.db
    replicas:
      - type: s3
        bucket: ${R2_BUCKET}
        path: twillingate
        endpoint: ${R2_ENDPOINT}
        sync-interval: 5s
```

The replica's `path` is the prefix inside the bucket. If you already
replicate under another prefix (earlier releases shipped `path: litestream`),
keep it.

> **Restore with the same configuration.** A restore names the database by
> its `dbs` path and reads the replica configured for it, so the server, a
> drill and a recovery host must use the same `path` values byte for byte,
> even when the restored file lands elsewhere. A wrong one produces an empty
> restore, not an error.

> **Run one litestream version everywhere.** 0.5 stores backups in a bucket
> format (LTX) that 0.3 cannot see, and the reverse: a mismatched restore
> reports `no matching backups found`, which looks exactly like an empty
> bucket. The compose service below pins `litestream/litestream:0.5`.

On a Raspberry Pi or another small host, raise `sync-interval` (say `60s`)
for fewer, larger uploads.

## systemd

Install the litestream binary from <https://litestream.io/install/> (the
`.deb` from its GitHub releases puts it at `/usr/bin/litestream`), then the
configuration and a unit that runs as the collector's user:

```bash
sudo install -m 0644 litestream.yml /etc/litestream.yml
sudo tee /etc/systemd/system/litestream.service > /dev/null <<'EOF'
[Unit]
Description=Litestream backup for the twillingate collector
After=network-online.target twillingate.service
Wants=network-online.target

[Service]
User=twillingate
Group=twillingate
ExecStart=/usr/bin/litestream replicate -config /etc/litestream.yml
EnvironmentFile=/etc/twillingate/twillingate.env
Restart=on-failure
RestartSec=5
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=/var/lib/twillingate
ProtectHome=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
EOF
sudo systemctl daemon-reload
sudo systemctl enable --now litestream
journalctl -u litestream -f      # "snapshot complete", then a line per upload
```

Change `ExecStart=` if `command -v litestream` prints another path, and
`User=`/`Group=` if the installer was given `--user`. Re-running
`install.sh` to upgrade twillingate leaves this unit alone.

## docker compose

Put `litestream.yml` next to `docker-compose.yml`, the four variables in
`.env`, and add this service under `services:`:

```yaml
  litestream:
    image: litestream/litestream:0.5
    restart: unless-stopped
    # The collector's user: a root-owned -wal or -shm file in the shared
    # volume would lock the collector out of its own database.
    user: "10001:10001"
    command: ["replicate", "-config", "/etc/litestream.yml"]
    volumes:
      - ./litestream.yml:/etc/litestream.yml:ro
      - data:/var/lib/twillingate
    env_file: .env
```

```bash
docker compose up -d
docker compose logs -f litestream   # "snapshot complete", then a line per upload
```

## Check the backup monthly

A backup you have never restored is not a backup. Restore into a scratch
file and query it: the newest day must be today or yesterday.

```bash
# systemd: litestream reads the bucket credentials from the env file
sudo sh -ac '. /etc/twillingate/twillingate.env
  litestream ltx -config /etc/litestream.yml /var/lib/twillingate/twillingate.db
  litestream restore -config /etc/litestream.yml -o /tmp/check.db /var/lib/twillingate/twillingate.db'
sudo sqlite3 /tmp/check.db 'PRAGMA quick_check;'             # expect: ok
sudo sqlite3 /tmp/check.db "SELECT MAX(day) FROM v_views_daily;"
sudo rm /tmp/check.db*
```

```bash
# docker compose: the images carry no sqlite3, so check the copy on the host
docker compose exec litestream litestream ltx -config /etc/litestream.yml \
  /var/lib/twillingate/twillingate.db
docker compose exec litestream litestream restore -config /etc/litestream.yml \
  -o /var/lib/twillingate/check.db /var/lib/twillingate/twillingate.db
docker compose cp twillingate:/var/lib/twillingate/check.db ./check.db
docker compose exec twillingate rm /var/lib/twillingate/check.db
sqlite3 check.db 'PRAGMA quick_check;'                        # expect: ok
sqlite3 check.db "SELECT MAX(day) FROM v_views_daily;"
rm check.db
```

`ltx` lists what the bucket holds. A newest day several days old means
litestream stopped uploading (`journalctl -u litestream` or `docker compose
logs litestream`); `no matching backups found` against a bucket you know is
written means a version mismatch.

## Restore after losing the host

> **Restore before the collector starts, always.** Started against an empty
> data directory, twillingate creates a fresh database, and litestream then
> uploads that empty database beside the good backup.

**systemd.** Install twillingate (`install.sh`), put the same credentials in
`/etc/twillingate/twillingate.env`, and set litestream up as
[above](#systemd) but without starting it (`systemctl enable`, not
`--now`). Then:

```bash
sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; litestream restore \
  -config /etc/litestream.yml -o /var/lib/twillingate/twillingate.db \
  /var/lib/twillingate/twillingate.db'
sudo -u twillingate sqlite3 /var/lib/twillingate/twillingate.db 'PRAGMA quick_check;'
sudo systemctl start twillingate litestream
```

**docker compose.** With the compose file (litestream service included),
`litestream.yml` and `.env` in place and nothing started yet:

```bash
# Creates the data volume owned by the collector's user; mounted first by
# the litestream image, it would belong to root and the restore would fail
# with "permission denied".
docker compose run --rm --no-deps --entrypoint true twillingate
docker compose run --rm --no-deps litestream restore -config /etc/litestream.yml \
  -o /var/lib/twillingate/twillingate.db /var/lib/twillingate/twillingate.db
docker compose up -d
docker compose exec twillingate twillingate project list   # your projects are back
```

A backup from an older release migrates when the collector starts. Two
things do not survive: the visitor salt (rotated daily anyway, so at most a
day of continuity) and events still buffered in memory when the host died,
bounded by `BUFFER_FLUSH_INTERVAL`.
