# Backup and restore

A backup nobody has restored is a hope, not a backup. This guide covers taking
one, checking it, and — most importantly — rehearsing the restore before you
need it.

---

## The short version

```sh
make backup                                    # take one
make backup-verify BACKUP=/path/to/backup      # check it is intact
make restore BACKUP=/path/to/backup            # put it back
```

Backups land in `backups/` under your `STORAGE_ROOT` unless you say otherwise.
**Copy them somewhere else.** A backup on the same disk as the original protects
against mistakes, not against the disk failing.

---

## What is in a backup

| File | What it is |
| --- | --- |
| `database.dump` | Every account, folder, permission, share, version, and audit event |
| `content.tar` | The stored files themselves |
| `env` | Your configuration, **including the secret key** |
| `manifest.txt` | What this backup is, when it was taken, and how to restore it |
| `checksums.txt` | SHA-256 of everything above |

Two exclusions are deliberate. `temp/` holds partial uploads, which are
meaningless without their session rows and will have expired by the time anyone
restores. `backups/` is excluded so a backup written inside `STORAGE_ROOT` does
not try to include itself.

### The secret key

`env` contains `APP_SECRET_KEY`. Without it, a restored instance cannot decrypt
sessions or open public links created before the backup — everyone is signed out
and every published link is dead.

That makes the backup as sensitive as the server. **Anyone who has it can open
your public links.** Store it somewhere private, and do not put it in a shared
folder or a public repository.

---

## Taking a backup

```sh
make backup                          # into STORAGE_ROOT/backups
make backup DEST=/mnt/usb/pifiles    # somewhere else
```

The script checks every step and **never reports success unless every one of
them succeeded**. Specifically, it:

- refuses to run if PostgreSQL is not running, rather than producing a backup
  that silently omits the database;
- checks there is enough free space first, so a full disk cannot produce a
  truncated archive that looks finished;
- reads the database dump back with `pg_restore --list` to confirm it parses;
- reads the content archive back with `tar --test` to confirm it is complete;
- records and immediately verifies checksums;
- **deletes the partial output if anything fails**, so nothing is ever left
  behind that could be mistaken for a usable backup.

A backup taken while people are using the server is fine. `tar` may report that
a file changed while being read; that file was mid-upload and its metadata is
not in the dump either, so the two agree.

### Automatically, every night

```sh
sudo crontab -e
```

```cron
# 3am daily, keeping 14 days.
0 3 * * * cd /home/pi/pifiles && ./scripts/backup.sh >> /var/log/pifiles-backup.log 2>&1
30 3 * * * find /srv/pifiles/backups -maxdepth 1 -name 'pifiles-*' -type d -mtime +14 -exec rm -rf {} +
```

Then check that it is working, because a silent cron job is exactly where a
broken backup hides:

```sh
tail -20 /var/log/pifiles-backup.log
ls -lt /srv/pifiles/backups | head
```

---

## Checking a backup

```sh
make backup-verify BACKUP=/srv/pifiles/backups/pifiles-20260828-030000
```

This re-reads every file and confirms the checksums, the archive, and the
database dump. Worth running on a schedule against your most recent backup: bit
rot on a cheap USB drive is real, and discovering it while restoring is
discovering it too late.

Verification confirms the backup is **not damaged**. It does not confirm it
**restores correctly**. Only a rehearsal does that.

---

## Rehearsing a restore

Do this once, now, before you need it. It takes fifteen minutes and it is the
difference between a backup and a hope.

**Do not rehearse against your real instance.** Use a scratch copy:

```sh
# 1. A separate directory and a separate project name, so nothing collides.
git clone <your pifiles checkout> /tmp/pifiles-rehearsal
cd /tmp/pifiles-rehearsal
export COMPOSE_PROJECT_NAME=pifiles-rehearsal

# 2. Restore into it. There is nothing there, so it will not ask to replace
#    anything.
./scripts/restore.sh /srv/pifiles/backups/pifiles-20260828-030000

# 3. Start it on a different port so it does not clash with the real one.
sed -i 's/^HTTP_PORT=.*/HTTP_PORT=8089/' .env
docker compose up -d

# 4. Check it. This is the part that matters.
```

Four things to check, in order of how badly you would want to know:

1. **Sign in** as an account you recognise, with its own password. This proves
   the accounts and the secret key came through.
2. **Open a file** — actually download it and look at it. This proves the
   content is there and the database points at the right bytes.
3. **`make db-check`** should report zeroes across the board. This proves the
   metadata and the storage agree.
4. **Check a share and a public link** if you use them.

Then tear it down:

```sh
docker compose down -v
cd /tmp && rm -rf pifiles-rehearsal
```

If any of that failed, your backups are not working and you have found out on a
Tuesday afternoon rather than during an emergency. That is the entire point.

---

## Restoring for real

```sh
make restore BACKUP=/srv/pifiles/backups/pifiles-20260828-030000
```

The restore verifies the whole backup **before touching anything** — checksums,
archive, and dump. A restore that gets halfway and then finds the archive is
corrupt has destroyed the instance it was meant to save.

### It will not merge two installations

If the target already has accounts or stored files, the restore stops:

```
✗ This instance already has data.

  Accounts:      3
  Stored files:  present

  Restoring would REPLACE all of it. The restore will not merge two
  installations: the result would be a database referring to content
  that is not there, which is worse than either instance alone.
```

To replace it anyway, pass `--force` and type `replace` when asked. There is no
keypress shortcut; destroying an instance should take a deliberate action.

The previous content is **moved aside**, not deleted — to
`STORAGE_ROOT.replaced-<timestamp>`. Delete it once you are satisfied the
restore is correct.

### Onto a new machine

The common case after hardware fails:

```sh
git clone https://github.com/joshuanmoses/piCloud.git && cd piCloud
mkdir -p /srv/pifiles                       # or wherever your storage will live
./scripts/restore.sh /mnt/usb/pifiles-20260828-030000
make up
```

The restore brings `.env` from the backup because the instance has none, which
is what keeps the secret key — and therefore existing sessions and public
links — intact.

If `STORAGE_ROOT` is different on the new machine, edit `.env` after the restore
and move the content to match.

---

## Upgrading

```sh
git pull
make upgrade
```

`git pull` first: the upgrade builds the application from the source that is
checked out, rather than pulling a published image — PiFiles is distributed as
source, so there is nothing to pull.

It takes a backup first and **refuses to proceed if the backup fails**. Migrations
are forward-only, so that backup is the only way back — which is why it is not
optional. Afterwards it waits for the instance to become healthy and tells you
where the backup is if you need to go back.

---

## What is not covered

- **Continuous replication.** PiFiles has no streaming replica or
  point-in-time recovery. A backup is a snapshot, and you lose whatever changed
  since the last one. For a household file server that is the right trade; if it
  is not for you, PostgreSQL's own WAL archiving is what you want, and it is
  outside what these scripts do.
- **Encryption at rest.** Backups are not encrypted. If you are copying them to
  storage you do not control, encrypt them yourself — `age` and `gpg` both do
  this well, and doing it here would mean managing another key that could be
  lost separately from the data it protects.
- **Off-site copying.** The scripts write to a directory. Getting that directory
  somewhere else is `rsync`, `rclone`, or a scheduled copy to another machine,
  and those tools do it better than a reimplementation would.

---

## If a restore fails

The restore says which step failed and whether anything was changed.

- **"Nothing was changed"** — the backup was rejected before the instance was
  touched. Try another backup, or `make backup-verify` to see what is wrong
  with this one.
- **"This instance is now in a partial state"** — the restore got as far as
  writing. Do not start it. Fix the cause and run the restore again; it is safe
  to repeat and will replace whatever is there.

If the database restored but the content did not, `make db-check` will report
`files_missing_content`. That means the dump and the archive came from different
backups — restore both from the same directory.
