# Travel site translations — session handover (2026-10-01)

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
