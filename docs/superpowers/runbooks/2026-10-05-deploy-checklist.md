# Production deploy checklist — 2026-10-05 changes

> **Superseded in part (2026-10-06).** digitalservice no longer has a `TENANT_RESOLVER` switch or a local resolver: it always resolves keys through tenantcore and refuses to start without `TENANTCORE_URL` and `TENANTCORE_SERVICE_KEY`. Steps here that set, unset or flip `TENANT_RESOLVER`, or roll back to `local`, no longer apply; there is no rollback switch, so the production dry run and per-tenant key re-issue must come first. See `digitalservice/handover.md` (Latest state).

Scope: tenantcore `master` (PR #4 site hosts and rotate, PR #5 central tenant resolution), digitalservice `master` (PR #3 central resolution, inert by default), core admin `innonomads/admin` (PR for `feat/tenant-admin-updates`). This does NOT start the resolution rollout (see `2026-10-05-central-tenant-resolution-rollout.md`); `TENANT_RESOLVER` stays unset.

Hard rules while following this: never paste a key, token, password or connection string into chat or a ticket; never rotate, suspend or reset a LIVE tenant to "test"; the E&S subscription ends 2026-10-20, so do not leave this half-done near that date.

## 0. Before you start

- [ ] Confirm the three PRs are merged: tenantcore `origin/master` contains PR #5; digitalservice `origin/master` contains PR #3 (`8b804d7`); the core admin PR is merged (or deploy from its branch deliberately).
- [ ] Take a backup of the production databases (tenantcore and digitalservice), or confirm your Atlas tier makes automatic snapshots. tenantcore changes indexes at boot (step 1).
- [ ] Note the currently deployed commit of each service so you can roll back (Render: previous deploy; Vercel: previous deployment).
- [ ] Pick a quiet hour. Wake tenantcore first (Render cold start) by opening `/healthz`.

## 1. tenantcore (Render, from `master`)

Environment (names only; do not change values that already work):
- [ ] `TOKEN_PRIVATE_KEY` unchanged. Changing it invalidates every operator token and breaks every service that holds the public key.
- [ ] Mail: `BREVO_API_KEY` and `MAIL_FROM_EMAIL` (a sender verified in Brevo), `MAIL_FROM_NAME` optional. `GMAIL_*` are the old dev fallback; leave them unset in production.
- [ ] `EXPIRY_NOTICE_EMAIL`: setting it turns ON the hourly subscription expiry warning email. Decide deliberately; leave unset if not ready.
- [ ] `MONGO_CONNECT_TIMEOUT` (default 30s) is fine for Atlas.

Deploy, then verify:
- [ ] Boot logs: the site hosts index step finishes without error. Expected: the older `site_hosts` index is dropped and the new one created. Index changes are not undone by a code rollback, but the extra index is harmless.
- [ ] `GET /healthz` returns 200; `GET /readyz` status `ok`; features include `email` (and `expiry_notice` only if you set the address).
- [ ] Read-only route probes (no credentials, expect 401; a made-up path expects 404): `POST /api/v1/admin/service-clients/000000000000000000000000/rotate`, `GET /api/v1/svc/tenants/resolve`, `GET /api/v1/admin/plans`.
- [ ] `GET /.well-known/tenantcore` returns the `public_key`. Record it for step 2 (it is public, not a secret).
- [ ] Sign in to the old core admin or the new one; the tenants list loads.

Rollback: redeploy the previous Render deploy. Safe: the new code only adds routes and an index.

## 2. digitalservice (from `master`, commit `8b804d7` or later)

Environment:
- [ ] Already present and unchanged: `MONGO_URI`, `MONGO_DB`, `TOKEN_SECRET`, `TENANTCORE_URL`, `TENANTCORE_SERVICE_KEY` (an ACTIVE service client on production tenantcore; compare its last 4 with the console's service key table).
- [ ] NEW `TENANTCORE_PUBLIC_KEY`: the `public_key` you recorded from PRODUCTION tenantcore in step 1. Not the value from your local `.env`. Standard base64 of the raw 32-byte key. If it is wrong the tenant page's admin-accounts section says the operator token was rejected.
- [ ] NEW `TRUSTED_PROXIES` (comma-separated IPs/CIDRs of the reverse proxy in front of the service, from the hosting provider's documentation). Not needed while `TENANT_RESOLVER` is unset (the new limiter does not exist then), but set it now so the later flip is safe.
- [ ] `TENANT_RESOLVER`: leave UNSET (same as `local`). `TENANT_RESOLVE_RATE_PER_MINUTE` and `TENANT_RESOLVE_BURST` keep their defaults (600 and 120); they only matter after the flip.

Deploy, then verify:
- [ ] `GET /readyz`: status `ok`; features include `entitlement`, `password_reset`, `tenantcore_admin_users`; there is NO `tenant_resolver_tenantcore` feature (that confirms the resolver is still local).
- [ ] The live E&S storefront loads and a public read works. The admin sign-in works. Nothing about tenant resolution has changed.
- [ ] `GET /api/v1/platform/tenants/000000000000000000000000/admin-users` with no token returns 401 (404 means `TENANTCORE_PUBLIC_KEY` is not set).

Rollback: redeploy the previous deploy, or unset `TENANTCORE_PUBLIC_KEY`. Nothing else is active.

## 3. Core admin (Vercel, from `master` after the PR merge)

Environment:
- [ ] `API_URL` = production tenantcore ending in `/api/v1` (server only); `NEXT_PUBLIC_API_URL` = same (browser, public routes only).
- [ ] NEW `DIGITALSERVICE_URL` = production digitalservice ending in `/api/v1` (server only). Unset shows "Not configured" in the admin accounts section.
- [ ] `SITE_URL`, `CLOUDINARY_URL` unchanged.
- [ ] Redeploy after changing any env (Next bakes values at build or start).

Verify (read-only on live tenants):
- [ ] Sign in at `/admin/login`.
- [ ] Tenants list loads; open a tenant's Details page: the API key shows `•••• <last4>`, the service key table lists the product clients, the admin accounts section lists accounts (this proves the public key and the URL are right).
- [ ] The Plans, Staff and Service clients pages load.
- [ ] Do NOT press Rotate, Reset or Suspend on a live tenant to test. Test those on a throwaway tenant you created for the purpose, or on staging.

Rollback: promote the previous Vercel deployment.

## 4. After all three

- [ ] One storefront request and one admin sign-in per live tenant (E&S, Nomad Trails, Inno Nomads).
- [ ] tenantcore `/readyz` and digitalservice `/readyz` still `ok` after ten minutes.
- [ ] Record the deployed commits and the date in each repo's `handover.md`.

## 5. Not part of this deploy

- The resolution rollout (production dry run, key re-issue, `TENANT_RESOLVER=tenantcore`): separate runbook, needs your approval at each write.
- E&S translations seed: blocked until the E&S tenant key production accepts is known.
- Invoices (dropped), the old component versions (not ported).
