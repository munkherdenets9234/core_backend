# Tenant Detail Page Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A `/admin/tenants/[id]` page in the core admin that shows a tenant's API key suffix and admin email, and rotates the tenant API key, rotates a product service key, and resets the tenant admin's password.

**Architecture:** tenantcore gains one route (service-key rotate). digitalservice gains a route group that verifies tenantcore's Ed25519 public key (verify only) and exposes tenant admin users plus a reset trigger that reuses the existing emailed-code flow. The console calls tenantcore as today and digitalservice directly with the operator's own tenantcore token.

**Tech Stack:** Go + Gin + MongoDB (tenantcore, digitalservice), golang-jwt/v5 (already in digitalservice go.mod), Next.js 16 server components and server actions (`innonomads/admin`).

**Spec:** `tenantcore/docs/superpowers/specs/2026-10-02-tenant-detail-page-design.md`

## Global Constraints

- Go checks, per service: `go build ./... && go vet ./internal/... ./pkg/... && go test ./internal/... ./pkg/... -count=1`. Never `go test ./...` (`test/api` needs Docker).
- tenantcore: every new route must be in `docs/api.json` or `internal/api/openapi_test.go` fails. `internal/api/guard_test.go` `publicRoutes` map must not grow.
- digitalservice: routes go in `internal/api/docs/docs/openapi.json`, a **CRLF** file; edit as bytes and write back CRLF. A pre-commit hook runs gitleaks and static analysis on every commit.
- A raw key (tenant API key or service key) is returned once, in action state only. Never stored, logged, or put in a URL.
- Password reset never returns, sets, or displays a password or code to the operator.
- Console style: double quotes and semicolons. `lib/api/client.ts` and `lib/data/*` are server-only.
- Never print `.env` values. Never touch E&S's real admin (`enkhjin.erdenebuyan@gmail.com`). Test data names start with `ZZ-THROWAWAY`.
- Console has no test runner: verify with `npx tsc --noEmit`, `npx eslint <touched files> --max-warnings=0`, and the browser click-through (Task 6). `npx eslint src` has known errors in untouched files.
- `AGENTS.md` in `innonomads/admin` says its Next has breaking changes. Copy patterns from `tenants/[id]/subscription/page.tsx`, `SubscriptionPanel.tsx`, and `actions.ts` rather than inventing new ones.

## Review Focus

- Rotating a service key whose record is already revoked: must be refused, never silently reactivate a revoked client.
- Rotating while an old key is mid-request: the old key must stop authenticating immediately after the swap (hash replaced in one write).
- digitalservice with `TENANTCORE_PUBLIC_KEY` unset: the new group must answer 404, and existing routes stay unchanged.
- A tenant-scoped or HMAC platform token presented to the new group: refused. A tenantcore token with role other than superadmin: refused.
- Password reset aimed at a staff-role user, a suspended user, or a user of a different tenant: refused, no mail queued.
- Mail not configured: reset answers 503 `email`, console shows "Email isn't set up on this server".
- Tenant absent from digitalservice: list returns `[]`, console shows "No admin account in this product", no error banner.

---

### Task 1: tenantcore — rotate a service key

**Files:**
- Modify: `internal/repository/platform_repo.go` (add `ReplaceKey` on `ServiceClientRepo`, next to `UpdateStatus` at ~line 136)
- Modify: `internal/service/platform_service.go` (add `Rotate` on `ServiceClientService`, after `Revoke` ~line 318)
- Modify: `internal/api/admin/private/clients.go` (add `Rotate` handler)
- Modify: `internal/api/admin/private/private.go:105-108` (mount route)
- Modify: `docs/api.json`
- Test: `internal/service/platform_service_test.go` (create; no service-client tests exist yet — use a fake behind a small interface if `ServiceClientService` cannot take one, otherwise follow the repo's existing service-test style in `password_reset_service_test.go`)

**Interfaces:**
- Consumes: `apikey.Generate() (raw, hash string, err error)`, `apikey.Last4(raw string) string`, `models.ServiceClient{KeyHash, KeyLast4, Status}`, `updateOne`.
- Produces:
  - `func (r *ServiceClientRepo) ReplaceKey(ctx context.Context, id primitive.ObjectID, keyHash, keyLast4 string) error` — one `UpdateOne` filtered on `{_id: id, status: active}`; returns `mongo.ErrNoDocuments` if no active record matched.
  - `func (s *ServiceClientService) Rotate(ctx context.Context, idStr string) (*models.ServiceClient, string, error)` — invalid hex → `apierr.BadRequest("invalid service client id")`; no active match → distinguish missing (`NotFound("service client").In(DomainService)`) from revoked (`Conflict("service client is revoked").In(DomainService)`) by a follow-up lookup; returns the updated client and the raw key.
  - Route `POST /api/v1/admin/service-clients/{id}/rotate` → 200 `{service_client, service_key}` same shape as create.

**Design note (deviates from the spec's wording, same intent):** `service_clients.name` has a unique index, so "create a second record, revoke the first" would collide. The rotate swaps `key_hash` and `key_last4` on the existing record in one write. Atomic by construction: failure leaves the old key valid; success invalidates it at once. Update the spec's tenantcore paragraph to say this.

- [ ] **Step 1: Write failing tests** in `internal/service/platform_service_test.go`: `TestServiceClientRotate_NewKeyAuthenticatesOldDoesNot`, `TestServiceClientRotate_RevokedClientRefused` (asserts Conflict, record still revoked, key hash unchanged), `TestServiceClientRotate_UnknownIDNotFound`, `TestServiceClientRotate_BadIDBadRequest`, `TestServiceClientRotate_RepoFailureLeavesOldKey` (repo returns an error; `Authenticate(oldKey)` still succeeds).
- [ ] **Step 2: Run** `go test ./internal/service -run ServiceClientRotate -count=1`. Expected: FAIL, `Rotate` undefined.
- [ ] **Step 3: Implement** `ReplaceKey` and `Rotate` with the signatures above.
- [ ] **Step 4: Run** the same command. Expected: PASS.
- [ ] **Step 5: Add handler `func (h *clientsController) Rotate(c *gin.Context) error`** mirroring `Create`'s response shape, mount `sc.POST("/:id/rotate", clients.Rotate)`.
- [ ] **Step 6: Document** the route in `docs/api.json` (operationId `rotateServiceClientKey`, summary "Replace a service key", description states the new key is shown once and the old stops working immediately, 409 for revoked, 404 unknown; response schema `CreateServiceClientData`).
- [ ] **Step 7: Run** the full check from Global Constraints in `tenantcore`. Expected: green, including `openapi_test.go` and `guard_test.go`.
- [ ] **Step 8: Commit** `feat: add service key rotation`.

---

### Task 2: digitalservice — verify tenantcore tokens

**Files:**
- Create: `pkg/token/tenantcore.go` (copy of tenantcore's `Claims`, `Role` consts needed, `Verifier`, `NewVerifier`, `Verify`, minus the Maker; keep the doc comments' reasoning short)
- Create: `pkg/token/tenantcore_test.go`
- Modify: `internal/config/config.go` (field `TenantcorePublicKey string`, env `TENANTCORE_PUBLIC_KEY`, optional; validate base64 Ed25519 length when set)
- Modify: `internal/config/config_test.go`
- Create: `internal/middleware/tenantcore_auth.go`
- Create: `internal/middleware/tenantcore_auth_test.go`

**Interfaces:**
- Produces:
  - `token.NewVerifier(publicKeyB64 string) (*token.TenantcoreVerifier, error)` and `func (v *TenantcoreVerifier) Verify(tokenStr string) (*TenantcoreClaims, error)` — names carry the `Tenantcore` prefix because this package already has `Claims`/`Maker` for the HMAC tokens. Verify rejects non-EdDSA algorithms, expired tokens, and a superadmin claim that carries a tenant id.
  - `middleware.NewTenantcoreAuth(v *token.TenantcoreVerifier) *TenantcoreAuth` with `func (a *TenantcoreAuth) RequireSuperadmin() gin.HandlerFunc`. A nil verifier yields a handler that aborts 404 (group switched off). Missing or invalid token → 401 via `fail(c, apierr.Unauthorized(...))`; non-superadmin role → 403.

- [ ] **Step 1: Write failing tests.** In `pkg/token`: `TestTenantcoreVerify_ValidSuperadmin`, `TestTenantcoreVerify_RejectsHMACToken`, `TestTenantcoreVerify_RejectsExpired`, `TestTenantcoreVerify_RejectsTamperedSignature`, `TestTenantcoreVerify_RejectsSuperadminWithTenantID`. Build test tokens with a locally generated ed25519 key via `golang-jwt` (no dependency on tenantcore code). In `middleware`: `TestRequireSuperadmin_NilVerifierIs404`, `_MissingBearerIs401`, `_TenantRoleIs403`, `_SuperadminPasses`.
- [ ] **Step 2: Run** `go test ./pkg/token ./internal/middleware -count=1`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement** the verifier and middleware with the signatures above; add config field, read in `Load`, error text on bad key names the env var.
- [ ] **Step 4: Run** same command plus `go test ./internal/config -count=1`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: verify tenantcore superadmin tokens`.

---

### Task 3: digitalservice — tenant admin users routes

**Files:**
- Modify: `internal/repository/tenant_user_repo.go` (add `FindAdmins`)
- Modify: `internal/service/tenant_user_service.go` (add `ListAdmins`, `GetAdmin`)
- Create: `internal/api/platform/tenantcore/admin_users.go` (package `tenantcore`, controller + `Register`)
- Create: `internal/api/platform/tenantcore/admin_users_test.go`
- Modify: `internal/api/platform/platform.go` (add `Tenantcore *middleware.TenantcoreAuth`, `Reset *service.TenantPasswordResetService` to `Deps`; mount the group)
- Modify: `internal/api/router.go`, `internal/api/server.go`, `internal/bootstrap/bootstrap.go`, `internal/bootstrap/wiring.go` (pass the verifier-backed middleware and the existing reset service through)
- Modify: `internal/api/guard_test.go` (the platform-private assertion must exempt this group and assert its own guard instead)
- Modify: `internal/api/docs/docs/openapi.json` (CRLF)

**Interfaces:**
- Consumes: `TenantcoreAuth.RequireSuperadmin()` (Task 2), `TenantPasswordResetService.Request(ctx, tenantID primitive.ObjectID, email string) error` (existing), `AuthRateLimit gin.HandlerFunc` (existing in `platform.Deps`).
- Produces:
  - `func (r *TenantUserRepo) FindAdmins(ctx context.Context, tenantID primitive.ObjectID) ([]*models.TenantUser, error)` — role `admin`, any status, sorted by email.
  - `func (s *TenantUserService) ListAdmins(ctx context.Context, tenantID primitive.ObjectID) ([]*models.TenantUser, error)`
  - `func (s *TenantUserService) GetAdmin(ctx context.Context, tenantID primitive.ObjectID, idStr string) (*models.TenantUser, error)` — `NotFound` when the user is absent, not role admin, or in another tenant.
  - `GET /api/v1/platform/tenants/:id/admin-users` → 200 `[{id, email, name, status}]`, `[]` (not null) when none. Invalid tenant id → 400.
  - `POST /api/v1/platform/tenants/:id/admin-users/:user_id/reset-password` → 200 `{"message": "A reset code was emailed."}`. Suspended → 409. Mail unavailable → 503 via the existing `apierr.FeatureUnavailable("email")`. Behind `AuthRateLimit`.
  - Mounted under `/platform/tenants/:id/...`, which collides by prefix with existing public `GET /platform/tenants/:id`; use a separate `Group` so the guard applies only to the new paths.

- [ ] **Step 1: Write failing tests** (fakes for the user and reset collaborators, httptest router with a real `TenantcoreAuth`): `TestListAdminUsers_ReturnsAdminsOnly`, `TestListAdminUsers_EmptyIsArrayNotNull`, `TestListAdminUsers_NoTokenIs401`, `TestListAdminUsers_HMACPlatformTokenIs401`, `TestListAdminUsers_GroupOffIs404WhenKeyUnset`, `TestResetPassword_QueuesCodeForAdmin` (fake reset service records `(tenantID, email)`), `TestResetPassword_StaffUserRefused`, `TestResetPassword_SuspendedUserConflict` (nothing recorded), `TestResetPassword_OtherTenantsUserNotFound`, `TestResetPassword_ResponseCarriesNoCode`, `TestResetPassword_MailUnavailableIs503`.
- [ ] **Step 2: Run** `go test ./internal/api/platform/tenantcore -count=1`. Expected: FAIL (package missing).
- [ ] **Step 3: Implement** repo, service, controller, and wiring with the signatures above. The reset handler loads the user with `GetAdmin`, checks status, then calls `Request` with the stored email; the operator-supplied path never supplies an email.
- [ ] **Step 4: Run** the package tests. Expected: PASS.
- [ ] **Step 5: Update `guard_test.go`** so `TestPrivatePlatformRoutesRequireToken` keeps its meaning: it asserts the two new routes refuse an HMAC bearer, and still asserts every other private platform route refuses no token. Run `go test ./internal/api -count=1`. Expected: PASS.
- [ ] **Step 6: Document** both routes in `openapi.json` (bytes, CRLF); the OpenAPI test, if one exists here, must pass.
- [ ] **Step 7: Run** the full check from Global Constraints in `digitalservice`. Expected: green.
- [ ] **Step 8: Mutation check** (the repo's own practice, see its handover): remove the status check, the role check, and the tenant scoping in turn; each must fail a named test above. Restore after each.
- [ ] **Step 9: Commit** `feat: add tenant admin users routes for the core admin`.

---

### Task 4: console — data layer and actions

**Files:**
- Create: `src/lib/api/digitalservice.ts` (server-only fetch helper; base `DIGITALSERVICE_URL` env, no default; same envelope handling and 401-with-token redirect as `client.ts`)
- Create: `src/lib/data/tenant-detail.ts`
- Modify: `src/lib/types.ts` (add `ServiceClient`, `TenantAdminUser`)
- Modify: `src/app/admin/(console)/tenants/actions.ts`
- Modify: `.env.example` (create if absent; add `DIGITALSERVICE_URL` with a comment)

**Interfaces:**
- Produces (types in `lib/types.ts`):
  - `interface ServiceClient { id: string; name: string; status: "active" | "revoked"; key_last4: string; created_at: string; last_seen_at?: string | null }` — confirm field names against `view.ServiceClientOf` and `docs/api.json` before writing.
  - `interface TenantAdminUser { id: string; email: string; name: string; status: "active" | "suspended" }`
- Produces (data): `listServiceClients(): Promise<ServiceClient[]>` (tenantcore `GET /admin/service-clients`, null → `[]`); `listTenantAdminUsers(tenantId: string): Promise<TenantAdminUser[] | null>` — returns `null` when `DIGITALSERVICE_URL` is unset (section shows "Not configured"), `[]` when none.
- Produces (actions), each `(…bound args, prevState, formData) → Promise<State>` using `useActionState`, token from `requireToken()`:
  - `rotateTenantKeyAction(tenantId)` → `{ error?: string; newKey?: string }` via `POST /admin/tenants/${id}/rotate-key`. Confirm the response field holding the raw key in `docs/api.json` first.
  - `rotateServiceKeyAction(serviceClientId)` → `{ error?: string; newKey?: string }` via `POST /admin/service-clients/${id}/rotate`.
  - `resetAdminPasswordAction(tenantId, userId)` → `{ error?: string; sent?: boolean }` via digitalservice. A 503 maps to the message "Email isn't set up on this server".
  - Rotation actions `revalidatePath(`/admin/tenants/${tenantId}`)` (last4 changes) and never log or redirect with the key.

- [ ] **Step 1: Verify wire shapes** by reading `tenantcore/docs/api.json` (`rotateTenantAPIKey` response, `ServiceClient` schema) and `digitalservice` openapi from Task 3; write the field names into the types, not from memory.
- [ ] **Step 2: Implement** types, helper, loaders, actions with the signatures above.
- [ ] **Step 3: Run** `npx tsc --noEmit` (expected exit 0) and `npx eslint src/lib src/app/admin/\(console\)/tenants --max-warnings=0` on touched files only (expected clean).
- [ ] **Step 4: Commit** `feat: add tenant detail data layer and actions`.

---

### Task 5: console — the page and the list link

**Files:**
- Create: `src/app/admin/(console)/tenants/[id]/page.tsx`
- Create: `src/components/TenantDetailPanels.tsx` (client component: key-reveal, confirm, and reset forms; one file for the three panels because they share the confirm-then-reveal behaviour)
- Modify: `src/app/admin/(console)/tenants/page.tsx` (add a "Details" link first in the action row)

**Interfaces:**
- Consumes: Task 4 loaders and actions, `getTenantById`, `safeLoad`.
- Produces: page with these sections, each loaded through its own `safeLoad` so one failing source degrades only itself:
  1. Header: name, slug, status, contact email.
  2. Tenant API key: shows `•••• ` + `api_key_last4`; "Rotate key" opens a confirm step stating the storefront stops working until its key is replaced; on success shows the new key once with a copy button and a "cannot be recovered" note; the key disappears on navigation (held only in component state).
  3. Admin accounts: table of email/name/status with a "Reset password" button per active admin, a confirm step naming the email, success text "A reset code was emailed to …". States: "Not configured" (null), "No admin account in this product" (empty).
  4. Product service keys (shared by all tenants): table of name/status/last4/created; "Rotate" per active client with a confirm step naming the product and warning it stops working until its `TENANTCORE_SERVICE_KEY` is replaced; new key shown once as in 2; revoked rows have no button.

- [ ] **Step 1: Implement** the page and panels, copying the layout classes (`label`, `border border-paper/10`, `text-accent` for errors) and the `useActionState` pattern from `AssignPackageForm.tsx` and `SubscriptionPanel.tsx`; 404 from `getTenantById` → `notFound()` as the subscription page does.
- [ ] **Step 2: Add the "Details" link** to the tenants list row, same classes as its siblings.
- [ ] **Step 3: Run** `npx tsc --noEmit` and `npx eslint` on touched files `--max-warnings=0`. Expected: exit 0.
- [ ] **Step 4: Commit** `feat: add the tenant detail page to the core admin`.

---

### Task 6: live verification and cleanup

**Files:** none (verification; record findings in `innonomads/admin/handover.md` under a dated entry).

- [ ] **Step 1: Start** tenantcore (:8092), digitalservice (:8080) with `TENANTCORE_PUBLIC_KEY` set to tenantcore's public key (`GET /.well-known/tenantcore` serves it — confirm the field), and the console (`npm run dev -- -p 3011` from `innonomads/admin`, `DIGITALSERVICE_URL=http://localhost:8080/api/v1`). Check the three ports' titles.
- [ ] **Step 2: Ask the user to sign in** at `http://localhost:3011/admin/login`. Do not type the password.
- [ ] **Step 3: Create throwaways:** a service client named `zz-throwaway-rotate` through the tenantcore API; a tenant `ZZ-THROWAWAY detail test` mirrored into digitalservice at the same `_id` with one admin user whose email is the operator's own (guarded one-off helper that refuses names not starting `ZZ-THROWAWAY`, as in `digitalservice/handover.md` Task 6).
- [ ] **Step 4: Click through and confirm:** last4 matches the API; rotate tenant API key shows a key once and last4 changes; the old key now fails entitlement lookup and the new one passes; rotate `zz-throwaway-rotate` shows a key once, old key 401 on `/svc`, new key passes; reset shows the sent message, the operator's inbox gets a code, completing it signs in with the new password (needs the operator); a tenant not in digitalservice (for example Nelson Travel) shows "No admin account in this product"; with `DIGITALSERVICE_URL` unset the section shows "Not configured" and the rest of the page loads; a staff user and a suspended user show no usable reset button or are refused.
- [ ] **Step 5: Confirm** the new group answers 404 when digitalservice runs without `TENANTCORE_PUBLIC_KEY`, and 401 for a digitalservice HMAC platform token.
- [ ] **Step 6: Clean up** the throwaway tenant (both databases), user, reset codes, and service client; confirm nothing named `zz-throwaway` or `ZZ-THROWAWAY` remains. Never delete a tenant whose name does not start with `ZZ-THROWAWAY`.
- [ ] **Step 7: Write** the dated handover entry in `innonomads/admin/handover.md` and `digitalservice/handover.md` (what was verified, what was not, the new env var). Commit the console and digitalservice handover edits only if the user asks.

## Self-review notes

- Spec coverage: page contents table → Tasks 1, 3, 4, 5; tenantcore rotate → Task 1; digitalservice guard + routes + openapi + tests → Tasks 2–3; console env, safeLoad degradation, confirm/one-time reveal, section copy → Tasks 4–5; error handling (401, 503, absent tenant) → Tasks 3–5; verification plan including throwaway-only rule → Task 6.
- One spec deviation, called out in Task 1: rotation is an in-place key swap, because the service client name is uniquely indexed.
- Names checked across tasks: `ReplaceKey`/`Rotate` (Task 1), `TenantcoreVerifier`/`TenantcoreAuth.RequireSuperadmin` (Task 2, used in Task 3), `listTenantAdminUsers`/`resetAdminPasswordAction` (Task 4, used in Task 5).
