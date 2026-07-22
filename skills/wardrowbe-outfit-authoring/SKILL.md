---
name: wardrowbe-outfit-authoring
description: Author Wardrowbe outfits, suggestions, and item pairings so they render correctly in the web UI (titles, descriptions, Tips, calendar). Use when creating or editing outfits/pairings, scheduling looks on the calendar, recording as-worn outfits, or logging wears and feedback.
---

# Wardrowbe outfit & pairing authoring

The Wardrowbe web UI renders different API fields in different places, and several
tools have one-way semantics (no update, date params ignored). Authoring without
knowing the rendering matrix produces blank cards, wall-of-text titles, and
uncorrectable data. This skill encodes the working conventions.

## The rendering matrix (learn this first)

| Field | Outfits list page | History / calendar page | Pairings page |
|---|---|---|---|
| `name` | **Outfit card title** | *not rendered* | — (field doesn't exist) |
| `reasoning` | Pairing card title (truncated ~60 chars) | **Card title + description block** | **Headline / body text** |
| `style_notes` | *not rendered* | **"Tip:" bar** | **"Tip:" bar** |
| `notes` | *not rendered* | *not rendered* | *not rendered* (edit view only) |

Consequences:

- **Manual outfits render as blank cards in History.** `wardrowbe_create_outfit` has
  no `reasoning`/`style_notes` params, and History shows *only* those two fields — a
  manual outfit on the calendar is images + buttons with no text at all.
- **Pairings have no `name` field.** Their `reasoning` doubles as the card title on
  the Outfits list, so it must stay **≤ ~60 characters** or it truncates.

## Conventions that render well everywhere

**Calendar-visible outfits — always author via `wardrowbe_create_outfit_suggestion`**,
never `wardrowbe_create_outfit`, and fill:

- `name`: short rated title — `"Bomber, Roll-neck & Chinos (4.5/5)"` (Outfits list title)
- `reasoning`: the same rated title, an em-dash, then 1–3 description sentences —
  `"Bomber, Roll-neck & Chinos — 4.5/5 (Tue 21 Jul). <why the combination works>"`
  (History renders this as title + description in one block)
- `style_notes`: concrete wearing/care tips (renders as the "Tip:" bar)
- `notes`: bookkeeping only (cross-references, "wears already logged") — never rendered
- `scheduled_for`: the calendar date

**Pairings** (`wardrowbe_create_item_pairing`):

- `reasoning`: **≤ ~60 chars**, title-shaped, rating inline —
  `"Roll-neck × Structured Jacket (4.5/5) — proven formula"`
- `style_notes`: styling tips *plus* the why-it-works explanation (only other rendered field)
- `notes`: wear history and references
- `scheduled_for`: **defaults to today**, which drops the pairing onto today's History
  page next to real outfits. Date it deliberately — e.g. the date the combination was
  first worn — to keep the current calendar clean.

**Privacy:** outfit/pairing text is visible in the UI (and potentially shared feeds).
Keep occasions neutral (weekday, date, register like "smart-casual") unless the user
explicitly wants the occasion named. Sensitive context belongs in local files, not in
Wardrowbe fields.

## Lifecycle: suggestion → accept

`create_outfit_suggestion` creates `status: pending` (History shows Accept/Reject
buttons). To record a committed or as-worn look, follow with
`wardrowbe_accept_latest_outfit` passing the **explicit `outfit_id`** — don't rely on
"latest". Leave future planned outfits `pending` so the user can accept in the app.

## Mutation model: there is no update

- **No update tool exists for outfits or pairings.** To change any field (including
  `scheduled_for`): `wardrowbe_delete_outfit` + recreate. `delete_outfit` works on
  pairings too (they share the outfit store).
- **Deleting an outfit destroys its attached feedback/rating.** Before deleting an
  outfit that has feedback, copy what matters into the recreation's `notes` or a local
  record. Prefer keeping originals that hold real user feedback; add a copy instead.
- Recreation churn is normal — expect several delete+recreate cycles when iterating on
  how something renders, and re-verify with a UI screenshot when possible.

## Wears, washes, feedback — one-way writes

- **`wardrowbe_log_wear` ignores its `date` parameter** and records *today*. If
  logging a past wear, note the true date in a local/authoritative record.
- **There is no un-log tool.** A wear logged against the wrong item is a permanent
  phantom (inflates `wear_count`/`wears_since_wash`). **Confirm which items were
  actually worn before logging** — when a user reports wearing "the planned outfit,"
  verify substitutions first; users commonly swap one or two pieces.
- `wardrowbe_submit_outfit_feedback` `rating` is an **integer 1–5** — no halves. Keep
  the precise rating (e.g. 4.5/5) in the outfit's `reasoning`/name text.
- Outfit-level feedback does **not** log item wears; call `log_wear` per worn item.

## Misc gotchas

- `item_ids` must be exact full UUIDs — a wrong/guessed UUID fails as an opaque
  backend **403**, not a validation error. Fetch IDs from `list_items`/local mirror;
  never reconstruct from truncated prefixes.
- Large `get_recent_outfits` responses may be saved to a file by the harness — parse
  the saved JSON with a script instead of re-fetching.
- `wardrowbe_get_wardrobe_summary` counts archived items separately from active ones;
  a wardrobe can carry a large hidden archive (old imports/duplicates) worth auditing.
- Notification tools need configured settings: with no notification settings, history
  is empty and `test_notification` has no `setting_id` to target.
