# Troubleshooting

Start here:

```sh
./scripts/preflight.sh    # checks the machine, explains any problem
make ps                   # what is running
make logs-app             # what the application is saying
make health               # what it thinks of itself
```

Every HTTP response carries an `X-Request-Id`, and the same value appears in the
log line for that request. When someone reports a failure, ask for that ID and
search the logs for it.

---

## "permission denied" on the storage directory

```text
storage: cannot open root "/data": open /data: permission denied
Ensure the storage volume is mounted and writable by uid 10001.
```

The container runs as an unprivileged user that cannot write to `STORAGE_ROOT`.
This is the most common installation problem, and it has two possible causes.

**Ownership.** Compare the directory's owner with the configured user:

```sh
stat -c '%u %g' /srv/pifiles
grep PIFILES_UID .env
```

Fix by making them agree — either:

```sh
sudo chown -R 10001:10001 /srv/pifiles
```

or set `PIFILES_UID` and `PIFILES_GID` in `.env` to the directory's current
owner, then `make up`.

**SELinux**, on Fedora, RHEL, CentOS, or Rocky. If `getenforce` prints
`Enforcing` and ownership is already correct, the bind mount needs a label. The
Compose file already mounts with `:z`; if you have edited it, restore that
suffix:

```yaml
- ${STORAGE_ROOT:-/srv/pifiles}:/data:z
```

Raspberry Pi OS, Debian, and Ubuntu use AppArmor and are unaffected.

## The application will not start

Read the first few lines of `make logs-app` — startup failures print a plain
explanation before any JSON.

**Configuration errors** list every problem at once:

```text
invalid configuration (2 problems):
  - APP_SECRET_KEY: is required in production
  - UPLOAD_CHUNK_SIZE: must be at most 256MB
```

Check `.env` against `.env.example`, or validate without starting:

```sh
docker compose run --rm app -check-config
```

**"database unreachable"** — PostgreSQL did not come up. Check `make ps` and
`docker compose logs postgres`. A corrupted volume after an unclean shutdown
usually appears here.

**"migration has been modified since it was applied"** — an applied migration
file was edited. Never edit applied migrations; add a new one. If this happened
on a development database, the fastest fix is `docker compose down -v` to start
clean. On production, restore from backup.

**"this binary is older than the database schema"** — you rolled back the
application without rolling back the database. Redeploy the newer version, or
restore the database from a backup taken before the upgrade.

## Cannot reach it from a browser

**From the machine itself:**

```sh
make health                # via the app directly
curl -I http://localhost   # via Caddy
```

If the first works and the second does not, the problem is Caddy:

```sh
docker compose logs caddy
```

A Caddyfile syntax error appears as a restart loop with
`Error: adapting config`.

**From another device** — check a firewall:

```sh
sudo ufw status
sudo ufw allow 80/tcp && sudo ufw allow 443/tcp
```

**"port is already allocated"** — something else holds port 80. Find it with
`sudo ss -ltnp 'sport = :80'`, or change `HTTP_PUBLIC_PORT` in `.env`.

## Certificate problems

**"could not get certificate"** with a public domain — Let's Encrypt must reach
the machine on port 80 from the internet. Confirm the DNS record resolves to
your public address, that ports 80 and 443 are forwarded, and that your ISP does
not block them.

Let's Encrypt rate-limits to five failures per hour per hostname. Use their
staging environment while debugging:

```ini
CADDY_GLOBAL_OPTIONS=acme_ca https://acme-staging-v02.api.letsencrypt.org/directory
```

**"your connection is not private"** with a `.local` name — expected. Caddy
issued a locally-trusted certificate; install its root certificate on each
device, or use plain HTTP on a network you trust.

## Login fails silently over plain HTTP

*(Relevant from Phase 2.)* A `Secure` cookie is never sent over plain HTTP, so
the browser discards the session immediately and the login appears to do
nothing. Either serve over HTTPS, or set `SESSION_COOKIE_SECURE=false` and
accept that cookies travel unencrypted.

## Forgotten password, or locked out of the only administrator account

Nothing about this is lost, but the way back is through the shell rather than
the browser: PiFiles sends no email, so there is no "forgot password" link to
click. That is a deliberate trade — an SMTP configuration is one more thing to
get wrong on a home server, and the failure mode is a password-reset email that
never arrives — but it does mean recovery is a command.

### Somebody else forgot theirs

Any administrator can set a new one from **Administration → Accounts → the key
icon**. Every session on that account ends, and they are asked to choose their
own password at the next sign-in.

### You are the only administrator, and you are locked out

Create a second administrator from the machine itself:

```sh
cd /path/to/pifiles
make create-admin
```

This works on an instance that already has accounts — it says how many exist
and then adds another. Sign in as the new account and reset the original from
Administration → Accounts.

Once you are back in, delete the temporary account if you do not want it.

### Locked out by too many failed attempts

That is the lockout, not a lost password, and it clears itself after
`LOCKOUT_DURATION` (15 minutes by default). An administrator can clear it
immediately from **Administration → Accounts → the padlock icon**.

### Nobody can reach the machine either

If you have neither a working administrator account nor shell access, there is
no recovery path. Content is on disk under `STORAGE_ROOT` and can be read
directly by anyone with the filesystem, but the accounts themselves cannot be
recovered without one or the other. This is the same trade every self-hosted
system makes, and it is worth knowing before you need it.

## Browser insists on HTTPS for a plain-HTTP instance

If a browser refuses to load `http://` and there is no certificate, an HSTS
entry was set at some point. PiFiles never sends `Strict-Transport-Security`
over a plain-HTTP connection, so this is either left over from a previous
server on that hostname, or the site was previously served over HTTPS.

Clear it at `chrome://net-internals/#hsts` (Chrome) or by clearing site data
for that host (Firefox).

## It is slow

Check hardware before tuning software.

```sh
vcgencmd get_throttled     # anything but 0x0 means power or thermal throttling
vcgencmd measure_temp      # sustained 80°C+ means throttling
sudo hdparm -t /dev/sda    # a USB 3 SSD should exceed 100 MB/s
free -h                    # swap in use means memory pressure
docker stats               # which container is actually busy
```

An inadequate power supply is the most common cause of poor and erratic
performance on a Pi, and no configuration change compensates for it. See
[raspberry-pi.md](raspberry-pi.md#performance).

## Health reports degraded

```sh
make health
```

The `detail` field on the failing subsystem says why. The usual cause is free
space approaching `STORAGE_MIN_FREE`. `degraded` means "attend to this", not
"broken" — the service keeps working, which is why it still returns HTTP 200.

## Out of disk space

```sh
df -h /srv/pifiles
docker system df
```

Uploads are refused once free space would drop below `STORAGE_MIN_FREE`, so the
database and OS are protected. Recover space by emptying Trash, reducing
`TRASH_RETENTION_DAYS` or `VERSIONS_MAX_PER_FILE`, or pruning Docker:

```sh
docker system prune -a
```

`docker system prune` never touches `STORAGE_ROOT` or the database volume.

## Starting over

**Reset the database, keep the files** — this deletes all users, permissions,
and shares, and the metadata that maps stored blobs to filenames. The blobs
survive but become unreferenced.

```sh
docker compose down -v
make up
```

**Remove everything, including files:**

```sh
docker compose down -v
sudo rm -rf /srv/pifiles
```

There is no undo. Take a backup first.

## Getting help

Include:

```sh
make version
uname -m && cat /etc/os-release | head -2
docker --version && docker compose version
make ps
make logs-app 2>&1 | tail -50
./scripts/preflight.sh
```

Redact `.env` before sharing it — it contains your database password and
application secret key.
