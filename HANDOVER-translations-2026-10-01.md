# Travel site translations — session handover (2026-10-01)

## Update 2026-10-02 (latest)

- Everything described below is built, reviewed and live on E&S, including the base snapshot (open item 1, resolved). Final commits: digitalservice `930b9c8`, travel admin `94385a9`, E&S site `1a9fcc9` + `cccd047`. Spec addendum and plan: `docs/superpowers/specs/2026-10-01-travel-site-translations-design.md`, `docs/superpowers/plans/2026-10-02-translations-base-snapshot.md`.
- Re-sync after editing `src/locales/*.json`: `node scripts/export-translations.mjs --sync --push` (`--dry-run` previews). Needs `TENANT_API_KEY` and `ADMIN_TOKEN` (from `POST /login`).
- A backup of all 37 pages as stored before the sync was taken during the session (scratchpad, this machine only); the sync changed no values.
- Cancellation only blocks writes, so a cancelled tenant's translations stay readable on the site; E&S ends **2026-10-20**.

- **Ports / how to start (2026-10-02):** tenantcore :8092, digitalservice :8080, travel admin :3001, inno dashboard :3011, carwash :8091, carwash-web :3002. The launch-config entry `eandstravelmongolia` serves 404 on every page (its `npm --prefix` form starts Next from the repo root): start the E&S site with `npm run dev -- -p 3000` from `eandstravelmongolia/`. Port 3000 may be another project; check the page title.
- **Pushing is blocked from this machine:** GitHub answers `Permission denied (publickey)` for `~/.ssh/id_ed25519`. Nothing from the 2026-10-01/02 sessions was pushed except what the user pushed themselves (tenantcore `backend-update` was merged as PR #1). Unpushed at last check: digitalservice 13, E&S site 7, travel admin 6, inno admin 6, inno site 1, carwash 1, carwash-web 1.
- **Production tenantcore** is `https://core-backend-5cjs.onrender.com`. Checked 2026-10-02: `/healthz` and `/readyz` 200, public API 200, admin routes 401 without a token, `POST /api/v1/admin/password-reset/request` is registered but answers **503** while `BREVO_API_KEY`/`MAIL_FROM_EMAIL` are not set on Render (`/readyz` is `degraded`; `email` and `expiry_notice` are off). Mail now goes through Brevo's HTTPS API (commit `b65cfd6`); set a Brevo **API key** (`xkeysib-`, not an SMTP key) and a verified sender there, plus `EXPIRY_NOTICE_EMAIL`. Details in `HANDOVER-2026-10-01.md`.
- **Inno dashboard production 404 on password reset:** `POST <host>/admin/password-reset/request` (no `/api/v1`) is 404 on production, which is exactly the dashboard's error. The dashboard's server-side `API_URL` on Vercel must be `https://core-backend-5cjs.onrender.com/api/v1` (and `NEXT_PUBLIC_API_URL` the same); redeploy after changing. Not confirmed: the Vercel settings could not be seen.
- **Cancelled on 2026-10-02 (by the user, in tenantcore):** Nelson Travel and Bayan Bogd (Bayan Bogd is the tenant behind carwash/carwash-web). E&S Discovery Mongolia is still active and its subscription **ends 2026-10-20**: after that its writes (including saving translations) return 402 until renewed. Plan question still open: E&S is on `starter`, the assistant had set `travel-pro`; ask the user, change nothing unasked.
- **Credentials:** the user typed a password in chat for sign-in during the session. It is stored nowhere. Suggest changing it. Never print `.env`.

Editable E&S public-site wording, in English, Mongolian and Korean, from the travel admin (System > Translations). Built and reviewed; nothing pushed. This is a session note; the standing handover is `HANDOVER.md`.

- Spec: `docs/superpowers/specs/2026-10-01-travel-site-translations-design.md`
- Plan: `docs/superpowers/plans/2026-10-01-travel-site-translations.md`
- Ledger with every ruling and deferred minor (this machine only, gitignored): `.superpowers/sdd/2026-10-01-travel-site-translations/progress.md`

## What shipped (all committed, none pushed)

| Repo | Branch | Last commit | What |
|---|---|---|---|
| digitalservice | refactor/backend-core | 1fc664b | `site_pages` per tenant. Admin `GET/GET/PUT /admin/translations[/:page]` (token, role admin, PUT behind the subscription gate); public `GET /translations?lang=`. Validation by shape only. |
| eandstravelmongolia | master | bff2394 | `getTranslation` is async (`src/lib/translations/server.ts`), merges overrides over `locales/*.json` (`merge.mjs`), 2 s timeout, falls back to shipped wording. Client components read a context provider. Importer `scripts/export-translations.mjs`. |
| admin | master | dd9ea5f | Translations page and editor. `key`, `id`, `icon` fields are read-only on stored items. Also the forgot-password page (ddce295). |
| tenantcore | backend-update | docs only | spec, plan, this note |

Also fixed on the way: `greetingName` in digitalservice (8e29af1). A user with an empty `name` made tenantcore answer 500 to the reset-code mail, so no code was ever sent.

## Live state

- E&S has 37 pages imported (349 entries) in `site_pages`. Five entries were skipped because the server cannot store them: `journeys.items`, `map.destinations`, `journal.items`, `airportTransferForm.tiers`, `about.since_year`. Those keep the shipped wording and are not editable.
- Verified live: edit and revert through the admin UI, public read, the live site showing an edit, 413 over 512 KB, 400 on a bad path, 401 without a bearer.
- NOT verified live: lapsed subscription (PUT should be 402, GET 200; from code reading only), the array editors being used, drag/reorder.

## Open decisions and known limits

1. **Unchanged values (RESOLVED 2026-10-02, base snapshot).** Each stored value carries a per-language `base` (the shipped wording it was seeded from). The public read returns a value only if it differs from its `base`, so unedited text follows the site's current locale JSON. Admin reads return `base`; the editor sends it back untouched. Importer: a newly imported page stores `base = values`; `node scripts/export-translations.mjs --sync --push` (add `--dry-run` to preview) migrates and refreshes existing pages: adds new shipped paths, attaches `base` to old data, refreshes unedited values to the new shipped text, keeps edited values (and moves their `base`). Run `--sync --push` after changing `src/locales/*.json`. E&S was migrated on 2026-10-02 (37 pages, 349 entries, second run all unchanged). Commits: digitalservice 930b9c8, admin 94385a9, site 1a9fcc9 + cccd047. Spec addendum and plan: `docs/superpowers/specs/...-design.md` (Addendum), `docs/superpowers/plans/2026-10-02-translations-base-snapshot.md`.
   - An OLD admin build saving a page against the new server drops `base` (values become overrides again, today's behaviour); `--sync --push` re-attaches it.
   - An editor tab left open across a `--sync` holds stale rows; saving it writes the old values and bases back (last write wins).
   - Latent, no current data triggers either: (a) the editor drops blank list items on every save, so a shipped list with a blank item (`["A",""]`) would save as `["A"]`, differ from its `base` and look edited forever; the importer would need to strip blank items the same way. (b) Running `--sync` against a server built BEFORE 930b9c8 reports "synced" but stores no `base` (the old binder ignores the field): deploy the server first and check one page. Idea for later: one shared fixture of `(a, b, equal)` cases read by a Go test of `siteValuesEqual` and a JS test of `valuesEqual`, so the two cannot drift.
2. Structured arrays (ids, coordinates, nested lists) are not editable. Supporting them means relaxing the value rule, the site merge and the editor.
3. Two admins saving the same page: last write wins.
4. Positional arrays (for example `contact.offices`) shift icons if items are removed or reordered.
5. `import 'server-only'` was not added to `server.ts` (the package is not installed). Add it when installs are allowed.
6. Most valuable missing test: a Mongo-backed repository test (tenant isolation, upsert, nested value round trip). Keep it behind the testcontainer target.
7. `npm test` does not exist in the site: adding a script to `package.json` trips the pre-commit `npm audit` (existing high/critical vulnerabilities in next, postcss, sharp). Run `node --test src/lib/translations/`. Admin: `node --test src/lib/translations-edit.test.mjs`.
8. Pre-existing, not from this work: `/sitemap.xml` returns 500 (an article with no date, `src/app/sitemap.ts:25`); `npx eslint src` in admin fails on `(dashboard)/tours/page.tsx:39`.

## Running things

- digitalservice must be started with `PORT=8080` (`.env` has `APP_PORT=8081`). tenantcore must be up: with it down every write returns 500 (fail-closed, correct).
- Importing a tenant: `node scripts/export-translations.mjs --push --dry-run`, then without `--dry-run`. It needs `TENANT_API_KEY` and `ADMIN_TOKEN` (from `POST /login`) in the environment or `.env.local`. It never overwrites a page that already has entries.
- Services this session left running: digitalservice :8080, tenantcore :8092, site :3005, admin :3006. Port 3000 is a different project; :3001 belongs to another chat.

## Credentials

The user typed their admin password in chat during this session so it could be used for sign-in. It is not stored anywhere in this repo, the ledger or any report. Suggest changing it.
