# Contributing to EventRelay

Thanks for taking the time. This document is deliberately short and specific.

## Read first

1. [docs/PLAN.md](docs/PLAN.md) - the authoritative specification. It explains scope, architecture,
   milestones and what is explicitly out of scope.
2. [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) - how the pieces fit together.
3. [docs/adr/](docs/adr/) - decisions that have already been argued about. Do not relitigate them
   without a new ADR.
4. [docs/phases/M4.md](docs/phases/M4.md) - M4 scope, dependency order and evidence;
   preserve the [M3 contract](docs/phases/M3.md) and accepted ADR 0006.
5. [docs/DEVELOPMENT-TOOLS.md](docs/DEVELOPMENT-TOOLS.md) - CodeGraph freshness, static-analysis coverage
   and profiling appropriate to this repository's runtime.

## Ground rules

- Be decent. See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
- Security issues never go in a public issue. See [SECURITY.md](SECURITY.md).
- One logical change per pull request. If the description needs "and also", split it.
- Do not invent benchmarks, screenshots or feature claims. If it is not measured, it is not stated.

## Development setup

```bash
git clone https://github.com/shellghostt/eventrelay.git
cd EventRelay
make bootstrap   # install tooling and dependencies
make test        # unit tests
make lint        # formatting and static analysis
```

Integration tests need real PostgreSQL and Redis services and are skipped without the relevant URLs:

```bash
docker compose -f deploy/docker-compose.yml up -d postgres redis
EVENTRELAY_TEST_DATABASE_URL='postgres://eventrelay:eventrelay@localhost:55434/eventrelay?sslmode=disable' \
EVENTRELAY_TEST_REDIS_URL='redis://localhost:56380/0' \
  go test -race -count=1 -timeout 120s ./internal/store ./tests/integration
```

Use disposable services: delivery tests isolate their schema/key, while the M1 store test uses the
default schema. [deploy/README.md](deploy/README.md) includes Windows commands, the delivery demo and load tests.

Go-only builds and tests use committed dashboard assets. Frontend changes require Node 22.22.3/npm
10.9.8 and the locked dependencies:

```bash
make frontend-install
make frontend-check
(cd web && npx playwright install chromium)
make frontend-browser  # with the disposable PostgreSQL/Redis URLs exported
make frontend-assets-check
```

Commit source, `web/package-lock.json` and rebuilt `internal/dashboard/dist` together. The browser fixture
uses a unique schema/key and teardown waits for cleanup before returning. See [web/README.md](web/README.md)
for pinned tools/license review and [the M4 guide](docs/guides/inspection-and-replay.md) for upgrade rules.

## Code navigation

[CodeGraph](https://github.com/colbymchenry/codegraph) builds a local symbol index of this
repository. It is optional: neither the build nor CI depends on it.

```bash
codegraph init --yes .   # build the index once
codegraph status          # whether the index is current
codegraph sync            # refresh it after a change
```

The index lives in `.codegraph/`. Git ignores its database and temporary files; the
`.codegraph/.gitignore` that enforces that is tracked.

## Commit messages

Conventional Commits, imperative mood, no trailing period:

```
feat(ingest): verify HMAC signature before persisting
fix(retry): honour Retry-After when it exceeds the base delay
docs(plan): record M2 acceptance criteria as met
```

Types: `feat`, `fix`, `docs`, `test`, `refactor`, `perf`, `build`, `ci`, `chore`.

## Pull request process

1. Open an issue first for anything larger than a typo. It saves everyone time.
2. Branch from `main`: `feat/short-slug`, `fix/short-slug`.
3. Add tests. A pull request that reduces coverage needs a reason in the description.
4. Run `make lint`, `make test`, `make race` and `make build` locally before pushing; run the frontend
   gates above when changing dashboard source/assets or its API.
5. Fill in the pull request template completely, including how you verified the change.
6. CI must be green. Reviewers are looking for correctness first, style second.

## Definition of done

A change is done when:

- it is implemented, not stubbed;
- it is covered by a test that fails without it;
- `docs/PLAN.md` status fields and `README.md` roadmap reflect the new reality;
- any user-visible behaviour is documented where a user would look for it.

## Good first issues

Issues labelled `good first issue` are scoped to be completable in one sitting, with the relevant files
listed in the issue body. Claim one by commenting, then open a draft pull request early.
