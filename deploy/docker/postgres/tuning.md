# PostgreSQL tuning for Raspberry Pi

The settings applied in `docker-compose.yml` are chosen for a Raspberry Pi 4 or
5 with 4–8 GB of RAM, running PostgreSQL alongside the application, Caddy, and
the operating system — not for a dedicated database server.

They are passed as `-c` command-line overrides rather than through a mounted
`postgresql.conf`. Command-line options take precedence over every file-based
setting, they require no `data_directory` coordination with the image's
entrypoint, and `docker compose config` shows exactly what is in effect.

| Setting | Value | Why |
| --- | --- | --- |
| `max_connections` | 50 | The application pool defaults to at most 10. The remainder is headroom for `psql`, backups, and a second instance during an upgrade. Each connection costs roughly 5–10 MB. |
| `shared_buffers` | 128MB | About 3% of a 4 GB board. The conventional 25% guidance assumes a dedicated server; here the page cache serves the same purpose more flexibly. |
| `effective_cache_size` | 768MB | A hint, not an allocation. Tells the planner roughly how much OS page cache it can expect, which makes it prefer index scans. |
| `work_mem` | 4MB | Allocated *per sort or hash node*, so a complex query can use several multiples. Small values keep a burst of concurrent queries from exhausting RAM. |
| `maintenance_work_mem` | 64MB | Speeds up `VACUUM` and index builds, which run one at a time. |
| `random_page_cost` | 1.1 | The default of 4.0 models a spinning disk. PiFiles requires SSD or NVMe storage, where random reads cost nearly the same as sequential ones. Leaving this at 4.0 causes needless sequential scans. |
| `effective_io_concurrency` | 200 | Flash storage services many concurrent requests. On a USB HDD, lower this to 2. |
| `wal_compression` | on | Trades a little CPU for less write volume — a good trade when the write path is USB-attached storage. |
| `max_wal_size` | 1GB | Fewer, larger checkpoints rather than frequent small ones. |
| `checkpoint_completion_target` | 0.9 | Spreads checkpoint writes out so they do not appear as an I/O spike. |
| `synchronous_commit` | on | **Deliberately left at the safe default.** Turning it off would speed up writes but can lose recently committed transactions after a power cut — and unexpected power loss is a normal event for a Raspberry Pi. Losing file metadata that the user was told was saved is not an acceptable trade. |
| `max_worker_processes` | 4 | Matches the core count. |
| `max_parallel_workers_per_gather` | 1 | Parallel query on four small cores mostly adds coordination overhead. |
| `autovacuum_vacuum_scale_factor` | 0.05 | More frequent, smaller vacuums. The `audit_events` and `recent_items` tables churn heavily. |
| `jit` | off | JIT compilation costs more than it saves for the short, indexed queries this workload consists of. |

## Adjusting for your hardware

**8 GB Raspberry Pi 5, storage on NVMe** — the defaults are already reasonable;
you may raise `shared_buffers` to `256MB` and `effective_cache_size` to `2GB`.

**Storage on a USB hard disk rather than flash** — set
`-c random_page_cost=2.5` and `-c effective_io_concurrency=2`.

Apply changes by editing the `command:` block in `docker-compose.yml` and
running `docker compose up -d postgres`.
