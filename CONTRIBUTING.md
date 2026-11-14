# Contributing to EventRelay

Thanks for taking the time. This document is deliberately short and specific.

## Read first

1. [docs/PLAN.md](docs/PLAN.md) - the authoritative specification. It explains scope, architecture,
   milestones and what is explicitly out of scope.
2. [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) - how the pieces fit together.
3. [docs/adr/](docs/adr/) - decisions that have already been argued about. Do not relitigate them
   without a new ADR.

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
4. Run `make lint` and `make test` locally before pushing.
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
