# HANDOVER: tenantcore changes for the realestate product — 2026-10-03

Branch **`feat/site-hosts`** (off `master`; not merged, not pushed). Commits `998ea1b..9cd6d58` (two: feature + fix round). Full story: `../realestate/HANDOVER.md` and `../realestate/docs/superpowers/sdd-ledger-2026-10-03.md`.

## What changed (additive; carwash and digitalservice untouched)

- `models.Tenant.Hosts []string` (`site_hosts`), `NormalizeHost` (lowercase, no port, no trailing dots, bracketed IPv6; refuses `/ \ ? # @ *`, whitespace). Separate from the existing `Tenant.Domain` (which binds an API key to one origin; unchanged).
- Unique multikey partial index on `site_hosts` with partial filter `{site_hosts: {$exists: true}}` (never store null or `[]`; empty list is `$unset`). **Unverified against a live Mongo** (no `explain` run). Check that `FindOne({site_hosts: "x"})` uses it.
- `GET /api/v1/svc/tenants/by-host/:host` (X-Service-Key): returns the same body as `/svc/entitlements/:tenant_id`; unknown host => 404 with the `apierr.NotFound("tenant")` / `TENANT` envelope. Documented in `docs/api.json`.
- `PUT /api/v1/admin/tenants/:id/hosts` body `{hosts: [...]}`: validates (max 20 hosts, 253 chars, no slashes/wildcards), 409 on a host owned by another tenant.
- Mail templates `staff_invite` and `lead_notification` (fixed fields; subject CR/LF stripped by `buildMessage`).

## Before merging / deploying

1. Review and merge `feat/site-hosts` (run `go build ./... && go vet ./... && go test ./...`; all pass on the dev toolchain).
2. Existing deployments: the new unique index is created at boot via `EnsureIndexes`; duplicate hosts cannot exist yet so it should be clean.
3. Create for the realestate product: a service client key (`POST /api/v1/admin/service-clients`), a plan with module `realestate` and limits `realestate.projects`, `realestate.units`, `realestate.users`, tenants with hosts set. `../realestate/scripts/seed-tenants.md` has the exact steps.

## Known minor

The `buildMessage` `to` address is not CR/LF-stripped (the route validates `email` format first); worth hardening.
