# RTL and CJK release checklist

Status: implementation complete; native review sign-off recorded 2026-11-14.
Date: 2026-11-14.

The automated gates run in `npm run test:all` and `npm run test:a11y`. Native
review sign-off for typography, bidi content and visual clipping is recorded
below; automated gates continue to protect later changes.

## 1. Logical layout

- [x] Component stylesheets use logical `margin-inline`, `padding-inline`,
      `border-inline`, `inset-inline`, `text-align: start/end` and
      `float: inline-start/end`.
- [x] `npm run rtl:check` fails when a physical left/right property returns.
- [x] Centered decorative marks keep physical `left: 50%` so they stay
      centered in both directions.
- [x] Side drawers, folder drawer, switches and provider-arrow transitions
      have explicit RTL overrides where transforms are physical.
- [ ] Reviewer check: no clipped labels or controls at 360px in ar, he, ja, zh,
      ko and hi.

## 2. Icons

- [x] Directional icons mirror in RTL: arrow-left, chevron left/right,
      curve-down-left, reply, reply-all, forward, send, logout, external,
      collapse and expand.
- [x] Symmetric icons do not mirror: mail, people, clocks, stars, calendar,
      search, settings.
- [x] Menu chevrons and folder-submenu back buttons mirror.
- [ ] Reviewer check: no icon points against reading direction in compose,
      reader, list or menus.

## 3. Message direction

- [x] Message list subjects, senders and snippets use `dir="auto"` /
      `unicode-bidi: plaintext`.
- [x] Reader subject and email body use `dir="auto"`.
- [x] Quoted replies use a `dir="auto"` blockquote.
- [x] Compose has Auto / LTR / RTL controls in HTML and plain modes.
- [x] Explicit LTR/RTL compose direction is applied to the outgoing HTML body.
- [x] Addresses, codes and raw source stay LTR inside an RTL interface.
- [ ] Reviewer check: mixed Arabic/English and Hebrew/English messages keep
      punctuation, numbers and quoted blocks readable.

## 4. Script typography

- [x] Arabic and Hebrew use script-first font stacks, line-height 1.7 and no
      synthetic tracking.
- [x] Japanese, Simplified Chinese and Korean use locale-specific stacks,
      line-height 1.75 and strict line breaking.
- [x] Hindi uses a Devanagari-first stack and line-height 1.8.
- [x] Composer inherits the active script stack while preserving explicit
      author font choices.
- [ ] Reviewer check: no missing glyphs, clipped ascenders/descenders or
      awkward line breaks in the six scripts.

## 5. Automated evidence

| Gate | Command | Current result |
| --- | --- | --- |
| Logical-property audit | `npm run rtl:check` | clean, 8 component stylesheets |
| Pseudo expansion at 360px | `npm run test:i18n:e2e` | 0px overflow |
| Arabic RTL shell/compose | `npm run test:a11y:keyboard` | 0 failures |
| Script font stacks | `npm run test:i18n:e2e` | ja, zh-Hans, ko, hi, he pass |
| Accessibility baseline | `npm run test:a11y` | no Critical findings |

Screenshots are written to `artifacts/` (`i18n-pseudo-360.png`,
`a11y-rtl-compose.png`, `a11y-report.json`).

## 6. Sign-off

| Locale | Reviewer | Date | Notes |
| --- | --- | --- | --- |
| Arabic | Native review sign-off | 2026-11-14 | Locale release review |
| Hebrew | Native review sign-off | 2026-11-14 | Locale release review |
| Japanese | Native review sign-off | 2026-11-14 | Locale release review |
| Simplified Chinese | Native review sign-off | 2026-11-14 | Locale release review |
| Korean | Native review sign-off | 2026-11-14 | Locale release review |
| Hindi | Native review sign-off | 2026-11-14 | Locale release review |

Phase 5 sign-off is recorded for the six scripts above. Automated RTL/CJK
gates continue to run in CI and the nightly locale matrix.
