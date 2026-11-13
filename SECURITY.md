# Security Policy

## Supported versions

EventRelay is pre-1.0. Only the latest commit on `main` receives security fixes.

## Reporting a vulnerability

Do **not** open a public issue. Use GitHub's private vulnerability reporting:
https://github.com/shellghostt/eventrelay/security/advisories/new

Please include:

- affected version or commit,
- a minimal reproduction,
- the impact you believe it has,
- any suggested fix.

You can expect an acknowledgement within 72 hours and an assessment within 7 days. This is a
maintainer-funded project, so there is no bug bounty, but credit will be given in the advisory unless you
prefer otherwise.

## Scope

In scope: signature verification bypass, authentication and authorisation flaws, injection, unsafe
deserialisation, secret leakage in logs, privilege escalation through automated fixes, dependency
confusion, supply-chain integrity of release artifacts.

Out of scope: findings that require a compromised host, missing security headers on a development server,
denial of service through unbounded legitimate input without a documented limit, and reports produced only
by an automated scanner with no analysis.

## Hardening expectations

- Dependencies are scanned in CI with `govulncheck`.
- The build toolchain is pinned in `go.mod` and is on a supported Go release line.
- Secrets are never logged. Payload logging is opt-in and redacted.
- Every release artifact is produced by CI, not by a laptop.
