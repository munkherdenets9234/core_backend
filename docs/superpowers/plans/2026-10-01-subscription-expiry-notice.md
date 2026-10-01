# Subscription management and expiry notice Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the operator manage a tenant's subscription from the core admin, and email the operator once, seven days before a subscription's period ends.

**Architecture:** tenantcore gains a `NotifyExpiring(now)` function that claims each warning with an atomic conditional update before sending, run by an in-process hourly ticker. A new `renew` endpoint extends a period. The core admin gets a Subscription page that calls the existing and new subscription routes. Nothing in digitalservice changes.

**Tech Stack:** Go, Gin, MongoDB driver v1, zap (tenantcore); Next.js 16, React 19, server actions (core admin).

**Spec:** `tenantcore/docs/superpowers/specs/2026-10-01-subscription-expiry-notice-design.md`

All paths below are relative to `D:\bkup\projects\digitalbrochure`. Go commands run in `tenantcore/`; console commands run in `innonomads/admin/`.

## Global Constraints

- Warning is sent **7 days before** `current_period_end`, **once per period**.
- Recipient comes from `EXPIRY_NOTICE_EMAIL`, set in `tenantcore/.env` (gitignored). The address is never hardcoded in source. This deployment's value is `munkherdene.ts9234@gmail.com`.
- Ticker runs **once at startup, then hourly**, and does not start unless mail and `EXPIRY_NOTICE_EMAIL` are both configured.
- Only `active` and `trialing` subscriptions are warned about.
- **Claim before send**; release the claim if the send (or a lookup) fails.
- The marker is the `current_period_end` value warned about, stored as `expiry_notice_for`, `json:"-"`.
- Renew: new end = `max(now, current_period_end)` + one plan period. Cancelled returns `409`; no subscription returns `404`. No request body.
- Subscription routes bind `plan_id`. Package assignment binds `package_id`. Do not unify them.
- Mail is template-only; the sender is fixed server-side.
- Responses may carry `null` collections; console types must admit `null`.
- Do not modify digitalservice.
- `docs/api.json` uses CRLF line endings. Preserve them.
- Commit messages end with the `Co-Authored-By` trailer the session specifies.

## Review Focus

Failure modes the spec implies that no obvious test covers. Each has a test in its owning task.

1. A subscription ending exactly at the window edges (exactly `now`, exactly `now+7d`, one second past) must be handled as the spec says. Owner: Task 4.
2. A tenant or plan lookup that fails **after** the claim must release it, or the warning is silently lost for that period. Owner: Task 4.
3. One subscription failing to send must not stop the others in the same tick. Owner: Task 4.
4. `days_left` for a live subscription must never read `0` or round down: 6 days 23 hours reads 7, 1 hour reads 1. Owner: Task 4.
5. The console must render a subscription whose plan was deleted (`plan` is null) and a cancelled one without crashing, and must not offer Renew on a cancelled one. Owner: Task 8.

---

### Task 1: Expiry mail template and template-enum drift guard

**Files:**
- Modify: `tenantcore/pkg/mailer/template.go`
- Modify: `tenantcore/docs/api.json` (the `template` enum under `/api/v1/svc/notifications/email`)
- Test: `tenantcore/pkg/mailer/template_test.go`, `tenantcore/internal/api/openapi_test.go`

**Interfaces:**
- Produces: `mailer.TemplateSubscriptionExpiring Template = "subscription_expiring"`. Required data keys: `app`, `tenant`, `plan`, `ends_on`, `days_left`.

- [ ] **Step 1: Write the failing tests**

In `template_test.go`:
- `TestSubscriptionExpiringRenders`: render with `{app:"Inno Nomads Console", tenant:"E and S Discovery Mongolia", plan:"Travel Pro", ends_on:"2026-10-31", days_left:"7"}`. Assert the subject contains `E and S Discovery Mongolia` and `7`; the body contains `2026-10-31` and `Travel Pro`; neither contains `{{`.
- `TestSubscriptionExpiringRequiresAllData`: omit `days_left`; assert an error naming `days_left`.

In `openapi_test.go`:
- `TestDocsTemplateEnumMatchesMailer`: load `docs/api.json`, read the `template` enum of `POST /api/v1/svc/notifications/email`, assert it equals `mailer.Names()` (both sorted). This also catches the existing drift: `password_reset_code` is a template but is missing from the enum.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./pkg/mailer/ ./internal/api/ -run "SubscriptionExpiring|DocsTemplateEnum" -count=1`
Expected: FAIL (`undefined: TemplateSubscriptionExpiring`, then the enum test fails on the missing templates).

- [ ] **Step 3: Implement**

Add `TemplateSubscriptionExpiring` and its `templateDef` in `template.go`. Subject: `{{tenant}}: subscription ends in {{days_left}} days`. Body states tenant, plan, end date, days left, and that the tenant's writes return 402 once it lapses. Update the `docs/api.json` enum to list every name in `mailer.Names()`, preserving CRLF (read bytes, normalise to LF, edit, write back as CRLF).

- [ ] **Step 4: Run to verify pass**

Run: `go test ./pkg/mailer/ ./internal/api/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tenantcore/pkg/mailer tenantcore/internal/api/openapi_test.go tenantcore/docs/api.json
git commit -m "feat: add subscription_expiring mail template and a docs enum drift guard"
```

---

### Task 2: Configuration and readiness

**Files:**
- Modify: `tenantcore/internal/config/config.go`, `tenantcore/.env.example`
- Test: `tenantcore/internal/config/config_test.go`

**Interfaces:**
- Produces: `Config.ExpiryNoticeEmail string` (env `EXPIRY_NOTICE_EMAIL`); `func (c Config) ExpiryNoticeEnabled() bool`, true only when `EmailEnabled()` and `ExpiryNoticeEmail != ""`; a `Features()` entry named `expiry_notice`.

- [ ] **Step 1: Write the failing test**

`TestExpiryNoticeNeedsMailAndAddress`: table over (mail on/off) x (address set/blank). Assert `ExpiryNoticeEnabled()` is true only for on+set, and that the `expiry_notice` entry in `Features()` agrees. Assert the disabled `Detail` names `EXPIRY_NOTICE_EMAIL`.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/config/ -run ExpiryNotice -count=1`
Expected: FAIL (field and method undefined).

- [ ] **Step 3: Implement**

Add the field, load it in `Load()` with `getEnv("EXPIRY_NOTICE_EMAIL", "")`, add the method and the feature entry. Document the variable in `.env.example` with a comment: operator address, blank disables, requires the Gmail variables.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/config/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tenantcore/internal/config tenantcore/.env.example
git commit -m "feat: add EXPIRY_NOTICE_EMAIL config and expiry_notice readiness entry"
```

---

### Task 3: Marker field and repository methods

**Files:**
- Modify: `tenantcore/internal/models/billing.go` (the `Subscription` struct), `tenantcore/internal/repository/billing_repo.go`
- Test: `tenantcore/internal/repository/billing_repo_test.go` (create)

**Interfaces:**
- Produces: `models.Subscription.ExpiryNoticeFor *time.Time` with tag `bson:"expiry_notice_for,omitempty" json:"-"`.
- Produces, on `*SubscriptionRepo`:
  - `FindExpiring(ctx context.Context, after, through time.Time) ([]*models.Subscription, error)`: status in `active`, `trialing`; `current_period_end > after` and `<= through`.
  - `ClaimExpiryNotice(ctx context.Context, id primitive.ObjectID, periodEnd time.Time) (claimed bool, err error)`.
  - `ReleaseExpiryNotice(ctx context.Context, id primitive.ObjectID, periodEnd time.Time) error`.
  - `ExtendPeriod(ctx context.Context, tenantID primitive.ObjectID, newEnd time.Time, userID *primitive.ObjectID) error`: sets `current_period_end`, `status=active`, `updated_at`, and `user_id` when non-nil; returns `mongo.ErrNoDocuments` when nothing matched.
- Produces (unexported, pure, so the filters are testable without MongoDB): `expiringFilter(after, through time.Time) bson.M`, `claimFilter(id primitive.ObjectID, periodEnd time.Time) bson.M`, `releaseFilter(id primitive.ObjectID, periodEnd time.Time) bson.M`.

- [ ] **Step 1: Write the failing tests**

- `TestExpiringFilter`: assert `status` is `{$in: [active, trialing]}` and `current_period_end` is `{$gt: after, $lte: through}`.
- `TestClaimFilter`: assert `_id` equals the id, `current_period_end` equals `periodEnd` (so a renewal between find and claim fails the claim), and `expiry_notice_for` is `{$ne: periodEnd}` (which also matches a missing field).
- `TestReleaseFilter`: assert `_id` equals the id and `expiry_notice_for` equals `periodEnd` (so releasing never clears a newer marker).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/repository/ -count=1`
Expected: FAIL (functions undefined).

- [ ] **Step 3: Implement**

Add the field, the three pure filter builders and the four methods. `ClaimExpiryNotice` returns `ModifiedCount == 1`. `ReleaseExpiryNotice` uses `$unset`. The repository methods themselves need MongoDB; they are exercised live in Task 9.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/repository/ -count=1 && go build ./...`
Expected: PASS, build clean.

- [ ] **Step 5: Commit**

```bash
git add tenantcore/internal/models tenantcore/internal/repository
git commit -m "feat: add expiry-notice marker and repository claim/release methods"
```

---

### Task 4: The notifier

**Files:**
- Create: `tenantcore/internal/service/expiry_notifier_service.go`
- Test: `tenantcore/internal/service/expiry_notifier_service_test.go`

**Interfaces:**
- Consumes: the Task 3 repository methods; `sender` (declared in `password_reset_service.go`: `Available() bool`, `Send(to string, tmpl mailer.Template, data map[string]string) error`); `mailer.TemplateSubscriptionExpiring`.
- Produces:
  - `const ExpiryWarnWindow = 7 * 24 * time.Hour`
  - narrow interfaces `expirySubs` (the three repo methods `FindExpiring`, `ClaimExpiryNotice`, `ReleaseExpiryNotice`), `expiryTenants` (`FindByID(ctx, id) (*models.Tenant, error)`), `expiryPlans` (`FindByID(ctx, id) (*models.Plan, error)`).
  - `func NewExpiryNotifier(subs expirySubs, tenants expiryTenants, plans expiryPlans, mail sender, to string, log *zap.Logger) *ExpiryNotifier`
  - `func (n *ExpiryNotifier) NotifyExpiring(ctx context.Context, now time.Time) (sent int, err error)`. The error is only for a failed `FindExpiring`; per-subscription failures are logged and skipped.
  - `func daysLeft(now, end time.Time) int`: whole days rounded **up**.

The mail data is `app` = `AppName`, `tenant` = tenant name, `plan` = plan name (fall back to the plan slug, then `your plan`, when the plan is missing), `ends_on` = `end.UTC().Format("2006-01-02")`, `days_left` = decimal string.

Order per subscription, from the spec: skip if marker equals period end; claim; resolve tenant and plan; send; on any failure after the claim, release and continue.

- [ ] **Step 1: Write the failing tests** (fakes: an in-memory store with a mutex-guarded conditional claim, recording the `after`/`through` it was called with; a recording mailer that can be told to fail; tenant and plan maps)

- `TestNotifyExpiring_WarnsOncePerPeriod`: subscription ending `now+3d`. First call returns `sent==1`, one mail to `ops@example.com`, template `subscription_expiring`, data `tenant`, `plan`, `ends_on`, `days_left=="3"`. Second call with the same `now` returns `sent==0`.
- `TestNotifyExpiring_QueriesTheSpecWindow`: assert the store received `after == now` and `through == now + ExpiryWarnWindow` (the repo filter makes `now` exclusive and `now+7d` inclusive; this pins the arguments). **Review Focus 1.**
- `TestNotifyExpiring_ReWarnsAfterPeriodEndChanges`: warn once; move the subscription's end forward 30 days (as Renew does); advance `now` until the new end is inside the window; assert a second mail.
- `TestNotifyExpiring_SkipsWhenMarkerAlreadyMatches`: marker equals end; assert no claim call and no mail.
- `TestNotifyExpiring_ReleasesClaimWhenSendFails`: mailer fails; assert `sent==0`, marker cleared, no error returned; a later call with a working mailer sends once.
- `TestNotifyExpiring_ReleasesClaimWhenLookupFails`: tenant missing; assert marker cleared, no mail, no error. **Review Focus 2.**
- `TestNotifyExpiring_OneFailureDoesNotBlockOthers`: two subscriptions, the first send fails; assert the second still sends and `sent==1`. **Review Focus 3.**
- `TestNotifyExpiring_ConcurrentRunsSendOnce`: two goroutines call `NotifyExpiring` with the same `now`; assert exactly one mail in total.
- `TestNotifyExpiring_StoreErrorIsReturned`: `FindExpiring` fails; assert the error is returned and nothing is sent.
- `TestDaysLeft`: 6 days 23 hours -> 7; exactly 7 days -> 7; 24 hours -> 1; 1 hour -> 1. **Review Focus 4.**

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/service/ -run "NotifyExpiring|DaysLeft" -count=1`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement** `ExpiryNotifier` in `expiry_notifier_service.go`. Round up with integer arithmetic on durations, not floats.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/service/ -race -count=1`
Expected: PASS, with no data race reported. `-race` needs cgo; if it is unavailable on this machine, run without it and rely on `TestNotifyExpiring_ConcurrentRunsSendOnce`, then say so in the task report.

- [ ] **Step 5: Commit**

```bash
git add tenantcore/internal/service/expiry_notifier_service.go tenantcore/internal/service/expiry_notifier_service_test.go
git commit -m "feat: add the subscription expiry notifier"
```

---

### Task 5: Ticker and bootstrap wiring

**Files:**
- Create: `tenantcore/internal/service/every.go`
- Modify: `tenantcore/internal/bootstrap/bootstrap.go`
- Test: `tenantcore/internal/service/every_test.go`

**Interfaces:**
- Consumes: `NewExpiryNotifier`, `Config.ExpiryNoticeEnabled()`, `Config.ExpiryNoticeEmail`.
- Produces: `func Every(ctx context.Context, interval time.Duration, fn func(context.Context))`. It runs `fn` once immediately, then every `interval`, and returns when `ctx` is cancelled.

- [ ] **Step 1: Write the failing test**

`TestEveryRunsImmediatelyThenRepeatsAndStops`: interval 10 ms. Assert `fn` has run at least once before the first interval elapses, at least three times after about 50 ms, and that after cancelling the context and waiting, the count stops increasing.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/service/ -run TestEvery -count=1`
Expected: FAIL (`undefined: Every`).

- [ ] **Step 3: Implement**

Implement `Every`. In `bootstrap.New`, when `cfg.ExpiryNoticeEnabled()`: build `NewExpiryNotifier(subscriptions, tenants, plans, mail, cfg.ExpiryNoticeEmail, log)`, derive a cancellable context stored on `App` as `stopJobs context.CancelFunc`, and `go service.Every(ctx, time.Hour, ...)` where the callback calls `NotifyExpiring(ctx, time.Now())` and logs the sent count at INFO (and the error at WARN). Log one INFO line at startup stating the interval and the recipient domain only. When disabled, log a WARN naming `EXPIRY_NOTICE_EMAIL`. `App.Close` calls `stopJobs` when non-nil.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./... -count=1 && go vet ./... && go build ./...`
Expected: PASS, vet clean. Then start tenantcore and confirm `GET /readyz` lists `expiry_notice` as disabled while the variable is unset.

- [ ] **Step 5: Commit**

```bash
git add tenantcore/internal/service/every.go tenantcore/internal/service/every_test.go tenantcore/internal/bootstrap/bootstrap.go
git commit -m "feat: run the expiry notifier hourly from bootstrap"
```

---

### Task 6: Renew endpoint

**Files:**
- Modify: `tenantcore/internal/service/billing_service.go`, `tenantcore/internal/api/admin/private/tenants.go`, `tenantcore/internal/api/admin/private/private.go`, `tenantcore/docs/api.json`
- Test: `tenantcore/internal/service/renew_test.go`

**Interfaces:**
- Consumes: `SubscriptionRepo.ExtendPeriod` (Task 3).
- Produces (unexported, pure):
  - `func renewedEnd(now, currentEnd time.Time, periodDays int) time.Time` = `max(now, currentEnd)` plus `periodDays` calendar days.
  - `func renewable(status models.SubscriptionStatus) bool`: false only for `canceled`.
  - `func periodFor(plan *models.Plan) int`: `models.DefaultPeriodDays` for a nil plan, otherwise `plan.Period()`.
- Produces: `func (s *SubscriptionService) Renew(ctx context.Context, tenantID primitive.ObjectID, userID *primitive.ObjectID) (*models.Subscription, error)`: `404` when there is no subscription, `apierr.Conflict` (409) when cancelled with a message that points to Change plan, otherwise extends and returns the refreshed subscription (via `Get`).
- Produces: `POST /api/v1/admin/tenants/:id/subscription/renew` (superadmin), `tenantsController.RenewSubscription`, responding with `view.SubscriptionOf(sub)`.

- [ ] **Step 1: Write the failing tests**

- `TestRenewedEnd`: current end `now+20d`, 30-day period -> `now+50d`; current end `now-10d` -> `now+30d`; current end exactly `now` -> `now+30d`.
- `TestRenewable`: `canceled` false; `active`, `past_due`, `trialing` true.
- `TestPeriodFor`: nil plan -> `DefaultPeriodDays`; `PeriodDays: 0` -> `DefaultPeriodDays`; `PeriodDays: 90` -> 90.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/service/ -run "RenewedEnd|Renewable|PeriodFor" -count=1`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement**

Implement the three helpers and `Renew`. `Renew` must read the plan with `planRepo.FindByID` directly, not the private `plan()` helper, because that helper rejects an inactive plan and an existing subscription on a since-retired plan must still be renewable. Add the handler and route next to the other subscription routes. Document the route in `docs/api.json`: description covers the formula, the 409, the 404, and the no-body rule, preserving CRLF.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./... -count=1`
Expected: PASS, including the route-table guard (which fails until the route is documented) and the superadmin-token guard (which covers the new route automatically).

- [ ] **Step 5: Commit**

```bash
git add tenantcore/internal tenantcore/docs/api.json
git commit -m "feat: add POST /admin/tenants/:id/subscription/renew"
```

---

### Task 7: Console data layer and actions

**Files:**
- Modify: `innonomads/admin/src/lib/types.ts`, `innonomads/admin/src/app/admin/(console)/tenants/actions.ts`
- Create: `innonomads/admin/src/lib/data/subscription.ts`, `innonomads/admin/src/lib/subscription.ts`

**Interfaces:**
- Produces in `lib/types.ts`: `SubscriptionStatus = "active" | "trialing" | "past_due" | "canceled"`; `Subscription { id; tenant_id; plan_id; status: SubscriptionStatus; current_period_start: string; current_period_end: string; canceled_at?: string | null; plan?: { id: string; slug: string; name: string; price: number; currency: string; period_days: number } | null }`.
- Produces `getTenantSubscription(tenantId: string): Promise<Subscription | null>`: calls `GET /admin/tenants/{id}/subscription`, returns `null` on a 404, rethrows anything else.
- Produces in `lib/subscription.ts` (pure): `daysRemaining(now: Date, end: Date): number` (rounded up, never negative); `projectedEnd(mode: "change" | "renew", now: Date, currentEnd: Date, periodDays: number): Date` where `change` is `now + periodDays` and `renew` is `max(now, currentEnd) + periodDays`; `DEFAULT_PERIOD_DAYS = 30`.
- Produces server actions, each returning `{ error?: string }` or void: `subscribeAction(tenantId, prev, formData)` posting `{ plan_id }` to `POST /admin/tenants/{id}/subscription`; `changePlanAction(tenantId, prev, formData)` putting `{ plan_id }` to `PUT .../subscription/plan`; `renewSubscriptionAction(tenantId)` posting to `.../subscription/renew`; `cancelSubscriptionAction(tenantId)` posting to `.../subscription/cancel`. Each calls `revalidatePath("/admin/tenants/{id}/subscription")`.

The console has no test runner. Verification is typecheck, lint, and the click-through in Task 8.

- [ ] **Step 1: Commit the existing uncommitted assign-package fix on its own**

The `package_id` and `Plan[]` read fixes in `actions.ts` and `lib/data/packages.ts` are uncommitted. Run `git -C innonomads/admin status`, stay on the current branch, and commit only those two files with the message `fix: send package_id and read the Plan[] shape when assigning packages`.

- [ ] **Step 2: Implement the types, data function, helpers and actions** as specified above. Note in a comment on the actions that `plan_id` here and `package_id` in `assignPackageAction` differ on purpose.

- [ ] **Step 3: Verify**

Run: `npx tsc --noEmit && npx eslint src --max-warnings=0`
Expected: both exit 0.

- [ ] **Step 4: Commit**

```bash
git add innonomads/admin/src/lib innonomads/admin/src/app/admin/\(console\)/tenants/actions.ts
git commit -m "feat: add subscription data layer and actions to the console"
```

---

### Task 8: Console Subscription page

**Files:**
- Create: `innonomads/admin/src/app/admin/(console)/tenants/[id]/subscription/page.tsx`, `innonomads/admin/src/components/SubscriptionPanel.tsx`
- Modify: `innonomads/admin/src/app/admin/(console)/tenants/page.tsx` (add a "Subscription" link beside the existing Packages link)

**Interfaces:**
- Consumes: `getTenantSubscription`, `getTenantById`, `listPackages` (plans for the picker), the Task 7 actions and helpers.
- Produces: `SubscriptionPanel({ tenantId, subscription, plans })`, a client component using `useActionState`, following `AssignPackageForm` for form state and styling.

Behaviour, per the spec:
- No subscription: empty state with a Subscribe form (plan picker).
- Has subscription: plan name, status pill, period start to end, days remaining.
- Change plan form: shows `projectedEnd("change", ...)` **before** submit, with the note that the current period is replaced.
- Renew button: shows `projectedEnd("renew", ...)` before submit. **Hidden when the status is `canceled`**, replaced by a line pointing to Change plan.
- Cancel button: requires a confirmation step; wording states that Change plan can reactivate. **Hidden when already cancelled.**
- A `null` `plan` renders `Plan removed` and does not crash. **Review Focus 5.**

- [ ] **Step 1: Implement the page and panel**, then add the link.

- [ ] **Step 2: Verify statically**

Run: `npx tsc --noEmit && npx eslint src --max-warnings=0`
Expected: both exit 0.

- [ ] **Step 3: Click-through against the live stack** (tenantcore and the console running)

Open `/admin/tenants/6a47a932c11d67fbde0d0cd4/subscription` (E&S) and confirm it shows Travel Pro, active, ending 2026-10-31. Then on a throwaway tenant (created and removed in Task 9): Subscribe works; Change plan previews the end date and applies it; Renew previews and applies it; Cancel asks for confirmation, then the panel shows cancelled with Renew hidden; Change plan reactivates it. Confirm a subscription whose plan was deleted renders `Plan removed`. A typecheck cannot catch a wrong wire field, so every action must be exercised once.

- [ ] **Step 4: Commit**

```bash
git add innonomads/admin/src
git commit -m "feat: add the Subscription page to the core admin"
```

---

### Task 9: Live verification of the warning email, and wrap-up

**Files:**
- Modify: `tenantcore/.env` (gitignored; not committed), memory notes.
- Create then delete: a throwaway Go helper under `tenantcore/cmd/`.

This task proves the one thing unit tests cannot: a real email arrives. **Gmail SMTP has never authenticated against this app password, so a failure here may be a mail-configuration finding, not a bug in this feature.** If the send fails with an auth error, stop and report it before changing any code.

- [ ] **Step 1: List what would send.** Before restarting, query tenantcore for any real subscription ending within 7 days (E&S ends 2026-10-31, outside the window). If any real tenant is inside the window, tell the user before proceeding, because restarting will email about it.

- [ ] **Step 2: Create a throwaway tenant** named `ZZ-THROWAWAY expiry test` through `POST /admin/tenants`, subscribe it to `travel-pro`, then set its `current_period_end` to now plus 72 hours with a one-off Go helper that refuses any tenant whose name does not start with `ZZ-THROWAWAY`.

- [ ] **Step 3: Configure and restart.** Append `EXPIRY_NOTICE_EMAIL=munkherdene.ts9234@gmail.com` to `tenantcore/.env` (do not print the file), restart tenantcore, and confirm `/readyz` lists `expiry_notice` enabled.

- [ ] **Step 4: Verify the send.** Confirm the startup log shows one warning sent and the user confirms one email arrived with the tenant name, plan, end date and `3` days left. Restart tenantcore again and confirm the log shows `sent 0` and no second email arrives.

- [ ] **Step 5: Verify re-arming.** Renew the throwaway through the console. Confirm its end moves out roughly 30 days (it is now outside the window, so no email). Then set its end back inside the window with the helper and restart; confirm a second email arrives for the new period.

- [ ] **Step 6: Clean up.** Delete the throwaway tenant and its subscription from tenantcore, delete the helper, and confirm the tenant list matches the original. Run `go test ./... -count=1` and the console `tsc` and `eslint` once more.

- [ ] **Step 7: Record and commit.** Add a memory note covering the feature, the claim-before-send trade-off, the Change-plan-restarts-the-period trap, and that cancel is reversible. Commit any remaining tenantcore changes with `docs: record expiry notice verification`.
