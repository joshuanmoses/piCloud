# Raspberry Pi

PiFiles targets the Raspberry Pi 4 and 5 running 64-bit Raspberry Pi OS Lite.
This page covers what is specific to that hardware. For general installation see
[installation.md](installation.md).

## Hardware

| | Minimum | Recommended |
| --- | --- | --- |
| Board | Pi 4, 4 GB | Pi 5, 8 GB |
| Boot media | 16 GB microSD (A2 class) | 32 GB microSD, or NVMe boot |
| Data storage | USB 3 SSD | NVMe via HAT (Pi 5), or USB 3 SATA SSD |
| Power | Official supply | Official supply |
| Cooling | Heatsink | Active cooler |

Two of these matter more than they look:

**Power.** Under-voltage is the most common cause of mysterious instability —
random I/O errors, containers being killed, filesystem corruption. Use the
official supply (5 V/3 A for the Pi 4, 5 V/5 A for the Pi 5). Check with:

```sh
vcgencmd get_throttled     # 0x0 is good; anything else indicates a problem
```

**Cooling.** A Pi under sustained load throttles at 80 °C, and uploading a large
file is sustained load. A passive heatsink is enough for a Pi 4; a Pi 5 benefits
from the active cooler.

```sh
vcgencmd measure_temp
```

## Operating system

Use **Raspberry Pi OS Lite (64-bit)**. Not the desktop image — a desktop
consumes RAM this workload needs. Not the 32-bit image — PiFiles does not
support `armv7l`, and a 32-bit userland cannot address more than 4 GB anyway.

Verify:

```sh
uname -m        # must print aarch64
```

Use Raspberry Pi Imager and, before writing, open the settings gear to set the
hostname, enable SSH, and configure your user and Wi-Fi. That gives a headless
machine you can reach immediately.

## Preparation

```sh
sudo apt update && sudo apt full-upgrade -y
sudo reboot
```

### Boot from the SSD (optional, recommended)

Both boards can boot from USB or NVMe, which removes the SD card as a failure
point entirely. Use `sudo raspi-config` → Advanced Options → Boot Order.

Even when booting from SSD, keep user files on a **separate** partition or drive
from the operating system, so a reinstall never risks them.

### Reduce writes to the boot media

If you are booting from microSD, moving logs to RAM meaningfully extends its
life:

```sh
sudo apt install -y log2ram
```

The Compose file already caps container log files at 10 MB each with three
rotations, which is the other main source of write churn.

### Swap

The default 100 MB swap file is small enough to be useless and, on an SD card,
harmful. Either disable it and rely on adequate RAM:

```sh
sudo dphys-swapfile swapoff && sudo systemctl disable dphys-swapfile
```

…or, better, put a larger swap file on the SSD. Never leave heavy swap on the
SD card.

## Installing PiFiles

Follow [installation.md](installation.md). Two Pi-specific notes:

**Build on the Pi, or build elsewhere.** Building the image on a Pi 4 takes
several minutes, most of it compiling the frontend. To build on a faster machine
instead:

```sh
# On your workstation
make docker-build-pi
docker save pifiles/app:1.0.0-dev-arm64 | gzip > pifiles.tar.gz
scp pifiles.tar.gz pi@raspberrypi.local:~

# On the Pi
gunzip -c pifiles.tar.gz | docker load
```

**Mount the drive before starting.** `scripts/preflight.sh` refuses to start if
`STORAGE_ROOT` is missing, and warns if it resolves to the SD card.

## Performance

The defaults are already tuned for a 4 GB board:

| | Default | Why |
| --- | --- | --- |
| PostgreSQL `shared_buffers` | 128 MB | Leaves room for the app, Caddy, and the page cache |
| PostgreSQL `max_connections` | 50 | Each costs 5–10 MB |
| Application pool | 10 | More connections do not make a Pi faster |
| Background workers | 2 | Keeps thumbnails from starving interactive requests |
| Upload chunk size | 16 MB | Bounds memory per concurrent upload |
| I/O buffers | 256 KB | File size never affects memory use |

Full rationale for the database settings, and how to adjust for an 8 GB board or
a spinning disk, is in `deploy/docker/postgres/tuning.md`.

### Watching resource use

```sh
docker stats                 # per-container CPU and memory
free -h                      # system memory
vcgencmd measure_temp        # temperature
vcgencmd get_throttled       # 0x0 means no power or thermal throttling
iostat -x 5                  # disk latency; needs `apt install sysstat`
```

### If it feels slow

1. **Check for throttling first.** `get_throttled` returning anything but `0x0`
   means the power supply or cooling is the problem, and no amount of tuning
   will help.
2. **Check the drive.** A USB 2 port, a poor cable, or an unpowered spinning
   disk will dominate everything else. `hdparm -t /dev/sda` should show well
   over 100 MB/s on a USB 3 SSD.
3. **Check memory pressure.** If `free -h` shows little available and swap in
   use, lower `DATABASE_MAX_CONNS` or `UPLOAD_CHUNK_SIZE`.
4. **Only then tune PostgreSQL.**

### Expected throughput

On a Pi 4 with a USB 3 SSD and gigabit Ethernet, expect roughly 70–110 MB/s for
uploads and downloads — the network is usually the limit, not the board. A Pi 5
with NVMe saturates gigabit comfortably. Wi-Fi will be substantially slower and
much less consistent.

### What has actually been measured

**Not on a Raspberry Pi.** PiFiles has not yet run on the hardware it is named
for. What follows is measured on a development machine, and is included because
some of it transfers and some of it does not — the distinction matters more than
the numbers.

Run them yourself with `make bench`.

| Operation | Measured | What it means on a Pi |
| --- | --- | --- |
| Folder listing, 10 items | 1.8 ms | Fixed cost is about six database round trips |
| Folder listing, 100 items | 3.0 ms | The default page size |
| Folder listing, 500 items | 8.7 ms | The maximum page size |
| Permission check, depth 1 | 0.16 ms | Runs on every content request |
| Permission check, depth 32 | 0.29 ms | Cost grows slowly with tree depth |
| Batched check, 100 items | 0.60 ms | 6 µs per item — the batching is real |
| Search over 500 files | 2.7 ms | Trigram scan, index-backed |
| Upload, 1 KiB | 3.1 ms | Fixed cost: quota checks and the metadata transaction |
| Upload, 8 MiB | 7.5 ms | Now bytes-bound rather than fixed-cost-bound |
| Folder ZIP, 100 × 32 KB | 10 ms | Streamed, one entry at a time |

**What transfers to a Pi:** the *shapes*. A listing is a fixed number of queries
rather than one per row; a batched permission check costs 6 µs per item at 100
and 24 µs at 10, so it is genuinely sub-linear; a deep tree costs less than
twice a shallow one. Those hold regardless of clock speed.

**What does not transfer:** every absolute number. A Pi 4's cores are roughly
five to eight times slower for this kind of work, and its storage very much
more so. Read the table as "a 100-item listing is about six queries", not as
"a 100-item listing takes 3 ms".

**What is still unknown, and needs the real board:** sustained upload
throughput to USB storage, thumbnail generation time for a large photograph,
behaviour under thermal throttling, and whether the 10-connection pool is right
when four slow cores are contending for it.

### Memory under load

The claim that file size never affects memory use is the one worth checking,
because it is the assumption a 4 GB board depends on. Both halves were verified
with the stack running under Pi-4 memory caps:

```sh
make up-pi-limits     # app 768 MB, PostgreSQL 1 GB, Caddy 256 MB
docker stats
```

| Test | Result |
| --- | --- |
| A single 2 GB upload | Resident memory went from 133 MiB to 136 MiB |
| Four concurrent 2 GB downloads (8 GB total) | Peaked at 153 MiB, settled back to 140 MiB |

The allocation profile says the same thing more precisely: an upload allocates
about 304 KB whether the file is 1 KiB, 1 MiB, or 8 MiB — the fixed 256 KB copy
buffer plus request overhead, and nothing proportional to the content.

(The download benchmark reports a large per-operation allocation. That is the
test harness buffering the response body in memory to assert on it, not the
server, which streams.)

## Networking

Give the Pi a static address, or a DHCP reservation on your router, so its
address does not change. Prefer Ethernet: Wi-Fi throughput on a Pi is variable
and a file server exposes that immediately.

To reach it by name from other devices, `pifiles.local` works out of the box on
most networks via mDNS (Avahi is preinstalled on Raspberry Pi OS).

## Known constraints

- **This has not been run on a Raspberry Pi.** The ARM64 binary and container
  image are built and verified to start under emulation, and the memory
  behaviour is verified under Pi-sized caps — but nobody has yet booted a real
  board with it. Treat throughput figures as estimates until that changes.
- **32-bit ARM is not supported.** Reinstall with the 64-bit image.
- **ClamAV is impractical below 8 GB.** Its signature database alone needs
  roughly 1 GB. It is disabled by default for this reason.
- **Office-document previews are not included.** LibreOffice conversion is far
  too heavy for this hardware; the preview subsystem is designed to accept it
  later on capable machines.
- **Network filesystems are not supported** for `STORAGE_ROOT`. File locking and
  root confinement do not behave reliably over NFS or SMB.
