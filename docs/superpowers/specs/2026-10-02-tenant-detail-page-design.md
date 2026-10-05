# Tenant detail page on the core admin — design

Date: 2026-10-02. Status: draft for review. Path: architectural (three repos, new routes in two services, a new trust link).

## Purpose

An operator of the platform needs one page per tenant in the core admin console (`innonomads/admin`) from which they can see how that tenant authenticates and act on its credentials, without leaving the console or touching a database. Success: from `/admin/tenants/[id]` the operator can read the tenant's API key suffix and admin email, rotate the tenant API key, rotate a product service key, and trigger a password reset for the tenant admin.

## What was asked, what was decided

Asked: a tenant detail page showing service key, API key last 4 characters, tenant admin email, with actions: tenant admin password reset, tenant service key rotate, tenant API key rotate.

Decided in discussion (2026-10-02):

- **Service keys are not per tenant.** Tenantcore stores service keys per product service (`service_clients`), with no link to a tenant. The page shows those product service keys as a shared section and rotates them. Per-tenant service keys are out of scope.
- **Tenant admin accounts live in digitalservice**, not tenantcore. The console reaches them by calling digitalservice directly, which learns to verify tenantcore's Ed25519 public key on one new route group.

## Page contents

| Item | Source | Status |
|---|---|---|
| Tenant name, slug, status, contact email | `GET /admin/tenants/{id}` (tenantcore) | exists |
| API key last 4 | `Tenant.api_key_last4` | exists |
| Rotate tenant API key | `POST /admin/tenants/{id}/rotate-key` (tenantcore) | exists |
| Product service keys: name, active/revoked, created | `GET /admin/service-clients` | exists |
| Rotate a service key | `POST /admin/service-clients/{id}/rotate` (tenantcore) | new |
| Tenant admin email(s) | `GET /platform/tenants/{id}/admin-users` (digitalservice) | new |
| Tenant admin password reset | `POST /platform/tenants/{id}/admin-users/{user_id}/reset-password` (digitalservice) | new |

The tenants list gains a "Details" link per row.

## tenantcore

New route `POST /api/v1/admin/service-clients/{id}/rotate`, superadmin bearer token. It replaces the key hash and last 4 on the existing client record in one write and returns the new key once, in the same shape as create. (Not "new record plus revoke": the client name is uniquely indexed.) The old key stops working at once; if the write fails the old key stays valid. A revoked client cannot be rotated (409); an unknown id is 404. The route is added to `docs/api.json`; `internal/api/openapi_test.go` fails the build otherwise. Tests cover: new key works, old key rejected, failure leaves the old key active, revoked client refused.

The tenant API key rotate route already exists and is unchanged.

## digitalservice

A new route group under `/api/v1/platform/tenants/{id}/admin-users`, guarded by a new middleware that verifies a tenantcore Ed25519 superadmin token using the **public key only**. Digitalservice can verify but cannot mint. Config: the public key, supplied by env, with the route group switched off (404) when it is unset, so existing deployments are unaffected.

- `GET` lists users of that tenant with role `admin`: id, email, name, status. Never returns hashes.
- `POST .../{user_id}/reset-password` starts the existing emailed-code flow for that user through `TenantPasswordResetService`. The operator never sees or sets a password and the response carries no code. The endpoint is rate limited. A suspended user is refused. Response wording is the same whether or not mail was queued, matching the self-service flow.
- A tenant that does not exist in digitalservice returns an empty list, not an error. The console shows "No admin account in this product". (Nelson Travel and Bayan Bogd are in this state today.)
- Routes added to `internal/api/docs/docs/openapi.json` (CRLF file, edit as bytes).
- Tests with fakes, in the style of the existing reset service tests. The guard test asserts a tenantcore token opens the group, the old HMAC platform token does not, and a missing token is refused.

## Console (`innonomads/admin`)

- New page `src/app/admin/(console)/tenants/[id]/page.tsx`, server component, following `subscription/page.tsx`. Data loaders in `src/lib/data`. Server actions in `tenants/actions.ts`.
- New env `DIGITALSERVICE_URL`, server-side only. The token is the signed-in operator's own tenantcore token, as the cutover design requires. Unset means the admin-account section shows "Not configured" and the rest of the page works.
- Each loader runs through `safeLoad`, so one failing source (digitalservice down) degrades only its own section.
- **Rotate actions** need a confirmation step and show the new key once, with a copy control, and warn that it cannot be recovered. Tenant API key rotate warns that the tenant's storefront stops working until its key is replaced. Service key rotate warns that the named product service stops working until its `TENANTCORE_SERVICE_KEY` is replaced. The key is returned in action state, never stored, logged, or placed in a URL.
- **Password reset** shows the target email and asks for confirmation. Success message says a code was emailed.
- Section copy states that service keys are shared by all tenants.

## Error handling

- 401 on a request carrying a token follows the existing redirect to `/admin/login`.
- Mail not configured (503 from tenantcore's mailer, via digitalservice) shows "Email isn't set up on this server".
- Rotation failure shows the server's message; the old key is still valid in the tenantcore case by construction. For tenant API key rotate, the existing route's behaviour is unchanged.

## Testing and verification

- Go: `go build ./... && go vet ./internal/... ./pkg/... && go test ./internal/... ./pkg/... -count=1` in both services. Never `go test ./...`.
- Console has no test runner: `npx tsc --noEmit`, eslint on touched files only (`npx eslint src` has known errors in untouched files), then a click-through in the browser against the throwaway tenant `ZZ-THROWAWAY console test` (id `6abddd74a3edc529920a78ab`). The click-through needs the operator to sign in; the assistant does not type the password.
- Live reset verification uses a throwaway tenant and a throwaway user with the operator's own email. Never E&S's real admin.
- Rotating a real service key (for example `digitalservice-local`) breaks the running local digitalservice until its `.env` is updated. Verify rotation against a throwaway service client created for the purpose.

## Out of scope

Per-tenant service keys. Setting a password directly. Ending sessions already signed in (tokens are stateless). Closing the three PII exposures recorded in the backend-core refactor notes. Retiring digitalservice's own platform login.

## Open points

None blocking. Mail on production tenantcore is off until `GMAIL_EMAIL` and `GMAIL_PASSWORD` are set on Render, so password reset will answer 503 there until then.
