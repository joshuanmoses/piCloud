# Authentication

PiFiles maintains its own user database. Accounts have no relationship to
operating-system users: there is no entry in `/etc/passwd`, no shell, no home
directory, and no PAM involvement. Compromising an application account grants
access to files through the API and nothing else.

---

## 1. Passwords

### Hashing

Passwords are stored as **Argon2id** hashes in PHC string format:

```text
$argon2id$v=19$m=65536,t=2,p=2$<base64 salt>$<base64 hash>
```

Argon2id is chosen over bcrypt and PBKDF2 because it is *memory*-hard: an
attacker with a GPU or purpose-built hardware gains far less advantage than raw
hashing speed would suggest.

| Parameter | Value | Reasoning |
| --- | --- | --- |
| Memory | 64 MiB | Comfortably above the OWASP baseline of 19 MiB. Memory is what resists parallel cracking hardware, so it is raised in preference to iteration count. |
| Iterations | 2 | With 64 MiB, two passes take a few hundred milliseconds on a Pi 4 — unnoticeable to a person signing in, expensive to an attacker doing it billions of times. |
| Parallelism | 2 | Capped to the core count at startup; more lanes than cores gains nothing. |
| Salt | 16 bytes, random per password | Two users choosing the same password produce different hashes, so one breach does not reveal every account sharing a common password. |
| Output | 32 bytes | |

Memory is the binding constraint on a 4 GB board. It cannot afford many
concurrent 256 MiB hashes, and sign-in is exactly where an attacker would try to
force that — which is why the rate limiter sits in front of it.

Because the parameters travel inside the hash, the cost can be raised later
without invalidating existing passwords. A hash made with weaker parameters is
detected at the next successful sign-in — the only moment the plaintext exists —
and transparently upgraded.

### Policy

Following NIST SP 800-63B, **length is what is enforced** and composition rules
are not. Requiring a symbol and a digit measurably pushes people toward
predictable patterns like `Password1!`.

| Rule | Default |
| --- | --- |
| Minimum length | 12 characters (`PASSWORD_MIN_LENGTH`, floor of 8) |
| Maximum length | 256 characters, purely to bound hashing work |
| Blocked | The most common breach-corpus passwords |
| Blocked | Anything containing the username, display name, or email |
| Blocked | A single repeated character, a simple sequence, or a short pattern repeated |

Sequence detection compares digits modulo 10, so `123456789012` — which wraps
from 9 back to 0 — is recognised for what it is.

### Never logged

Passwords, session tokens, link tokens, and API secrets are never written to a
log at any level. Three independent mechanisms enforce this:

1. `config.Secret` cannot be printed, serialised, or logged — only `Reveal()`
   returns the value, which makes every legitimate use grep-able.
2. The logging layer redacts any attribute whose name looks sensitive, in both
   console and JSON formats, including through `With()` and groups.
3. The pgx tracer never logs query arguments, which routinely contain hashes.

---

## 2. Sessions

Sessions are **server-side records**, not self-contained tokens.

A JWT cannot be revoked before it expires. An administrator disabling a
compromised account must be able to end its sessions immediately, and a user
must be able to see their devices and sign one out. That requires server state,
and on a single-node file server a database lookup per request is negligible.

### Tokens

- 256 bits from `crypto/rand`, base64url-encoded.
- Only a **SHA-256 hash** is stored. A leaked database backup yields no usable
  credentials.
- SHA-256 rather than Argon2 is correct here: the input is already uniform
  randomness, so there is no dictionary to attack, and this runs on every
  authenticated request.
- Malformed tokens are rejected on shape before any database round trip, so a
  flood of garbage cannot be turned into database load.

### Cookies

| Cookie | HttpOnly | Purpose |
| --- | --- | --- |
| `pifiles_session` | **Yes** | The credential. Unreadable by scripts. |
| `pifiles_csrf` | No | Read by the frontend and echoed in a header. Useless without the session cookie. |

Both are `SameSite=Lax` and `Secure` in production.

`Lax` rather than `Strict` is deliberate: `Strict` would mean a user following a
share link from an email arrives signed out, which reads as broken. `Lax` still
blocks cross-site POSTs, and the CSRF token covers the remainder.

Without "keep me signed in", no `Max-Age` is set, so the cookie disappears when
the browser closes — the right default on a shared computer.

### Lifetimes

| Setting | Default |
| --- | --- |
| `SESSION_LIFETIME` | 24h |
| `SESSION_REMEMBER_LIFETIME` | 30d |
| `SESSION_IDLE_TIMEOUT` | off |
| `SESSION_MAX_PER_USER` | 25 |

The absolute expiry is never extended. The per-user cap prevents a script that
signs in repeatedly from growing the table without limit.

### Revocation

A session ends immediately when any of these happen:

- The user signs out, or revokes it from their account page.
- The password is changed — every *other* session ends, which is the entire
  point of changing a password you think someone else knows.
- An administrator resets the password or disables the account.
- The account is locked by repeated failures.

A disabled account also loses access on its very next request, without waiting
for the session to expire.

---

## 3. Sign-in

The flow is deliberately uniform in both cost and message:

```text
rate limit (per IP, per username)
        ↓
look up the account
        ↓  not found → hash a dummy password anyway, return "invalid"
check the lockout state
        ↓  locked → return "locked" without hashing
verify the password
        ↓  wrong → increment the counter, return "invalid"
check the account is enabled
        ↓  disabled → return "disabled"
upgrade the hash if the parameters have been raised
        ↓
create the session, stamp the login, reset the counters
```

Two properties matter and are both tested:

**An unknown username and a wrong password are indistinguishable.** Identical
status, identical code, identical message. The unknown case still performs a
hash computation so the response time does not reveal which accounts exist.

**Whether an account is disabled is only revealed after the password is
verified.** Otherwise the endpoint would let anyone probe which accounts exist
and are suspended.

### Lockout and rate limiting

| | Purpose |
| --- | --- |
| **Account lockout** (`MAX_LOGIN_ATTEMPTS`, `LOCKOUT_DURATION`) | Protects one account against guessing. Persisted in the database, so it survives a restart. |
| **Per-IP rate limit** | Protects the server against a flood from one source. In-memory. |
| **Per-username rate limit** | Protects one account against a distributed attack spread across many addresses. In-memory. |

The counters are in-memory and per-process on purpose: PiFiles runs as a single
application container, so a shared store would add a dependency for no benefit.
Losing them on restart is acceptable because lockout — the durable half — lives
in the database. The limiter also caps how many keys it tracks, so an attacker
rotating source addresses cannot turn the defence itself into a memory
exhaustion attack.

Locking an account also **revokes its existing sessions**: repeated failures may
mean the account is under attack, and a session opened earlier could be the
attacker's.

---

## 4. CSRF protection

A signed double-submit cookie.

The token is `HMAC-SHA256(derived key, session ID)`, delivered in a
JavaScript-readable cookie and echoed back in an `X-CSRF-Token` header. A
cross-site attacker can cause the browser to send the session cookie, but the
same-origin policy prevents them reading the CSRF cookie, so they cannot
populate the header.

Deriving the token from the session ID rather than storing a random value means
no extra column and no lookup — it is recomputed and compared in constant time
on each request.

The signing key is derived from `APP_SECRET_KEY` with a purpose string, so a
token from one subsystem can never be valid in another.

Applied to every state-changing method. `GET`, `HEAD`, and `OPTIONS` are exempt:
they are defined to be safe, and a handler that mutates state on a `GET` is a bug
in its own right.

---

## 5. The first administrator

PiFiles ships with **no accounts and no default password**. There are two ways to
create the first administrator:

**From a browser.** Open the instance and complete the setup form. This endpoint
exists only while the instance has zero users; afterwards it returns 404
permanently, with no flag to reset.

**From the command line**, for the installer or a scripted deployment:

```sh
docker compose exec app pifiles -create-admin
# or
make create-admin
```

The password is prompted for and never echoed. It is deliberately not accepted
as a command-line argument, because arguments are visible in `ps` output and land
in shell history.

> **The window before setup is genuinely unauthenticated.** Between first start
> and first sign-in, whoever reaches the server can claim it. That is
> unavoidable — the instance must be reachable before an administrator can exist
> — so the server logs a prominent warning on every start until it is closed.
> Complete setup before connecting the machine to an untrusted network.

---

## 6. Password changes

**Self-service** requires the current password. Without that, anyone who borrows
an unlocked browser could lock the real owner out of their own account. The new
password must satisfy the policy and differ from the current one. Every other
session is revoked; the one making the change survives.

**Administrator reset** does not require the current password, revokes every
session for that user, unlocks the account, and can set a flag forcing a change
at next sign-in. That flag is enforced server-side: until the password is
changed, the API returns `password_change_required` for everything except the
password-change endpoint itself. Otherwise the reset would be advisory.

---

## 7. What is not implemented yet

| | Phase |
| --- | --- |
| Two-factor authentication | Not scheduled for v1 |
| Password reset by email | Not scheduled for v1 — administrators reset passwords |
| OIDC / LDAP / SAML | Not scheduled for v1 |
| API tokens for scripts | Under consideration for after v1 |

There is deliberately no self-service password recovery. It would require
outbound email, which most home deployments cannot send reliably, and an email
account becomes a second path into the file server.
