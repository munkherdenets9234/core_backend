# Tenant-user password reset Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a tenant's admin users reset a forgotten password from the travel admin using an emailed 6-digit code.

**Architecture:** digitalservice owns the flow (codes, attempts, expiry, the password write) behind two tenant-scoped public routes; it sends mail by calling tenantcore's existing `/svc/notifications/email`. The travel admin gets a `/forgot-password` page that calls those routes. All account-dependent work runs after the response so response time reveals nothing.

**Tech Stack:** Go, Gin, MongoDB driver v1, zap (digitalservice); Next.js, React 19, server actions (admin).

**Spec:** `tenantcore/docs/superpowers/specs/2026-10-01-tenant-user-password-reset-design.md`

Paths are relative to `D:\bkup\projects\digitalbrochure`. Go commands run in `digitalservice/`; console commands in `admin/`. Commits go to each repo's current branch (`digitalservice`: `refactor/backend-core`; `admin`: `master`).

## Global Constraints

- Code: 6 digits from `crypto/rand`, stored as SHA-256 hex, valid **10 minutes**, usable **once**, burned after **5** wrong guesses. New request invalidates earlier codes.
- Every `confirm` failure returns the **same** error: `that code is not valid — request a new one`.
- `request` returns `200` with wording true whether or not the account exists, **before any account-dependent work**. The lookup, insert and mail run in a goroutine with their own `30s` context.
- Only `503 FEATURE_UNAVAILABLE` (mail link unconfigured) may differ; it depends on deployment, never on the account.
- New password under 8 characters is a specific `400`.
- Routes are tenant-scoped by `X-API-Key`, rate limited by `AuthRateLimit`, and mounted **outside the subscription gate**, beside `/login`.
- Codes are looked up **per tenant**; a code for one tenant must never work for another with the same email.
- No new credentials: reuse `TENANTCORE_URL` and `TENANTCORE_SERVICE_KEY`.
- Mail goes through tenantcore's `password_reset_code` and `password_changed` templates; no tenantcore change.
- No session is issued by either route.
- Do not modify digitalservice's `/login`.
- `docs/api.json` files use CRLF; preserve it.
- Commit messages end with the `Co-Authored-By` trailer the session specifies.

## Review Focus

1. A code issued for tenant A, presented to tenant B for a user with the same email, must be refused. Owner: Task 3.
2. A user suspended **after** a code was issued must not be able to use it. Owner: Task 3.
3. The mail client must not hang: a server that never answers returns an error within its timeout, since it runs after the response and would otherwise pile up goroutines. Owner: Task 2.
4. A tenant with a lapsed subscription can still reset (the gate blocks every mutating method). Owner: Task 6 (live; the gate cannot be exercised without a database).
5. The page must say something useful when mail is not configured (`503`), and one attempt's error must not carry into the next. Owner: Task 5.

---

### Task 1: Reset code model, repository and password helper

**Files:**
- Create: `digitalservice/internal/models/tenant_password_reset.go`, `digitalservice/internal/repository/tenant_password_reset_repo.go`
- Modify: `digitalservice/internal/repository/indexes.go`, `digitalservice/pkg/password/password.go`
- Test: `digitalservice/internal/models/tenant_password_reset_test.go`, `digitalservice/internal/repository/tenant_password_reset_repo_test.go`

**Interfaces:**
- Produces: `models.TenantPasswordReset { ID, TenantID, UserID primitive.ObjectID; Email, CodeHash string; Attempts int; ExpiresAt time.Time; UsedAt *time.Time; CreatedAt time.Time }`; `const models.MaxResetAttempts = 5`; `func (p *TenantPasswordReset) Spent(now time.Time) bool` (used, expired, or attempts at the cap).
- Produces: `repository.TenantPasswordResetRepo` with `Create(ctx, p *models.TenantPasswordReset) error` (invalidates the user's outstanding codes first), `FindActive(ctx, tenantID primitive.ObjectID, email string) (*models.TenantPasswordReset, error)` (newest unused, unexpired), `RecordAttempt(ctx, id primitive.ObjectID) error`, `MarkUsed(ctx, id primitive.ObjectID) (bool, error)` (conditional on unused), `InvalidateForUser(ctx, tenantID, userID primitive.ObjectID) error`.
- Produces (unexported, pure): `activeFilter(tenantID primitive.ObjectID, email string, now time.Time) bson.M`, `invalidateFilter(tenantID, userID primitive.ObjectID) bson.M`.
- Produces: `password.DummyCompare()`: spends a bcrypt comparison's time against a fixed hash.

- [ ] **Step 1: Write the failing tests.** `TestSpent`: unused and unexpired and under the cap is false; used true; expired true; attempts at 5 true. `TestActiveFilter`: asserts `tenant_id`, `email`, `used_at: {$exists: false}`, `expires_at: {$gt: now}`. `TestInvalidateFilter`: asserts `tenant_id`, `user_id`, `used_at: {$exists: false}`. **Review Focus 1:** the active filter must include `tenant_id`.
- [ ] **Step 2: Run to verify failure.** `go test ./internal/models/ ./internal/repository/ -count=1`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement** the model, the repo and `DummyCompare`. Add two indexes in `EnsureIndexes`: `(tenant_id, email, created_at desc)` and a TTL index on `expires_at` with `ExpireAfterSeconds` 3600, each with a comment saying why (same reasoning as tenantcore's).
- [ ] **Step 4: Run to verify pass.** `go test ./... -count=1`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: add the tenant password reset code model and repository`.

---

### Task 2: Mail client to tenantcore

**Files:**
- Create: `digitalservice/internal/notify/client.go`
- Test: `digitalservice/internal/notify/client_test.go`

**Interfaces:**
- Produces: `notify.Config { BaseURL, ServiceKey string; Timeout time.Duration }`; `func NewClient(cfg Config) *Client` (nil when `BaseURL` or `ServiceKey` is blank; default timeout 5s); `func (c *Client) Available() bool` (nil-safe); `func (c *Client) Send(ctx context.Context, to, template string, data map[string]string) error`.
- Behaviour: `POST {BaseURL}/api/v1/svc/notifications/email` with header `X-Service-Key` and JSON `{ "to", "template", "data" }`. Any non-2xx is an error naming the status; `503` and `429` get distinct wording.

- [ ] **Step 1: Write the failing tests** against `httptest`. `TestSendPostsTheTemplateWithTheServiceKey`: asserts method, path, `X-Service-Key`, and the decoded body. `TestSendReportsNon2xx`: 401, 429, 503 and 500 each return an error containing the status; the 503 error says mail is not configured on the platform. `TestNilClientIsUnavailable`: `NewClient(Config{})` is nil, `Available()` false on it, and `Send` on it errors rather than panics. **Review Focus 3:** `TestSendGivesUpOnAServerThatNeverAnswers`: a handler that blocks past a 100 ms timeout; `Send` returns an error in under 1 s.
- [ ] **Step 2: Run to verify failure.** `go test ./internal/notify/ -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement** `Client` with an `http.Client` carrying the timeout.
- [ ] **Step 4: Run to verify pass.** `go test ./... -count=1`.
- [ ] **Step 5: Commit** `feat: add a client for tenantcore's mail route`.

---

### Task 3: The reset service

**Files:**
- Create: `digitalservice/internal/service/tenant_password_reset_service.go`
- Test: `digitalservice/internal/service/tenant_password_reset_service_test.go`

**Interfaces:**
- Consumes: Task 1 repo methods; Task 2 `notify.Client.Send`; `password.Hash`, `password.DummyCompare`; `TenantUserRepo.FindByTenantAndEmail`, `TenantUserRepo.UpdatePassword(ctx, tenantID, id primitive.ObjectID, hash string) error`; `TenantRepo.FindByID`.
- Produces narrow interfaces `resetUsers`, `resetCodes`, `resetTenants`, `resetMailer` (`Available() bool`, `Send(ctx, to, template string, data map[string]string) error`).
- Produces: `NewTenantPasswordResetService(users resetUsers, codes resetCodes, tenants resetTenants, mail resetMailer, log *zap.Logger) *TenantPasswordResetService`; `Request(ctx, tenantID primitive.ObjectID, email string) error` (only error: `apierr.FeatureUnavailable("email")` when mail is off); `Confirm(ctx, tenantID primitive.ObjectID, email, code, newPassword string) error`; `Drain()`.
- Mail data for `password_reset_code`: `app` = `<tenant name> admin`, `name`, `code`, `expires_in` = `10 minutes`; `password_changed`: `app`, `name`.

- [ ] **Step 1: Write the failing tests** with in-memory fakes (follow `tenantcore/internal/service/password_reset_service_test.go`). `TestRequestMailsACodeToAnActiveUser` (template, data, hash stored not the code, unexpired). `TestRequestRevealsNothingAboutTheAccount` (unknown address and suspended user: nil error, no mail). `TestRequestReportsWhenMailIsOff` (error). `TestRequest_DoesNotWaitForTheMail` and `TestRequest_ResponseDoesNotDependOnTheAccountLookup`: a mailer / user source that blocks until released; `Request` must return within 1 s, and after release plus `Drain()` exactly one mail is sent. `TestConfirmSetsThePasswordAndConsumesTheCode` (password verifies against the stored hash; `password_changed` sent). `TestConfirmRejectsAReusedCode`, `TestConfirmRejectsAnExpiredCode`, `TestConfirmBurnsTheCodeAfterTooManyWrongGuesses` (the right code fails after 5 wrong), `TestConfirmFailuresAreIndistinguishable` (wrong code vs no code vs unknown email yield equal error text), `TestConfirmRejectsAShortPassword`, `TestEmailIsCaseInsensitive`. **Review Focus 1:** `TestConfirmRejectsACodeFromAnotherTenant`: the same email in tenants A and B; a code requested in A is refused for B. **Review Focus 2:** `TestConfirmRefusesAUserSuspendedAfterTheCodeWasIssued`.
- [ ] **Step 2: Run to verify failure.** `go test ./internal/service/ -run "TenantPasswordReset|Request|Confirm" -count=1`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement** the service. `Request` checks `mail.Available()`, then does everything else in `issue(ctx, tenantID, email)` inside a goroutine tracked by a `sync.WaitGroup` with a fresh `30s` context (the request context dies when the handler returns). `Confirm` re-reads the user and refuses a non-active one before consuming the code.
- [ ] **Step 4: Run to verify pass.** `go test ./... -count=1`; then the new tests with `-count=30` to catch flakiness.
- [ ] **Step 5: Commit** `feat: add the tenant password reset service`.

---

### Task 4: Routes, wiring and documentation

**Files:**
- Create: `digitalservice/internal/api/tenant/public/password_reset.go`
- Modify: `digitalservice/internal/api/tenant/public/public.go`, `digitalservice/internal/api/tenant/tenant.go`, `digitalservice/internal/api/router.go`, `digitalservice/internal/api/server.go`, `digitalservice/internal/bootstrap/wiring.go`, `digitalservice/internal/bootstrap/bootstrap.go`, `digitalservice/internal/config/config.go`, `digitalservice/.env.example`, `digitalservice/internal/api/docs/docs/openapi.json`
- Test: `digitalservice/internal/api/guard_test.go`, `digitalservice/internal/config/config_test.go`

**Interfaces:**
- Consumes: Task 3 service. Produces `POST /api/v1/password-reset/request` and `/confirm` as specified, `Deps.PasswordReset *service.TenantPasswordResetService` threaded `api.Deps` → `tenant.Deps` → `public.Deps`.
- Produces: `Config.PasswordResetEnabled() bool` (true when `TenantcoreURL` and `TenantcoreServiceKey` are both set) and a `Features()` entry `password_reset`, disabled-detail naming both variables.

- [ ] **Step 1: Write the failing tests.** `guard_test.go`: both new routes appear in the route table and answer `401` without an `X-API-Key` (the existing `TestEveryTenantRouteRequiresAPIKey` walks the table, so confirm it covers them and add an explicit assertion for these two). `config_test.go`: `TestPasswordResetNeedsTheTenantcoreLink` over the four combinations of URL and key set or blank, including that the disabled `Detail` names both variables.
- [ ] **Step 2: Run to verify failure.** `go test ./internal/api/ ./internal/config/ -count=1`.
- [ ] **Step 3: Implement** the controller (bind `email`; `confirm` binds `email`, `code`, `new_password`; `request` response `{ sent: true, message: "If that address belongs to an account, a code is on its way." }`; `confirm` response `{ reset: true }`), mount both on `exempt.Group("", d.AuthRateLimit)` beside `/login`, build the `notify.Client` in `wiring.go` from the existing config, and construct the service. Document both routes in `openapi.json` (CRLF preserved).
- [ ] **Step 4: Run to verify pass.** `go build ./... && go vet ./... && go test ./... -count=1`. Start digitalservice and confirm `/readyz` lists `password_reset` as enabled.
- [ ] **Step 5: Commit** `feat: add tenant password reset routes`.

---

### Task 5: Travel admin page

**Files:**
- Create: `admin/src/app/forgot-password/page.tsx`, `admin/src/app/forgot-password/actions.ts`, `admin/src/app/forgot-password/ForgotPasswordForm.tsx`
- Modify: `admin/src/app/login/page.tsx` (link and `?reset=1` banner)

**Interfaces:**
- Consumes: `apiPost` from `admin/src/lib/api/client.ts` (server-side, carries `X-API-Key`), `ApiError`.
- Produces: `requestResetAction(prev, formData): Promise<{ error?: string; sentTo?: string }>`; `confirmResetAction(prev, formData)` that redirects to `/login?reset=1` on success. Client-side checks before any request: a 6-digit code, a password of at least 8 characters, matching confirmation.
- The `503` response reads: email isn't set up on this server. The `429` response reads: too many attempts, wait a minute. **Review Focus 5.**

The console has no test runner; verification is typecheck, lint and the browser.

- [ ] **Step 1: Implement** the actions, page, form (the code step as its own component so state resets per request) and the login link and banner. Follow the styling of `admin/src/app/login/page.tsx`.
- [ ] **Step 2: Verify statically.** `npx tsc --noEmit && npx eslint src/app/forgot-password src/app/login --max-warnings=0`. Expected: both exit 0.
- [ ] **Step 3: Click through** (digitalservice running): an unknown address advances with the same wording; mismatched passwords are caught without a request; a wrong code shows the single "not valid" message; "start over" then a new request shows no stale error; `?reset=1` shows the banner.
- [ ] **Step 4: Commit** `feat: add a forgot-password page to the travel admin`.

---

### Task 6: Live verification and wrap-up

**Files:** a throwaway tenant and user in digitalservice's database, removed afterwards; memory notes.

This sends a real email to `munkherdene@bdsec.mn` and changes the password of a **throwaway** user only, never E&S's real admin.

- [ ] **Step 1: Create the throwaway.** A tenant named `ZZ-THROWAWAY reset test` and one active user with the email `munkherdene@bdsec.mn`, inserted directly into digitalservice's database by a one-off helper that refuses any tenant not named `ZZ-THROWAWAY…`. Record its API key in the scratch directory only.
- [ ] **Step 2: Request a code** through the travel admin with that tenant's key; confirm the email arrives (the user confirms) and the user completes a reset to a throwaway password; confirm the old password no longer signs in and the new one does.
- [ ] **Step 3: Review Focus 4.** Give the throwaway tenant a cancelled subscription in tenantcore (mirrored at the same id), wait out the 60 s entitlement cache, confirm a gated write returns `402`, and confirm `POST /password-reset/request` still returns `200`.
- [ ] **Step 4: Timing.** Over paced requests (the rate limit is 10 per minute) confirm a real and an unknown address answer within noise of each other.
- [ ] **Step 5: Clean up** the throwaway tenant, user, reset codes and mirrored tenantcore records; run each repo's full checks again.
- [ ] **Step 6: Record** the feature in memory and commit anything outstanding.
