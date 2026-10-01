# Subscription management and expiry notice

Date: 2026-10-01
Repos: `tenantcore` (backend), `innonomads/admin` (core admin console)
Status: design approved in conversation; written spec awaiting review

## Purpose

The platform operator (Munkh-Erdene) needs to manage tenant subscriptions
from the core admin instead of raw API calls, and needs to be told by email
when a tenant's subscription is about to expire so it can be renewed before
the tenant's writes start returning 402.

Today the console has no subscription UI at all. Creating the E&S Discovery
subscription, extending it, and cancelling a test subscription were all done
with curl or direct database edits. tenantcore has no scheduler: the only
background work is a rate-limiter cleanup.

## Success criteria

1. From a tenant's page in the core admin the operator can view, create,
   change, renew and cancel that tenant's subscription.
2. Seven days before a subscription's `current_period_end`, one email goes to
   the operator's address. Exactly one per period, surviving restarts and
   multiple instances.
3. Renewing or changing a plan re-arms the warning for the new period without
   any reset step.
4. A subscription renews on a fixed day of the month (the **billing day**,
   default the 20th) instead of drifting by 30 days each time.
5. Everything is built in tenantcore and the core admin only. digitalservice
   is not touched.

## Decisions taken in conversation

| Question | Decision |
|---|---|
| Console actions | View, Subscribe / change plan, Renew / extend, Cancel |
| Warning schedule | 7 days before expiry, once per period |
| Scheduler | In-process ticker inside tenantcore (hourly, plus once at startup) |
| Recipient | The operator, configured by `EXPIRY_NOTICE_EMAIL`. Not the tenant's own contact address |
| Renewal day | A per-subscription billing day, 1 to 28, default 20. Renew, Subscribe and Change plan all end on it |

## Corrected assumption: cancel is reversible

An earlier statement in this design conversation said cancel could not be
undone. That was wrong. `PUT /admin/tenants/:id/subscription/plan` sets the
status to `active`, clears `canceled_at` and starts a fresh period, so Change
plan doubles as reactivate. Cancel therefore needs a confirmation step but is
not described as irreversible.

It also exposes a trap this design must defuse: Change plan restarts the
period from today (`start = now`, `end = now + plan period`). Changing plan on
a subscription with 20 days left discards those 20 days. Renew, by contrast,
extends from the existing end date.

## Backend (tenantcore)

### Data

`models.Subscription` gains one optional field:

```go
ExpiryNoticeFor *time.Time `bson:"expiry_notice_for,omitempty" json:"-"`
```

It holds the `current_period_end` value that a warning has already been
claimed for. It is keyed to the date, not to a boolean, so any change to the
period end (renew, change plan) makes it differ from the new end and re-arms
the warning with no reset code. It is `json:"-"`: internal bookkeeping, not
part of the wire contract.

### Notifier

`service.ExpiryNotifier.NotifyExpiring(ctx, now)`:

1. Find subscriptions with status `active` or `trialing` and
   `current_period_end` in `(now, now + 7 days]`.
2. For each, skip it if `expiry_notice_for` equals its `current_period_end`.
3. **Claim**: conditional `UpdateOne` setting `expiry_notice_for =
   current_period_end` where `_id` matches and the marker is missing or
   different. Only if `ModifiedCount == 1` does this caller proceed. This is
   what makes a restart or a second replica unable to double-send.
4. Resolve the tenant name and plan name, then send the
   `subscription_expiring` template to `EXPIRY_NOTICE_EMAIL`.
5. If sending fails, **release the claim** (unset the marker, conditionally on
   it still equalling this period end), log WARN, and move on. The next tick
   retries.

Trade-off, accepted: claiming before sending means a process crash between the
claim and the send loses that one warning. The alternative, sending before
marking, would send twice after the same crash, and two replicas running at the
same moment would both send, because neither has marked yet. Claim-first makes
the second replica's claim fail, so only one sends. A lost warning is
recoverable, since the operator can still read the end date in the console.

Subscriptions that are cancelled, past due or already expired are never warned
about.

The function takes `now` as a parameter and depends on narrow interfaces
(subscription store, tenant lookup, plan lookup, mail sender), following
`PasswordResetService`, so it is testable without MongoDB, SMTP or waiting.

### Ticker

Started in `bootstrap` only when both mail and `EXPIRY_NOTICE_EMAIL` are
configured. Runs `NotifyExpiring` once at startup, then every hour, and stops
when the application closes. Errors are logged, never fatal.

### Configuration

| Variable | Meaning |
|---|---|
| `EXPIRY_NOTICE_EMAIL` | Operator address that receives warnings. Blank disables the feature |

`config.Features()` gains an `expiry_notice` entry, disabled when either the
address or the Gmail credentials are missing, with a detail string naming what
to set. It therefore appears on `/readyz` and in the startup log like every
other optional capability.

The value for this deployment is `munkherdene.ts9234@gmail.com`, set in
`tenantcore/.env`, which is gitignored. It is not hardcoded in source.

### Mail template

New `mailer.TemplateSubscriptionExpiring` = `subscription_expiring`. Required
data: `app`, `tenant`, `plan`, `ends_on`, `days_left`. Subject names the tenant
and the number of days. Same rules as every template: the caller supplies data
only, the sender is fixed server-side.

### Renew endpoint

`POST /api/v1/admin/tenants/{id}/subscription/renew`, superadmin bearer token.

- Body: none. A custom length is deliberately out of scope.
- New end = `billingAlignedEnd(base, plan period, billing day)` where
  `base = max(now, current_period_end)`. A lapsed subscription is renewed from
  today; a live one keeps its remaining days. See "Billing day".
- Status is set to `active`.
- A **cancelled** subscription returns `409` with a message pointing to
  Change plan, because renewing a cancelled subscription silently would hide
  that the tenant was cancelled on purpose.
- No subscription returns `404`.
- Response: the updated subscription, same shape as the existing subscription
  endpoints.

The existing `GET/POST /subscription`, `PUT /subscription/plan` and
`POST /subscription/cancel` are unchanged. The new route is documented in
`docs/api.json`; the existing route-table test enforces this.

## Billing day (added 2026-10-01)

The business bills on the 20th of the month, so a subscription should end on
the 20th and stay there. Renewing "+30 days" drifts: E&S, subscribed on 1 Oct,
ended on 31 Oct, and each renewal would have slid further from the 20th.

### Data

`models.Subscription` gains `BillingDay int` (`bson:"billing_day,omitempty"`,
`json:"billing_day"`), valid 1 to 28 (28 so every month has the day).
`DefaultBillingDay = 20`. A subscription with no day stored, which is every
existing one, behaves as day 20 through `EffectiveBillingDay()`; nothing needs
a migration.

### The rule

`billingAlignedEnd(base, periodDays, billingDay)`:

1. `earliest = base + max(1 day, periodDays / 2 days)`: at least half a period.
2. `end` is the first billing-day date at 00:00 UTC on or after `earliest`.
3. For plans longer than one month, add whole months:
   `months = max(1, (periodDays + 15) / 30)`, and `end = end + (months - 1)`
   months.

The half-period floor is what stops a renewal near the 20th producing a
5-day period. For a 30-day plan and billing day 20:

| Starting point (`base`) | Result |
|---|---|
| ends 20 Oct | 20 Nov |
| ends 31 Oct | 20 Nov |
| ends 15 Oct | 20 Nov (not 20 Oct, which would be 5 days) |
| lapsed, renewed 3 Oct | 20 Oct (17 days: a short first period) |

The first period after switching to a billing day can therefore be shorter than
a month. Every period after that lands on the day.

The time is 00:00 UTC, matching the `2026-10-20` extension already applied to
E&S, so a subscription stops at 08:00 local on its billing day.

### Where it applies

- **Renew**: `base = max(now, current_period_end)`.
- **Subscribe**: `base = now`; accepts an optional `billing_day`, default 20.
- **Change plan**: `base = now`, using the subscription's existing day. Still
  restarts the period from today, which still discards remaining days.

### Editing the day

`PUT /api/v1/admin/tenants/{id}/subscription/billing-day` with
`{ "billing_day": 20 }`, superadmin only. `400` outside 1 to 28, `404` with no
subscription. **It changes future periods only, never `current_period_end`.**
Moving the current end earlier would silently take days the tenant already paid
for; Renew is how the current end moves.

### Consequences

- The expiry warning needs no change: it is keyed to the period-end date, which
  is now a billing-day date.
- E&S ended 31 Oct because it was subscribed 30 days out. It was moved to
  20 Oct by hand at the operator's request, and this feature keeps it there.

## Console (core admin, `innonomads/admin`)

A **Subscription** card on the tenant page, backed by server actions that call
the routes above.

- **View**: plan, status pill, period start to end, days remaining. Empty state
  when the tenant has no subscription, with a Subscribe form.
- **Subscribe** (no subscription): choose a plan. Sends `plan_id`.
- **Change plan** (has subscription): choose a plan. Shows the resulting end
  date, `today + plan period`, **before** submit, with a note that the current
  period is replaced.
- **Renew**: shows the resulting end date before submit.
- **Billing day**: a 1 to 28 field with its own Save, saying that it affects
  future periods only. Subscribe has the same field, defaulting to 20. Every
  date preview uses the billing-day rule above.
- **Cancel**: confirmation step. Wording states that Change plan can reactivate.

Naming: subscription routes bind `plan_id`; package assignment binds
`package_id`. The two differ on purpose and the console must not unify them.
Responses may carry `null` collections; types must admit it.

## Error handling

| Situation | Behaviour |
|---|---|
| Mail or `EXPIRY_NOTICE_EMAIL` not configured | Ticker does not start; `/readyz` reports the feature off |
| Send fails | Claim released, WARN logged, retried next hour |
| Tenant or plan lookup fails for one subscription | That subscription is skipped and logged; others proceed |
| Mongo unreachable during a tick | Tick logged and abandoned; next tick retries |
| Renew on cancelled | 409 with guidance |

## Testing

Unit, on the dev toolchain, no database, no SMTP, fake clock:

- Warns exactly once for a subscription inside the 7-day window.
- Does not warn for one ending in more than 7 days.
- Does not warn for cancelled, past due or already expired.
- Re-warns after the period end changes (renew, change plan).
- Releases the claim when the send fails, and warns on the retry.
- Two concurrent runs over the same subscription send once.
- Renew arithmetic: lapsed renews from today; live keeps remaining days;
  cancelled is 409; missing is 404.

Live, against the running stack: create a throwaway tenant (named
`ZZ-THROWAWAY`, removed afterwards) whose subscription ends in 3 days, restart
tenantcore, confirm one real email arrives and a second restart sends none.
Gmail SMTP has never authenticated against the app password, so this is also
the first real proof that mail works; a failure here is a finding about mail
configuration, not necessarily about this feature.

Console: typecheck and a click-through of each action against the live
tenantcore, since a typecheck cannot catch a wrong wire field name.

## Out of scope

- Emailing the tenant's own contact address.
- An "expired today" notice, or any second warning.
- Automatically suspending a tenant on expiry. The 402 gate already blocks
  writes; suspension is an administrative decision.
- A custom renew length, or a rolling "+N days" mode: every subscription has a billing day.
- Any change in digitalservice.
- Multi-instance coordination beyond the atomic claim.
