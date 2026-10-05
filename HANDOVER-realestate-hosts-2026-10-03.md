# HANDOVER: tenantcore changes for the realestate product — 2026-10-03

Branch **`feat/site-hosts`** (off `master`; not merged, not pushed). Commits `998ea1b..HEAD`, 14 in all as of 2026-10-04: the site-hosts feature and its first fix round (`280d276`, `9cd6d58`); the mail switch from Gmail SMTP to Brevo (SMTP, then Brevo's HTTPS API, with Gmail kept as a development-only fallback) and its config changes; service-key rotation; and the 2026-10-04 fix wave (mail value sanitising, the `site_hosts` index rename, strict host validation) plus handover updates. Full story: `../realestate/HANDOVER.md` and `../realestate/docs/superpowers/sdd-ledger-2026-10-03.md`.

## What changed (additive; carwash and digitalservice untouched)

- `models.Tenant.Hosts []string` (`site_hosts`), `NormalizeHost` (lowercase, no port, no trailing dots, bracketed IPv6). Separate from the existing `Tenant.Domain` (which binds an API key to one origin; unchanged).
- Host validation on write (`service.canonicalHost`): a Unicode name is converted with IDNA (`idna.Lookup.ToASCII`) and stored as punycode; only `[a-z0-9-]` labels (1-63 chars, no edge hyphen, non-numeric last label) or a zone-less IPv4/IPv6 literal are accepted; an optional port must be digits. `[::1`, `<`, `,`, `%`, `_`, and control or invisible characters are refused with 400. The stored form is what `../realestate/internal/entitlement.NormalizeHost` makes of a browser's Host header.
- Unique multikey partial index on `site_hosts` with partial filter `{site_hosts: {$exists: true}}` (never store null or `[]`; empty list is `$unset`). **Unverified against a live Mongo** (no `explain` run). Check that `FindOne({site_hosts: "x"})` uses it.
- **Index rename.** The index is now named `site_hosts_unique_exists`. `280d276` created it as the default `site_hosts_1` with a `$type: "string"` filter, so a database that booted that commit would hit an index-options conflict and `EnsureIndexes` would fail at boot. `EnsureIndexes` now drops `site_hosts_1` from `tenants` first, if it is listed (only that index; a no-op on a fresh database and on every later boot). site_hosts is briefly not unique between that drop and the create. Tested against a fake index view only; never run against a real MongoDB.
- Mail template values: every plain value has line breaks turned into spaces and other control/format characters removed, and is capped at 200 characters, so a visitor's `lead_name` cannot add lines to a tenant's mail. `*_url` values are never truncated (over 2048 bytes or containing a control character is an error), and `invite_url` / `lead_url` must be absolute `https` URLs.
- `GET /api/v1/svc/tenants/by-host/:host` (X-Service-Key): returns the same body as `/svc/entitlements/:tenant_id`; unknown host => 404 with the `apierr.NotFound("tenant")` / `TENANT` envelope. Documented in `docs/api.json`.
- `PUT /api/v1/admin/tenants/:id/hosts` body `{hosts: [...]}`: validates (max 20 hosts, 253 chars, no slashes/wildcards), 409 on a host owned by another tenant.
- Mail templates `staff_invite` and `lead_notification` (fixed fields; subject CR/LF stripped by `buildMessage`).

## Before merging / deploying

1. Review and merge `feat/site-hosts` (run `go build ./... && go vet ./... && go test ./...`; all pass on the dev toolchain).
2. Existing deployments: the unique index is created at boot via `EnsureIndexes` as `site_hosts_unique_exists`, after dropping any `site_hosts_1` left by `280d276` (see the index rename above); duplicate hosts cannot exist yet so it should be clean.
3. **Mail config, or mail silently stops.** Production mail now goes only through Brevo's HTTPS API. After the merge, the production environment must set `BREVO_API_KEY` (a Brevo API key, not an SMTP key) and `MAIL_FROM_EMAIL` (a sender verified in Brevo); see `Config.BrevoEnabled` in `internal/config/config.go`. The Gmail settings work only when `APP_ENV` is not production (`GmailEnabled`). With either Brevo value missing, `EmailEnabled()` is false in production: password-reset codes, invites, lead notifications and the expiry notice stop being sent. Boot does not fail: it logs one "email is off" warning and `POST /svc/notifications/email` answers 503.
4. Create for the realestate product: a service client key (`POST /api/v1/admin/service-clients`), a plan with module `realestate` and limits `realestate.projects`, `realestate.units`, `realestate.users`, tenants with hosts set. `../realestate/scripts/seed-tenants.md` has the exact steps.

## Known minor

The `buildMessage` `to` address is not CR/LF-stripped (the route validates `email` format first); worth hardening.
