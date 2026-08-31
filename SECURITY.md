# Security policy

PiFiles stores people's files and controls who may read them. Security reports
are welcome, and they are read.

## Reporting a vulnerability

**Please do not open a public issue for a security problem.**

Report it privately through GitHub's private vulnerability reporting:

**https://github.com/joshuanmoses/piCloud/security/advisories/new**

(the **Security** tab of this repository, then **Report a vulnerability**).

Include whatever you have. A rough description of the class of problem is more
useful than nothing:

- what an attacker can do that they should not be able to
- the smallest sequence of requests or steps that demonstrates it
- the version (`/api/v1/version`, or the commit you built)
- whether you needed an account, and if so what kind

You will get an acknowledgement. If the report is valid you will be told what
the fix is and when it lands, and you will be credited in the advisory unless
you would rather not be.

## Scope

PiFiles is self-hosted, so the deployment is part of the security boundary.

**In scope** — anything in this repository:

- reading, writing, or deleting content the permission model should have
  prevented, including through shares, groups, denials, public links, and the
  anonymous upload portal
- authentication and session handling: sign-in, lockout, CSRF, cookie
  attributes, session revocation
- escaping the storage root, or getting user-controlled text into a filesystem
  path
- injection into SQL, HTTP headers, ZIP archives, or shell commands
- learning that a file or account exists when you should not be able to
- secrets appearing in logs, error messages, or API responses
- the installer, backup, restore, and upgrade scripts destroying or exposing
  data

**Out of scope:**

- anything requiring host root, physical access, or a compromised administrator
  account — an administrator can already replace the binary
- vulnerabilities in PostgreSQL, Caddy, Docker, or the operating system, unless
  PiFiles configures them insecurely
- denial of service by exhausting disk or bandwidth from an authenticated
  account, which quotas bound but do not eliminate
- missing hardening that
  [docs/security.md](docs/security.md) already documents as absent. That file's
  "What PiFiles does not defend against" section is deliberate, not an
  oversight. If you think something in it is wrong, that is worth reporting —
  as an argument, not as a finding.

## Supported versions

PiFiles has not had a tagged release yet. Until it does, fixes land on `main`,
and `git pull && make upgrade` is how you get them.

## What PiFiles already does

[docs/security.md](docs/security.md) is the threat model: what is defended,
how, and — in its own section — what is not. Reading it first will tell you
whether something is a finding or a documented limit.
