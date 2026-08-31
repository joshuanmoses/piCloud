# Installation

For Raspberry Pi specifics, read [raspberry-pi.md](raspberry-pi.md) alongside
this. For mounting the drive your files will live on, read
[storage.md](storage.md) first — it is the step most installations get wrong.

## Supported systems

| | |
| --- | --- |
| **Primary** | Raspberry Pi OS Lite 64-bit, Debian 12+ (ARM64) |
| **Also supported** | Ubuntu 22.04+ (ARM64), any Linux x86_64 |
| **Not supported** | 32-bit ARM (`armv7l`/`armhf`), Windows, macOS as a host |

Minimum 4 GB RAM; 8 GB recommended. Storage on an external SSD, NVMe, or USB
disk — not the microSD card.

## 1. Install Docker

```sh
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker $USER
```

**Log out and back in** for the group change to take effect, then verify:

```sh
docker run --rm hello-world
docker compose version
```

If `docker compose version` fails, install the plugin:

```sh
sudo apt update && sudo apt install -y docker-compose-plugin
```

## 2. Prepare storage

Mount your external drive and create the data directory. The full procedure,
including making the mount survive a reboot, is in [storage.md](storage.md).

```sh
sudo mkdir -p /srv/pifiles
```

## 3. Get PiFiles

```sh
git clone https://github.com/joshuanmoses/piCloud.git
cd piCloud
```

## 4. Configure

```sh
./scripts/generate-secrets.sh
```

This creates `.env` from `.env.example`, generates the two required secrets,
sets the container's user to match your account, and restricts the file to mode
600. It is safe to run again; it never overwrites a value you have set.

Now open `.env` and set two things:

```ini
# Where user files live. Point this at your external drive.
STORAGE_ROOT=/srv/pifiles

# The address people will type into a browser. It must match exactly,
# because share links are built from it.
APP_URL=http://pifiles.local
```

Everything else has a working default. `.env.example` documents each setting.

## 5. Check the machine is ready

```sh
./scripts/preflight.sh
```

This verifies Docker, the architecture, available memory, the secrets, the
storage directory's existence and ownership, and that the ports are free. Every
problem it reports comes with the exact command that fixes it.

It changes nothing on your system and never touches a disk.

## 6. Start

```sh
make up
```

The first run builds the images, which takes a few minutes on a Pi. Afterwards
it starts in seconds. Database migrations are applied automatically.

When everything reports healthy, the URL is printed. To see it again:

```sh
make url
make health
```

## 7. Create the first administrator

**Arrives in Phase 2.** PiFiles deliberately ships with no default account and
no default password. Once authentication lands, the installer will prompt for
the first administrator's username and password on first run.

## Enabling HTTPS

### A public domain

Point a DNS `A` record at the machine, forward ports 80 and 443 to it, then set
in `.env`:

```ini
CADDY_SITE_ADDRESS=files.example.com
APP_URL=https://files.example.com
SESSION_COOKIE_SECURE=true
```

Restart with `make up`. Caddy obtains a certificate from Let's Encrypt and
renews it automatically. Nothing else is required.

To receive expiry notices, also set:

```ini
CADDY_GLOBAL_OPTIONS=email you@example.com
```

### A local network name

```ini
CADDY_SITE_ADDRESS=pifiles.local
APP_URL=https://pifiles.local
```

Caddy issues a certificate from a local authority. Browsers will warn until you
install Caddy's root certificate on each device — Caddy prints its location on
first start.

### Plain HTTP on a trusted LAN

Acceptable on a home network you control, but session cookies then travel
unencrypted:

```ini
CADDY_SITE_ADDRESS=:80
APP_URL=http://pifiles.local
SESSION_COOKIE_SECURE=false
```

`SESSION_COOKIE_SECURE=false` is required here — a `Secure` cookie is never sent
over plain HTTP, so leaving it `true` would make login silently fail.

PiFiles never sends `Strict-Transport-Security` over a plain-HTTP connection, so
choosing this does not lock your browser out of the instance later.

## Starting at boot

```sh
sudo cp deploy/systemd/pifiles.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now pifiles
```

Edit the unit first if you cloned somewhere other than `/opt/pifiles`.

## Upgrading

```sh
cd /opt/pifiles
git pull
make up
```

Migrations run automatically at startup. Back up first — see the README.

## Uninstalling

```sh
make down                    # stop, keeping all data
docker compose down -v       # also delete the database volume
```

User files under `STORAGE_ROOT` are never touched by either command. Delete
that directory yourself if you want the files gone.
