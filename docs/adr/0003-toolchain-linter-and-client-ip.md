# ADR 0003 - Toolchain, linter and client-IP decisions

- Status: accepted
- Date: 2026-11-13
- Deciders: Aditya

## Context

The first CI run on GitHub, on 2026-11-13, failed in two jobs that had never run before. Neither
failure was an application bug, and both were invisible until a real run happened:

- `govulncheck` reported three standard-library vulnerabilities. `actions/setup-go` had installed
  exactly Go 1.26.0, because the `go` directive in `go.mod` names a patch version and setup-go
  installs that exact patch. Go 1.26.0 carries `GO-2026-6859`, `GO-2026-6809` and `GO-2026-6617`.
- The lint job could not start at all. `golangci/golangci-lint-action@v6` with `version: latest`
  installs the newest release of the *v1* line, v1.64.8, which is built with Go 1.24 and refuses to
  analyse a module whose `go` directive is 1.26.

The same run surfaced one finding that was a defect in the code rather than in the tooling:
`chi`'s `middleware.RealIP` is deprecated because it copies `X-Forwarded-For` into `r.RemoteAddr`
whether or not a proxy in front of the service set it.

## Decisions

1. **`go.mod` declares `go 1.26.0` and `toolchain go1.26.11`.** The `go` directive is the minimum
   language version, and the dependency graph forces it to name a patch. The `toolchain` directive is
   the exact toolchain CI installs, and `actions/setup-go` prefers it over the `go` directive. Without
   the `toolchain` line, setup-go installs 1.26.0 exactly and the vulnerability scan fails again.
   `go mod tidy` preserves both lines.
2. **The linter is golangci-lint v2.15.0, pinned by version, run by `golangci/golangci-lint-action@v9`.**
   The v1 line cannot analyse this module at all. The version is pinned rather than `latest` so that a
   new release cannot turn a green build red without a commit.
3. **`middleware.RealIP` is removed.** The request log records the peer address in the `remote_addr`
   field. `X-Forwarded-For`, `True-Client-IP` and `X-Real-IP` are chosen by the client, so they are
   never used to attribute a request to an address.
4. **Client-IP attribution behind a reverse proxy is not implemented.** It needs a trusted-proxy
   allowlist and a defined header contract. Guessing it is how spoofing bugs get shipped, so it is
   backlog item `ER-037` and not a feature of M1.

## Consequences

- CI installs Go 1.26.11 on every platform and in every job. The `go version` line in a CI log is
  the exact toolchain the result was produced with.
- The standard-library vulnerabilities the first run reported are gone: `govulncheck ./...` under
  Go 1.26.11 reports no vulnerabilities for this module.
- The lint job runs the same linter major version as the other Go projects in the portfolio. Two
  findings were fixed rather than suppressed: the deprecated middleware above and one unchecked error
  return in the migration runner.
- A request that arrives through a reverse proxy is logged with the proxy's address, not the client's.
  That is the address the server actually saw. The client's address is not known, and inferring it
  without a trust boundary is the bug this decision removes.
