# EventRelay dashboard

The M4 console is implemented and verified locally on the published feature branch. Its CI dependency
audit is blocked by an unpatched Picomatch advisory; see the validation record below.
It uses React, TypeScript, Vite and repository-owned CSS. The Go API embeds committed assets from
`internal/dashboard/dist` and serves them at `/dashboard/`.

## Toolchain and checks

Use Node 22.22.3/npm 10.9.8. Runtime dependencies are React/ReactDOM 19.3.1. Development pins are
TypeScript 7.0.2, Vite 8.3.8, Vitest 5.1.0, Playwright 1.65.1, Biome 2.6.8,
Testing Library React 16.3.3, jsdom 31.0.1 and Axe Playwright 4.14.0; exact pins live in `package.json`.

From the repository root:

```bash
make frontend-install       # npm ci --ignore-scripts using the committed lockfile
make frontend-check         # type, lint, unit, build, license and npm audit gates
(cd web && npx playwright install chromium)
make frontend-browser       # requires disposable test PostgreSQL/Redis URLs
make frontend-assets-check  # rebuild and reject drift in tracked embedded assets
```

`npm --prefix web run format` applies Biome formatting. `npm --prefix web run dev` starts the loopback
Vite server, proxying `/dashboard/api` to the API at `127.0.0.1:8080`. Open its printed `/dashboard/` URL.
The normal embedded dashboard needs only Go; Node is not used by either deployment binary or Docker build.

CI installs the locked dependencies, runs these gates on Ubuntu and checks rebuilt assets against Git.
The existing Go matrix consumes committed assets. The first M4 push CI passed all Go/backend jobs and
frontend type/lint/unit/build/license checks, then failed the npm audit before asset/browser validation.

## Browser lifecycle and data

The management token is entered at login and exchanged for an HttpOnly/SameSite=Strict session cookie.
The API retains the session and a separate CSRF nonce; the client retains the nonce in memory.
Sessions last 30 minutes and end on logout or API restart. No credentials, payloads or page caches are
written to local/session storage.

Views fetch one page per resource with a four-second client deadline, poll five seconds after completion
and abort on navigation. Hidden
tabs pause polling; stale responses are suppressed even if a transport ignores cancellation. Cursor
history is capped at 20 entries. Payload and diagnostic text use React text rendering under a same-origin
CSP. Replay conflicts are shown once and require a refreshed, explicit action.

Playwright starts `tests/browser`, a loopback-only synthetic fixture with a unique schema/Redis key.
Global teardown waits up to 30 seconds for the Go child to exit after cleanup, checks its exit code and
uses an atomic ignored completion marker. Test screenshots/profiles and fixture runtime files are ignored.

## Dependency licenses

`make frontend-licenses` checks declared licenses for all 155 locked packages. The allowlist in
`scripts/licenses.mjs` includes permissive licenses and the explicitly reviewed MPL-2.0 development
tools (`@axe-core/playwright`, `axe-core` and Lightning CSS packages). This is a declared-license check.
The build emits the React, ReactDOM and Vite-distributed license notices into
`internal/dashboard/dist/THIRD-PARTY-LICENSES.txt`.

The [usage/upgrade guide](../docs/guides/inspection-and-replay.md),
[ADR 0006](../docs/adr/0006-m4-replay-and-inspection.md) and
[validation record](../docs/testing/m4-inspection-replay-dashboard.md) describe the API, bounds and evidence.
