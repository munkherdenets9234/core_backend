# Password reset for travel-agency admin users

Date: 2026-10-01
Repos: `digitalservice` (the flow), `admin` (the page), `tenantcore` (docs only; its mail route is reused unchanged)
Status: design approved in conversation

## Purpose

A tenant's admin users, for example E&S Discovery Mongolia's, sign in to the
travel admin (`admin/`) with an email and password. If they forget it, the only
way back today is another admin pressing "Reset password" on the Staff page, and
a tenant with a single admin has nobody to do that. They need a self-service
"forgot password" that mails them a code.

The platform admins already have exactly this in the core admin, built on
tenantcore. This is the same flow for tenant users.

## Why digitalservice owns it

Tenant users live in digitalservice. tenantcore holds no copy of them and must
not grow one, since products ask tenantcore questions and not the other way
round. So digitalservice owns the reset (codes, attempts, expiry, the password
write) and tenantcore is only the mail sender, as agreed when mail was added.

## Success criteria

1. A tenant user can request a code by email, receive it, and set a new
   password from the travel admin, without any other admin's help.
2. Nothing observable, in the response body, status or response time, depends on
   whether the address has an account.
3. A tenant whose subscription has lapsed can still reset a password.
4. No new credentials are introduced.

## Behaviour

Same rules as the platform-admin reset (`tenantcore/pkg/mailer`, `PasswordResetService`):

- A 6-digit code from `crypto/rand`, stored as a SHA-256 hash, valid for 10
  minutes, usable once. Requesting a new code invalidates earlier ones.
- At most 5 wrong guesses, then the code is burned. The code is only safe
  because of all three limits together.
- Every failure of confirm returns the same error: wrong, expired, used, or no
  code at all.
- Codes are compared in constant time. Confirm for an address with no
  outstanding code spends comparable time to one that has.
- Neither route issues a session. Signing in afterwards is a separate step.
- Suspended users cannot reset. A suspended tenant is already refused by the
  tenant middleware.

### Nothing may depend on the account

The request route returns **before any account-dependent work**. The lookup,
the code insert and the mail all run in the background with their own bounded
context. This is not hardening for its own sake: the platform-admin version
first waited for SMTP (a real account answered 1.7 s later than a fake one),
then, with only the send moved out, still leaked about 250 ms of database
round trips, which is averaged out in a few dozen requests. Both measured live.
A background failure cannot be returned to the caller; it is logged.

The one answer that may differ by configuration is `503 FEATURE_UNAVAILABLE`
when the mail link to tenantcore is not configured. That depends on deployment,
never on the account.

## digitalservice

### Routes

`POST /api/v1/password-reset/request` `{ "email" }` and
`POST /api/v1/password-reset/confirm` `{ "email", "code", "new_password" }`.

Tenant-scoped by `X-API-Key` like every route here. Rate limited by the existing
`AuthRateLimit`. Mounted **outside the subscription gate**, next to `/login`:
the gate blocks every mutating method, and a lapsed tenant still needs its
admins to be able to sign in and see their data.

`request` always returns `200 { sent: true, message }` with wording that is true
whether or not the account exists. `confirm` returns `200 { reset: true }` or
`400` with one message. A new password under 8 characters is a specific `400`;
it says nothing about the account.

### Data

New collection `tenant_password_resets`: `tenant_id`, `user_id`, `email`,
`code_hash`, `attempts`, `expires_at`, `used_at`, `created_at`. Indexed by
`(tenant_id, email, created_at desc)` for lookup, with a TTL index on
`expires_at` plus one hour so spent codes delete themselves. Same reasoning as
tenantcore: a collection of old hashed credentials has no reason to sit in every
backup.

Reset lookups include the tenant, so a code issued for one tenant's user can
never be used against another's, even with the same email address.

### Service

`TenantPasswordResetService` with narrow interfaces for the user source, the
code store and the mail sender, as `PasswordResetService` has, so every rule
above is testable without a database or a network.

### Mail

A small client, `internal/notify`, posts to tenantcore's
`POST /api/v1/svc/notifications/email` with the **existing**
`TENANTCORE_URL` and `TENANTCORE_SERVICE_KEY`. It sends the `password_reset_code`
template, and `password_changed` after a successful reset. The `app` data field
names the tenant ("E and S Discovery Mongolia admin"), looked up in the
background.

If the link is not configured, `request` answers `503`, as tenantcore's does,
because no code can ever be delivered and saying otherwise leaves a person
waiting for an email that cannot arrive.

The client has a short timeout, since it runs after the response and a hung
connection should not pile up goroutines.

## admin (the travel admin)

`/forgot-password`, public, outside the signed-in area like `/login`:

1. Email, then a "Send code" button.
2. Code, new password and confirmation. Mismatched or short passwords are
   caught before the request, so a typo does not spend one of the five guesses.
3. On success, redirect to `/login?reset=1`, which shows "Password changed".

The login page gains a "Forgot password?" link. The calls use the server-side
API key; it never reaches the browser. The code step is its own component so one
attempt's error cannot carry over to the next.

## Out of scope

- Ending sessions that are already signed in. Tokens are stateless and last 24
  hours; a reset does not revoke them. This is the same trade-off tenantcore
  makes with its offline-verified tokens.
- digitalservice's own `/login`, which still answers differently for an unknown
  email. It is rate limited and has been a known, accepted exposure since the
  backend-core refactor. The new reset does not have that flaw.
- Any change to tenantcore. Its mail route allows 10 sends per minute across all
  callers, which bounds this to roughly 10 resets per minute platform-wide.
- Letting an admin trigger a reset for another user. The Staff page already has
  a button that does the equivalent.
