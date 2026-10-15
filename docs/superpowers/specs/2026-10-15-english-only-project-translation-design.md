# English-Only Project Translation Design

## Goal

Convert the current ARTEX repository from Chinese to English across the frontend, backend, database defaults, agent prompts, skills, scripts, tests, fixtures, mock data, documentation, changelog, and screenshots. The resulting product is English-only; this work does not introduce a localization framework.

## Scope

Translate all human-readable Chinese text in Git-tracked files, including:

- Frontend labels, descriptions, validation messages, notifications, mock content, and accessibility text.
- Backend API errors, logs, notification content, report text, default prompts, and operator-facing messages.
- Agent prompts, planning instructions, tool descriptions, and other model-facing prose.
- Built-in skills and their references, examples, templates, and script messages.
- SQL comments, seed content, known built-in database records, shell and batch scripts.
- Tests, assertions, fixtures, sample data, README files, design notes, operational documentation, and historical changelog text in the current working tree.
- Chinese filenames, with every in-repository reference updated to the English path.
- All existing images under `screenshots/`: 13 application screenshots recreated from the translated UI with deterministic mock data, plus the `wx.png` promotional banner translated separately while preserving its QR code.

Git commit history is not rewritten. User-created database content, captured evidence, reports, and other historical operational data are not machine-translated.

## Compatibility Boundaries

The translation must not change machine contracts. Preserve route paths, URLs, JSON keys, database table and column names, enum values, tool names, protocol tokens, environment variables, command-line flags, code identifiers, placeholders, and executable examples unless a value is purely human-facing.

Use official English brand names where available, including DingTalk, Feishu, WeCom, Telegram, and ScopeSentry. Retain the ARTEX product name.

Markdown structure, code fences, links, templates, interpolation markers, format verbs, SQL placeholders, regular expressions, and JSON examples must remain syntactically valid. Security meaning and authorization constraints in prompts and skills must be preserved rather than paraphrased loosely.

## Translation Method

Use contextual translation by subsystem, not blind global replacement. Maintain one glossary for repeated domain concepts and apply it consistently. Canonical English terms include Asset, Vulnerability, Finding, Interception, Approval, Retest, Task, Session, Exploration, and Notification.

Translate in bounded batches so each area can be reviewed and verified independently:

1. Inventory all tracked Chinese text and establish the glossary.
2. Translate frontend source and mock data.
3. Translate backend, database, agent prompts, and runtime messages.
4. Translate built-in skills, scripts, tests, fixtures, and samples.
5. Translate README, documentation, filenames, and changelog.
6. Add and verify the upgrade migration for known built-in records.
7. Build the translated application and recreate screenshots.
8. Run the complete verification suite and a final repository-wide Han-character scan.

## Existing Database Upgrade

An upgrade must translate only known built-in Chinese records. Each update is restricted by a stable built-in identity and an exact match on the legacy Chinese value. A customized value therefore remains untouched even when it belongs to a built-in record.

The migration must be idempotent: a second startup produces no further data changes. Tests must cover a fresh database, a database containing unchanged legacy defaults, customized built-in records, and a repeated migration run.

The migration must not translate user-authored task names, conversations, findings, evidence, reports, notification history, or any other operational content.

## Image Recreation

Rebuild the 13 tracked application screenshots from the English UI using the repository's deterministic mock data. Keep the current filenames and the purpose of each image so README links remain stable. Use the closest current route if an older screenshot no longer maps exactly to the current UI.

Capture at consistent viewport dimensions and UI state, without browser chrome or transient loading indicators. Review every image for Chinese UI text, Chinese mock content, clipping introduced by longer English strings, and accidental sensitive or environment-specific data.

Translate the Chinese call-to-action in `screenshots/wx.png` separately. Preserve the original QR region pixel-for-pixel and verify that it remains decodable after the banner is rebuilt.

## Error Handling and Risk Controls

Message translation can break tests, format strings, structured prompts, or consumers that incorrectly depend on display text. For every changed message, update exact assertions while preserving the original error type, status code, and control flow. Do not relax tests to accommodate a translation.

Database updates use exact-match guards and stable built-in identities. File renames include reference searches before and after the change. Prompt and skill translations receive a structural review of commands, examples, placeholders, and safety boundaries.

Work is divided into small subsystem commits so a faulty batch can be reverted without discarding unrelated translation work.

## Verification

Verification includes:

- Record the current test baseline before implementation.
- Run the complete Go test suite after backend and database changes and again at the end.
- Run Biome checks and the TypeScript/Next production build for the frontend.
- Exercise the routes used for screenshot generation with mock data.
- Run relevant script and skill self-checks where present, and validate Markdown paths and code blocks.
- Test fresh-install and upgrade database behavior, including preservation of customized values and migration idempotency.
- Inspect every regenerated application screenshot for English-only content and layout regressions, and verify the rebuilt `wx.png` banner visually and with a QR decoder.
- Scan all Git-tracked text files for Han characters; completion requires zero matches.
- Review the final diff for accidental changes to identifiers, contracts, generated dependency data, or user-owned content.

If a verification command already fails before translation, record that baseline and distinguish it from regressions introduced by this work. No completion claim is made without fresh test, build, screenshot, and zero-Han evidence.

## Acceptance Criteria

- No Han characters remain in Git-tracked text files in the current tree.
- Runtime UI, API messages, logs, prompts, skills, scripts, tests, fixtures, documentation, and changelog are English-only.
- Known built-in Chinese database values migrate to English without overwriting customized or user-authored content.
- All 13 application screenshots are recreated from the translated application, and the `wx.png` banner is rebuilt with a preserved, decodable QR code; none of the 14 images contains Chinese text outside the encoded QR payload.
- Machine-facing contracts remain unchanged.
- Go tests, frontend checks, and the production build pass, except for explicitly documented failures proven to predate the translation.
