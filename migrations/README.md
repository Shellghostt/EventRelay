# Migrations

SQL migrations live here, numbered sequentially:

```
0001_init.sql
0002_add_endpoints.sql
```

Rules:

- Every migration is forward-only. Rollbacks are a separate migration.
- Every migration must be safe to run on a table with existing rows, or must be documented as
  intentionally blocking in docs/PLAN.md.
- Migrations are applied by the API on startup only in development, and by an explicit command in
  production.
