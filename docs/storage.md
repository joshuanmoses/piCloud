# Storage

PiFiles keeps two different kinds of data in two different places:

- **Metadata** — filenames, folder structure, owners, permissions, shares,
  checksums — lives in PostgreSQL.
- **File content** — the actual bytes — lives on the filesystem beneath
  `STORAGE_ROOT`.

They must be backed up and restored together. Restoring one without the other
produces an inconsistent instance.

---

## 1. Why not the microSD card

`STORAGE_ROOT` defaults to `/srv/pifiles`, but you should point it at an
external drive. Two reasons:

**Wear.** microSD cards have limited write endurance. A file server writes
constantly — uploads, thumbnails, database WAL, version history — and will wear
a card out in months rather than years.

**Blast radius.** If user files live on the boot media, a card failure loses the
operating system *and* everything anyone stored. On a separate drive, a failed
SD card means reinstalling the OS and reconnecting the drive.

`scripts/preflight.sh` warns you when `STORAGE_ROOT` resolves to the boot device.

## 2. Choosing a drive

| Option | Notes |
| --- | --- |
| **NVMe** (Pi 5 + HAT) | Fastest. The best choice for a Pi 5. |
| **USB 3 SATA SSD** | Excellent and inexpensive. The best choice for a Pi 4. |
| **USB 3 hard disk** | Fine for bulk storage. Set `random_page_cost=2.5` — see `deploy/docker/postgres/tuning.md`. |
| **USB flash drive** | Not recommended. Wears out much like an SD card. |
| **Network share (NFS/SMB)** | Not supported. File locking and `os.Root` confinement do not behave reliably over a network filesystem. |

Use an externally powered enclosure, or a good-quality powered hub, for a
spinning disk. The Pi's USB ports cannot reliably supply enough current, and the
resulting brownouts appear as random I/O errors that look like drive failure.

## 3. Mounting the drive permanently

Plug the drive in and find it:

```sh
lsblk -o NAME,SIZE,FSTYPE,LABEL,MOUNTPOINT
```

You will see something like `sda` with a partition `sda1`.

### Format the drive — only if it is new

**This erases everything on it.** Skip this section if the drive already holds
data you want.

```sh
sudo mkfs.ext4 -L pifiles /dev/sda1
```

`ext4` is the right default: it is well-tested on ARM, handles millions of small
files well, and needs no tuning. Avoid `exFAT` and `NTFS` — neither supports the
POSIX ownership and permissions PiFiles relies on.

### Mount it at boot

Get the partition's UUID:

```sh
sudo blkid /dev/sda1
```

Add a line to `/etc/fstab` using that UUID — never the device name, which can
change between boots if you add another drive:

```sh
sudo mkdir -p /srv/pifiles
echo 'UUID=your-uuid-here /srv/pifiles ext4 defaults,noatime,nofail,x-systemd.device-timeout=30 0 2' | sudo tee -a /etc/fstab
```

The options matter:

- `noatime` — do not write an access timestamp on every read. A meaningful
  reduction in write volume on a file server.
- `nofail` — if the drive is missing at boot, boot anyway. Without this, an
  unplugged drive drops a headless Pi into an emergency shell with no display
  and no network, which is very difficult to recover from remotely.
- `x-systemd.device-timeout=30` — do not wait indefinitely for a drive that
  is not there.

Mount it and confirm:

```sh
sudo mount -a
df -h /srv/pifiles
```

Reboot once and check it came back before putting real data on it.

## 4. Permissions

The application container runs as an unprivileged user and must be able to write
to `STORAGE_ROOT`. This is the single most common installation problem.

There are two ways to satisfy it. `scripts/generate-secrets.sh` sets up the
second automatically.

**A — give the directory to the default container user:**

```sh
sudo chown -R 10001:10001 /srv/pifiles
```

**B — run the container as the directory's existing owner.** Put the numeric
owner in `.env`:

```sh
stat -c '%u %g' /srv/pifiles     # prints, for example: 1000 1000
```

```ini
PIFILES_UID=1000
PIFILES_GID=1000
```

Either way, `scripts/preflight.sh` verifies the two agree before starting and
prints the exact fix if they do not.

> **On Fedora, RHEL, CentOS, and Rocky**, SELinux also has to allow the bind
> mount. The Compose file already mounts with `:z`, which asks Docker to apply
> the right label. Without it, the container reports a bare `permission denied`
> that looks exactly like a wrong-ownership problem. Raspberry Pi OS, Debian,
> and Ubuntu use AppArmor and are unaffected.

## 5. Layout

PiFiles creates and manages this structure beneath `STORAGE_ROOT`:

```text
/srv/pifiles/
├── files/        committed file content
├── versions/     superseded content kept by version history
├── trash/        reserved for soft-deleted content
├── previews/     generated thumbnails (derived; safe to delete)
├── temp/         in-flight chunked uploads
├── quarantine/   content ClamAV rejected
└── backups/      database dumps written by the backup script
```

Do not add, rename, move, or delete anything inside these directories by hand.
The database is the authority on what exists; a file the database does not know
about is invisible to users, and a database row whose content is missing is a
broken file. The storage-reconciliation job (Phase 7) detects both.

## 6. How files are named on disk

A file's on-disk path is derived from the SHA-256 digest of its contents, never
from its name:

```text
Report.pdf  →  /srv/pifiles/files/ab/cd/abcdef0123456789…
```

Browsing the storage directory therefore shows only hex-named files. The mapping
back to `Report.pdf` lives in the database.

This is deliberate. Because the only characters that ever reach a filesystem
call are `[0-9a-f/]`, an entire class of attacks and bugs cannot occur:
path traversal, symlink escape, Unicode normalisation mismatches, reserved
Windows names, absurdly long names, and collisions between files that differ
only in case.

A useful side effect: two users uploading identical content share one blob.

## 7. Capacity and free space

`STORAGE_MIN_FREE` (default `1GB`) is a floor. Uploads are refused once free
space would fall below it, so the database and the operating system cannot be
starved by user content.

The health endpoint reports `degraded` as the volume approaches that floor and
`unhealthy` once it is breached — visible on the system status page.

## 8. Expanding storage

To move to a larger drive:

1. Stop the stack: `make down`
2. Copy everything, preserving ownership and hard links:
   ```sh
   sudo rsync -aHAX --info=progress2 /srv/pifiles/ /mnt/new-drive/pifiles/
   ```
   `-H` matters: content is deduplicated with hard links, and omitting it can
   multiply the space used.
3. Point `STORAGE_ROOT` at the new location in `.env`, or update `/etc/fstab`
   to mount the new drive at the same path.
4. `make up`

Verify the file count matches before removing the old drive.
