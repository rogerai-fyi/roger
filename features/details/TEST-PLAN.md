# Model details: unit test plan and open questions

Spec set: `features/details/*.feature`. This file lists the table-driven Go tests (stdlib
`testing` + `testify/require`, no mocks) that sit under the godog scenarios, and the
questions the founder should answer at the approval gate.

## Table-driven unit tests

### internal/protocol (taxonomy_details_test.go)

| Test | Rows |
|---|---|
| `TestDetailsText` (the canonicalizer) | empty; whitespace only; ASCII under/at/over cap; 2-byte, 3-byte, 4-byte runes at cap and cap+1; invalid UTF-8; each C0 control 0x00-0x1f; DEL; each C1 0x80-0x9f; ESC sequences (CSI, OSC 8 hyperlink, OSC 52 clipboard); tab/CR/LF/U+2028/U+2029 to one space; runs of spaces collapse; bidi U+202A-202E and U+2066-2069; zero-width U+200B-200D, U+FEFF; combining marks kept; emoji kept |
| `TestValidateDetails` (strict) | each field at cap ok, cap+1 refused with exact message; reserved word whole-word in summary/best_at refused, case-insensitive; reserved word as substring accepted; reserved word in not_for accepted; hyphenated reserved ("target-acquisition") matched |
| `TestNormalizeDetails` (lenient) | over-length cut; reserved word in summary drops summary only; details_source from wire ("rogerai", "verified", "", junk) always becomes "declared" when text survives, "" otherwise; "page" never survives normalize |
| `TestDetailsNotDeclared` | details-only: `Declared()` false, `DetailsDeclared()` true; taxonomy-only: the reverse; both; neither |
| `TestOnAirKeepsInferenceWithDetailsOnly` | stored details-only + inferred category -> on air = inferred taxonomy + details, TaxonomySource "inferred", DetailsSource "declared" |
| `TestRegSigningBytesIgnoreDetails` | offer with and without details -> identical signing bytes; old-node body round trip |
| `TestDetailsJSONOmitEmpty` | no details -> none of the four keys in JSON |

### internal/catalog (notes_test.go)

| Test | Rows |
|---|---|
| `TestNotesValidate` | every defect row in curated_catalog.feature (no id, duplicate, case duplicate, no summary, lengths, control char, page forms: absolute URL, `javascript:`, protocol-relative, space, valid `/x.html`, valid `/x.html#frag`, reserved word, bad JSON) |
| `TestEmbeddedNotesLoad` | the shipped notes.json validates |
| `TestNotesPagesExist` | every page path (fragment stripped) names a file under `web/src` (or the built route list) |
| `TestNoteLookup` | exact canonical id; case-insensitive; zero-width stripped; `-v2`, `:Q4_K_M`, prefix junk do not match |

### cmd/rogerai-broker (details_test.go, details_bdd_test.go)

| Test | Rows |
|---|---|
| `TestStampNotes` | curated id with/without station text; note with/without license/base_model; non-curated id; empty notes; offline offer; category/tags/tuning/policy/price untouched |
| `TestModelsDetailsBlock` | curated; declared majority 2 of 3; tie; no text (no key, no description); class entries; filter path vs cached path identical; byte-identical output with no details anywhere (golden) |
| `TestDetailsVote` | shared with TUI/website semantics: human majority, tie alphabetical, curated proxies only when no human text, offline fallback, fields never mixed across stations |
| `TestProviderMetaDetails` | owner sets/clears details; non-owner 403; strict refusal 400 message; stored JSONB round trip on Mem and Postgres (real ephemeral Postgres) |

### internal/tui (about_test.go, about_bdd_test.go)

| Test | Rows |
|---|---|
| `TestAboutLines` | curated; declared; taxonomy only; nothing; vision; voice tts/stt; adapter with base (no separate base line); non-adapter with base (base line) |
| `TestAboutWrap` | widths 30/40/60/80/120/200; long word hard break; wide runes by display width; never wider than w |
| `TestSameBase` | siblings sorted; cap 4 then "+N more"; off-air excluded; self and other quants of self excluded; canonical base compare |
| `TestFoldDetails` | same rows as `TestDetailsVote` |
| `TestMetaEditorDetails` | field order; char counter; cap stops input; paste sanitized; refused save keeps editor open; voice model |
| `TestAboutNoEscape` | every adversarial row from `TestDetailsText` rendered: no 0x1b / 0x07 from station text |

### internal/webui (details_console_test.go)

| Test | Rows |
|---|---|
| `TestBrowsePassesDetails` | /api/browse carries the five keys when the broker sent them |
| `TestMetaDetails` | POST /api/share/meta with each field, at cap, over cap (400) |
| `TestConsoleDetailsNoMarkup` | console.js details section has no `innerHTML =`, `insertAdjacentHTML`, `outerHTML` (pattern of chat_test.go:190) |

### cmd/rogerai (share_details_test.go, models_info_test.go)

| Test | Rows |
|---|---|
| `TestShareDetailsFlags` | each flag; combined with taxonomy flags; replace-whole rule; empty value; each invalid row; help text in --advanced only |
| `TestModelsInfo` | curated; declared; not on air (exit 1); near-miss suggestions; --json; piped output has no ESC; LAN mode |

### web (Node tests, web/test)

| Test | Rows |
|---|---|
| `bands-details.test.mjs` | fold/vote parity with Go rows; ABOUT lines; no `{@html` / innerHTML in details code; page link only for a same-origin path; markup rows render as text (jsdom) |
| e2e (Playwright) | 360 px no horizontal scroll, light and dark; QSL switch clears previous text |

### Spec hygiene

| Test | Rows |
|---|---|
| `TestDetailsSpecHasNoRawControls` | every file in features/details has no raw C0/C1, bidi or zero-width character (escapes only) |

## Open questions for the founder

1. **Model id squatting.** Anyone can name an offer `wave-nano-fire-decision` today and it
   would show RogerAI's curated text. This spec labels the text as "RogerAI's description of
   this model" and shows the lineage mark beside it. Should curated ids be reserved for
   stations with verified lineage (refused at register otherwise)? That is a moderation and
   registration change, kept out of this spec.
2. **Wording of the curated notes.** The spec uses `demo-1b-*` fixtures. The real notes for
   the Wave Nano variants need text from the lab, drawn from `web/src/data/model-cards.json`
   (purpose, uses, limits), approved by you before they ship. Nothing in this spec invents it.
3. **Sharer page links.** This spec allows a "model page" link only from RogerAI's notes.
   Should sharers be able to link a Hugging Face repo? If yes, an allowlist of hosts is the
   smallest safe version; it is not in this spec.
4. **Reserved words in free text.** The spec reuses the tag reserved-word list for summary and
   best_at (whole words, not_for exempt). Confirm that, or drop it and rely on reports.
