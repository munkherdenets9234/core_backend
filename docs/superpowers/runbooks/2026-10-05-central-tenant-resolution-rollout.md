# Runbook: central tenant resolution rollout

Moves digitalservice from resolving `X-API-Key` against its own `tenants`
collection to asking tenantcore (`TENANT_RESOLVER=tenantcore`). Design:
`docs/superpowers/specs/2026-10-05-central-tenant-resolution-design.md`.

Status as of 2026-10-05: code done and committed in both repos; rollout NOT
started; `TENANT_RESOLVER` still defaults to `local`.

This file contains no secrets and must never gain any. Placeholders such as
`$MONGO_URI_SOURCE` are read from the operator's own shell environment.

## Never

- Never rotate a live tenant's key to test something.
- Never paste keys, tokens, hashes or connection strings into chat, tickets,
  commits or logs.
- Never run the migration without `-dry-run` first.
- Never run the migration against the live tenantcore database without
  `-only-missing`, and prefer `-collections tenants` (step 2).
- No assistant or automation performs any step marked **REQUIRES THE OWNER'S
  EXPLICIT APPROVAL AT THE TIME**. The owner runs it, or approves that single
  action in the moment. Earlier approval does not carry over.

## Rollback caveat (read before step 4)

After a tenant's key is re-issued in tenantcore, setting `TENANT_RESOLVER`
back to `local` does NOT restore that tenant. digitalservice's local `tenants`
collection still holds the OLD key hash, and no tool copies tenantcore's hash
back: the migration copies digitalservice to tenantcore only. Recovery for
that tenant is a digitalservice-side key rotate (digitalservice's own rotate
route), then updating that tenant's env files and redeploying. Tenants that
were not re-issued roll back cleanly.

## Step 1. Preconditions

- tenantcore production is reachable and its mail sender works.
- digitalservice production has `TENANTCORE_URL` and `TENANTCORE_SERVICE_KEY`
  set, and the service client behind that key is active in tenantcore.
- The new digitalservice and tenantcore code is deployed, with
  `TENANT_RESOLVER` unset (so `local`).

Read-only checks (placeholders, no credentials):

```
curl -fsS "$TENANTCORE_URL/healthz"
curl -fsS "$TENANTCORE_URL/readyz"
curl -fsS "$DIGITALSERVICE_URL/healthz"
curl -fsS "$DIGITALSERVICE_URL/readyz"
```

Success: all four answer 200. digitalservice `/readyz` lists `entitlement`
with `enabled: true`, has no `tenant_resolver_tenantcore` feature (still
local) and no `tenant_resolver` block. If tenantcore is on a cold-start host,
repeat the first call until it answers.

## Step 2. Dry run (read-only on both databases, writes nothing)

From the tenantcore repo, URIs from your own environment:

```
go run ./cmd/migrate-from-digitalservice \
  -from "$MONGO_URI_SOURCE" -from-db "$MONGO_DB_SOURCE" \
  -to   "$MONGO_URI_TARGET" -to-db   "$MONGO_DB_TARGET" \
  -only-missing -dry-run
```

`-collections` takes a comma list of `tenants,platform_users,plans,subscriptions`
(default all four; an unknown name stops the tool at startup). The default
recommendation for the real run (step 3) is `-collections tenants`: with all
four, `-only-missing` also inserts every platform user, plan and subscription
that is absent from tenantcore, which resurrects users deleted there on purpose
(with their old password hashes) and plans or subscriptions removed or changed
since. Dry-run the same collection set you intend to run for real. Run the dry
run once with the default (all four) as well if you want to see what that
would add.

(`-from`/`-to` default to `DIGITALSERVICE_MONGO_URI` / `MONGO_URI`.) The tool
logs the source and destination as scheme, host and database only; user info
and options are removed. Still do not paste its output anywhere unneeded.

Read:
- per collection `would:` summary lines (inserted and skipped counts);
- one `would insert <collection> id=<_id>` line per document that would be
  inserted, with a safe label: tenants show name and slug, plans the slug,
  subscriptions the tenant id, platform_users the `_id` only. Use them to spot
  an `_id` difference: a tenant that already exists in tenantcore under
  another `_id` shows up as would-insert with the same slug;
- the tenants drift report, for tenants present in BOTH databases: for each
  one that differs, id, name and every differing facet: `key` (the
  `api_key_last4` field of each side), `domain` and `status` (both values).
  Equal tenants and tenants on only one side are not listed. No hash is ever
  printed. Key drift marks the tenants needing re-issue (step 4).

Success: the run ends with `dry run complete`, and the would-insert lines
match what you expect (tenants absent from tenantcore, today Inno Nomads and
Nomad Trails, are the ones that would be inserted).

STOP, resolve by hand with the owner, and rerun the dry run before going on, if:
- any would-insert tenant has a slug that already exists in tenantcore under
  another `_id` (the two databases disagree about who the tenant is; its data
  would be orphaned);
- any would-insert row appears in platform_users, plans or subscriptions and
  the owner has not explicitly said they want it (otherwise run step 3 with
  `-collections tenants` only);
- any domain or status drift is listed that has not been resolved first (decide
  which side is right and fix it in tenantcore's console; the migration does
  not overwrite existing tenants under `-only-missing`).

## Step 3. Insert missing tenants (writes to the shared database)

**REQUIRES THE OWNER'S EXPLICIT APPROVAL AT THE TIME. The assistant or
automation must not run this.**

The same command without `-dry-run`, keeping `-only-missing`:

```
go run ./cmd/migrate-from-digitalservice \
  -from "$MONGO_URI_SOURCE" -from-db "$MONGO_DB_SOURCE" \
  -to   "$MONGO_URI_TARGET" -to-db   "$MONGO_DB_TARGET" \
  -only-missing -collections tenants
```

(Add other collections only if the owner explicitly asked for them after
reading the step 2 would-insert lines.) Inserts use `$setOnInsert`: documents already in tenantcore are never
touched. Tenants newly inserted keep their existing keys; no re-issue, no env
change.

Success: log ends with `done.`; the inserted counts equal the dry run's
`would:` counts; skipped counts cover the rest. Open the tenants list in the
platform console and confirm the inserted tenants appear.
Rerunning is safe (insert-only).

## Step 4. Re-issue mismatched tenants, one at a time

Applies to the tenants with key drift in the step 2 report (E&S expected).
Their old key stops working in tenantcore-resolved mode at the moment of
rotation, and E&S's subscription ends 2026-10-20: do not do this in the last
days of that period, and finish the flip well before it. Re-read the rollback
caveat above first. Do step 4b (TRUSTED_PROXIES) before the flip too.

**REQUIRES THE OWNER'S EXPLICIT APPROVAL AT THE TIME, per tenant. The
assistant or automation must not rotate a key or edit production env files.**

Order for each tenant, finishing one before starting the next:

1. Rotate the key in tenantcore (console, tenant Details page). The new key
   is shown once; copy it once, straight into the next item, and nowhere else.
2. Set `TENANT_API_KEY` in that tenant's site env file and its admin env file
   (hosting dashboard, not a file in git).
3. Redeploy both.
4. FLIP (step 5). The flip is one switch for all tenants; with several
   re-issued tenants, do items 1 to 3 for each of them, then flip once.
5. THEN verify: a public read through the tenant's site (for example the
   translations read) returns 200, and admin sign-in works.

Do not try to verify before the flip. In `local` mode digitalservice still
holds the OLD hash, so the new key is refused there; a refusal before the flip
is expected and proves nothing. The mismatch line for the tenant also cannot
be used as a success criterion: digitalservice's local hash stays old forever
and the drift report will keep listing the key difference.

Unavoidable refusal window: from the moment the key is rotated until its env
is updated, redeployed AND the flip is done, that tenant's storefront is
refused. In `local` mode digitalservice does not know the new key; after the
flip it does not know the old key. Keep that window to minutes: have the env
change ready and do rotation, env update, redeploy and the flip in one sitting.
Tenants whose keys were not re-issued are unaffected.

Success per tenant (after the flip): public read 200 and admin sign-in works.

## Step 4b. Before the flip: TRUSTED_PROXIES

digitalservice has a per-IP limiter in front of key resolution. It needs to
know which peers are your reverse proxy, otherwise it trusts a client-supplied
`X-Forwarded-For` header and the limiter can be bypassed with one header.

Set `TRUSTED_PROXIES` on production digitalservice to a comma-separated list
of the IPs or CIDR ranges of the reverse proxy in front of digitalservice.
Read the ranges from the hosting provider's documented proxy ranges; do not
guess them. Record the value you set (it is not a secret) so the owner can
check it. Redeploy.

Success before the flip: digitalservice restarts cleanly with `TRUSTED_PROXIES`
set. An invalid entry stops startup with an error naming `TRUSTED_PROXIES` and
the bad entry; fix it and redeploy.

Do not use `/readyz` to check this before the flip. The `tenant_resolver`
block and its `proxy_safe: false` detail exist only while
`TENANT_RESOLVER=tenantcore`; in `local` mode `/readyz` never shows them, so
it cannot confirm or refute the setting. The `/readyz` check is in step 5,
after the flip.

## Step 5. Flip

**REQUIRES THE OWNER'S EXPLICIT APPROVAL AT THE TIME (changes production
env). The assistant or automation must not perform it.**

On production digitalservice set `TENANT_RESOLVER=tenantcore` and restart.
Optional tuning, defaults shown: `TENANT_RESOLVE_RATE_PER_MINUTE=600`,
`TENANT_RESOLVE_BURST=120`. Startup fails closed: it refuses to boot if
`TENANTCORE_URL` or `TENANTCORE_SERVICE_KEY` is missing, or either limiter
number is below 1. A refusal to start is the safe outcome; fix the env, do
not revert blindly.

Checks:

```
curl -fsS "$DIGITALSERVICE_URL/readyz"
```

Success: 200; `features` contains `tenant_resolver_tenantcore` with
`enabled: true`; `degraded` is false. The `tenant_resolver` block depends on
state:
- `TRUSTED_PROXIES` set and tenantcore reachable: no `tenant_resolver` block.
- `TRUSTED_PROXIES` unset: a `tenant_resolver` block with `proxy_safe: false`
  and a detail saying the per-IP limiter trusts a client-supplied
  `X-Forwarded-For`. Status stays 200 and `degraded` is not changed by this;
  set `TRUSTED_PROXIES` (step 4b) and redeploy.
- tenantcore unreachable: `degraded` is true and the `tenant_resolver` block
  has `stale: true`, a detail naming tenantcore as unreachable (resolving from
  cache, unseen keys answer 503) and a `since` time. If `TRUSTED_PROXIES` is
  also unset the detail carries both notes and `proxy_safe: false`.

Then, per tenant: one public
storefront request returns 200 with that tenant's key (set in your shell, not
pasted anywhere), and one admin sign-in succeeds. Watch error rates (401, 403,
429, 503) for a stable period, for example 24 hours, before calling it done.
Expected failures to look for: 401 for a tenant whose key was re-issued but
whose env was not updated; 503 only if tenantcore is down and the key was
never cached; 429 means the per-IP limiter is refusing a client (check
`TRUSTED_PROXIES` and the two limiter numbers before raising them).

## Step 6. Rollback

Set `TENANT_RESOLVER=local` (or unset it) on digitalservice and redeploy.

Simplest path for a re-issued tenant: put its OLD key back in that tenant's
site and admin env files and redeploy. In `local` mode digitalservice still
holds the old hash, so the old key works again with no digitalservice-side
rotate. Note that this also re-enables a possibly leaked old key; if the
re-issue was done because the key leaked, use the rotate path below instead.

Works as-is for every tenant that was NOT re-issued. For a re-issued tenant
it does not, unless you restore the old key as above (see the rollback
caveat): its local hash is the old one. Otherwise recover it
with a digitalservice-side key rotate via digitalservice's own rotate route
(owner approval, same care as step 4), then update that tenant's env files
and redeploy. Success: that tenant's public read returns 200 in `local` mode.

## Step 7. Known limits

- Fresh window 60 s: a resolved identity is reused without asking tenantcore.
- Stale grace 24 h: while tenantcore is unreachable, known keys keep being
  served from cache for up to 24 hours past the 60 s. A tenant suspended or
  re-keyed during an outage keeps working until the entry ages out or tenantcore
  returns. Past 24 h with nothing cached the answer is 503, never 401.
- Negative cache 30 s: a refused key is remembered; a freshly issued or
  re-issued key can be refused for up to 30 s after tenantcore accepts it.
- After a transport failure the client stops contacting tenantcore for 10 s
  and answers from cache; the cache holds at most 10000 entries.
- tenantcore on Render can cold start: the first call after idle can be slow
  (the client times out at 3 s and falls back to cache or 503). Warm it with
  the step 1 `curl` before the flip.
- Per-IP limiter in front of resolution only in `tenantcore` mode. Without
  `TRUSTED_PROXIES` it trusts a client-supplied `X-Forwarded-For` (step 4b).
- Phase 3, removing digitalservice's duplicate management routes and the
  local lookup, is a separate plan.

## What changes for operators after the flip

- Key rotation, suspension, domain and status changes happen only in
  tenantcore (console Details page). They reach digitalservice within about 60
  seconds; a rotated key is refused after that.
- digitalservice still has its own tenant create / rotate / status / domain
  routes until phase 3. They no longer affect which key is accepted, and must
  not be used: they silently diverge from the truth.
- During a tenantcore outage the 24 h stale window applies (step 7); a
  suspension is not enforced on cached tenants until tenantcore is back.
- After issuing or re-issuing a key, allow up to 30 s for the negative cache.
- Avoid key changes for E&S near the end of its subscription (2026-10-20).

## Follow-ups

- A tenantcore to digitalservice hash back-copy tool, so rollback after a
  re-issue does not need a digitalservice-side rotate.
- Phase 3 plan: remove digitalservice's duplicate tenant management routes
  and the local lookup.
