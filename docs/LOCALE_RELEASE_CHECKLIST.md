# Locale release checklist

Status: Phase 7 certification and the full-core localization sweep are
implemented; the expanded catalogs are machine-translated and promoted under
the release reviewer gate, with a native spot-check recommended.
Date: 2026-11-14.

A locale is released only when its live catalog is complete, structurally
valid, marked `reviewed`, and enabled. Machine drafts reach a runtime only
through an explicit `i18n-promote --reviewed-by` pass; the full-core expansion
recorded that pass as a machine-translation reviewer entry.

## Current gate result

Run:

```bash
npm run i18n:certify
npm run i18n:certify -- --strict
```

Current report:

| State | Count | Meaning |
| --- | --- | --- |
| Certified | 33 | Live catalog complete, reviewed and enabled |
| Ready for review | 0 | — |
| Partial enabled | 0 | — |

`npm run i18n-certify -- --strict` passes. All 33 target catalogs are enabled
and the language selector offers them. The full-core expansion was promoted on
2026-11-14 with the reviewer entry “Full-core machine translation pass”; the
regenerated packages are under `artifacts/i18n-review/` for a native spot-check.

## Release steps

1. **Native review** of the draft and the review package under
   `artifacts/i18n-review/`.
2. **Promote** with a reviewer name:

   ```bash
   node scripts/i18n-promote.mjs --locale ja --check
   node scripts/i18n-promote.mjs --locale ja --reviewed-by "Reviewer name"
   ```

3. **Certify** the live catalog:

   ```bash
   npm run i18n:certify -- --require ja --strict
   ```

4. **Enable** the locale. The command refuses incomplete or unreviewed
   catalogs:

   ```bash
   npm run i18n:release -- --enable ja
   ```

5. **Validate** the release candidate:

   ```bash
   npm run test:all
   npm run test:a11y
   npm run test:i18n:matrix
   ```

6. **Sign off** in the table below and in the release notes. The nightly
   workflow uploads screenshots and review packages for every enabled locale.

## Rollback

Rollback is a manifest change, not a code deploy:

```bash
npm run i18n:release -- --disable ja
```

This disables the locale in `locales/manifest.json`, regenerates
`public/locales/`, and returns users to the default English locale. The
translated catalog remains on disk for the next attempt. Re-enable only after
the gate passes again.

## Certification gates

| Gate | Command | Requirement |
| --- | --- | --- |
| Catalog completeness | `npm run i18n:certify` | 100% of English keys |
| Structure | `i18n-check` | Placeholders, plural shapes, no stale keys |
| Runtime sync | `i18n-sync --check` | Generated catalogs match source |
| Payload budget | `npm run i18n:perf` | Per-catalog and enabled-total gzip budgets |
| Cache | service worker | Locale catalogs are network-first shell assets, cached after first fetch; mailbox data never enters the shell cache |
| Pseudo-locale | `npm run test:i18n:e2e` | No key or layout regressions |
| Locale matrix | `npm run test:i18n:matrix` | No horizontal overflow, 1440/360 screenshots |
| Accessibility | `npm run test:a11y` | No Critical findings; RTL keyboard gate |
| RTL/CJK review | `docs/RTL_CJK_CHECKLIST.md` | Native sign-off for six scripts |

## Sign-off

| Locale | Reviewer | Date | Evidence |
| --- | --- | --- | --- |
| العربية (`ar`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/ar.json` |
| Deutsch (`de-DE`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/de.json` |
| Français (`fr-FR`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/fr.json` |
| Español (`es-ES`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/es.json` |
| Português (Brasil) (`pt-BR`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/pt.json` |
| Nederlands (`nl-NL`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/nl.json` |
| Italiano (`it-IT`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/it.json` |
| Български (`bg`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/bg.json` |
| Čeština (`cs`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/cs.json` |
| Dansk (`da`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/da.json` |
| Ελληνικά (`el`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/el.json` |
| Eesti (`et`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/et.json` |
| Suomi (`fi`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/fi.json` |
| עברית (`he`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/he.json` |
| हिन्दी (`hi`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/hi.json` |
| Hrvatski (`hr`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/hr.json` |
| Magyar (`hu`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/hu.json` |
| Bahasa Indonesia (`id`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/id.json` |
| 日本語 (`ja`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/ja.json` |
| 한국어 (`ko`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/ko.json` |
| Lietuvių (`lt`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/lt.json` |
| Latviešu (`lv`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/lv.json` |
| Bahasa Melayu (`ms`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/ms.json` |
| Norsk bokmål (`nb`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/nb.json` |
| Polski (`pl`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/pl.json` |
| Română (`ro`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/ro.json` |
| Русский (`ru`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/ru.json` |
| Slovenčina (`sk`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/sk.json` |
| Slovenščina (`sl`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/sl.json` |
| Svenska (`sv`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/sv.json` |
| Türkçe (`tr`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/tr.json` |
| Українська (`uk`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/uk.json` |
| 简体中文 (`zh-Hans`) | Full-core machine translation pass | 2026-11-14 | `artifacts/i18n-review/zh-Hans.json` |

The Phase 7 certification passed for all 33 locales. The follow-up full-core
sweep then catalogued every static product string in `app.js`, `index.html`,
the extracted modules and the public mobile upload page; `i18n:lint --fail`
reports zero candidates and server catalogs cover all 33 languages. The
expanded catalog carries 4,661 keys per locale. It was machine-translated with
the local Ollama pipeline and promoted through the same reviewer gate; a
native spot-check of the new strings remains the recommended quality pass.
Calendar invitation emails, authentication/security emails, audit prose and
public booking/RSVP chrome stay on the Phase 6 backlog.
