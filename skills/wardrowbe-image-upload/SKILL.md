---
name: wardrowbe-image-upload
description: Add or rebuild Wardrowbe wardrobe items from local image files and label them with Claude's own vision instead of the backend's weak auto-tagger. Use when bulk-importing garment photos, recreating a wardrobe from a folder/zip of images, or fixing items the in-cluster vision model mislabeled (wrong color/type).
---

# Wardrowbe image upload & accurate tagging

This skill covers getting **local** garment photos into Wardrowbe and giving them
**correct** attributes — the reason this MCP server exists (let Claude's vision do the
styling/analysis instead of the small in-cluster model).

The two `wardrowbe_create_item_from_*` tools hand the image to the backend, which then
runs its own auto-tagger. That tagger is weak: it mislabels color and type (e.g. a
beige bomber → "gray", a flight bomber → "puffer", a gingham shirt → "striped"). So the
job is two parts: **(1) get the image in, (2) overwrite the weak tags with your own.**

## Step 1 — Get the image to the backend

The backend needs to fetch the bytes. Pick by where the image lives and how big it is:

| Situation | Tool |
|---|---|
| Image already has a public http(s) URL | `wardrowbe_create_item_from_url` |
| Local file, small (≲300 KB) | `wardrowbe_create_item_from_base64` (inline) |
| Local file, large, OR many files | **upload to a temporary host, then `wardrowbe_create_item_from_url`** (requires user consent — see below) |

Inlining base64 for many/large files is impractical (the model has to emit the whole
string; payloads can exceed message limits — decoded size is capped at 20 MiB). For a
folder of photos, upload to a temporary host and pass URLs.

### Temporary host: litterbox (recommended)

`0x0.st` is currently disabled (AI-spam). Use **litterbox.catbox.moe** — URLs
auto-expire (no manual cleanup, and the backend downloads its own copy immediately, so
the public window is brief). One file:

```bash
curl -s -A "Mozilla/5.0" \
  -F "reqtype=fileupload" -F "time=1h" \
  -F "fileToUpload=@photo.webp" \
  https://litterbox.catbox.moe/resources/internals/api.php
# -> https://litter.catbox.moe/xxxxxx.webp
```

Batch a folder, capturing a `filename|||url` map (run via a **script file**, not an
inline multi-line `-c` string — newlines get flattened and the loop breaks):

```bash
#!/usr/bin/env bash
cd /path/to/images || exit 1
: > /tmp/urls.txt
for f in *.webp *.jpg; do
  [ -e "$f" ] || continue
  url=$(curl -s -A "Mozilla/5.0" -F "reqtype=fileupload" -F "time=1h" \
        -F "fileToUpload=@$f" https://litterbox.catbox.moe/resources/internals/api.php)
  printf '%s|||%s\n' "$f" "$url" >> /tmp/urls.txt
done
```

> ⚠️ Uploading a personal photo to a public host is an outward-facing action: garment
> photos can carry faces, home interiors and metadata, and litterbox is an unaffiliated
> third party. **Always confirm with the user before uploading anything**, prefer a
> short expiry (`time=1h`), and note it self-removes. If the user declines, fall back
> to `wardrowbe_create_item_from_base64` per file.

### Create the items

Call `wardrowbe_create_item_from_url` (or `_from_base64`) once per image. **Always pass
the fields you already know** — `name`, `type`, `primary_color`, `brand`, `subtype` — so
they're stored regardless of what the auto-tagger later guesses. Derive `name` from the
user's filename (their authoritative label); recover `brand` from any prior record.
New items come back `status: "processing"`, `ai_processed: false`.

## Step 2 — Tag accurately with Claude vision

This is the whole point. **Do not trust the backend tags.**

1. **View each garment.** `Read` renders JPG/PNG but **not webp** — convert first
   (ImageMagick is available via `nix-shell -p imagemagick` on NixOS):
   ```bash
   magick "in.webp" -resize 512x512 -quality 80 "/tmp/<item-id>.jpg"
   ```
   Key the temp files by item id so you can map image → item.
2. **Set structured tags** with `wardrowbe_set_item_tags`: `colors`, `primary_color`,
   `material`, `pattern`, `formality`, `season`, `style`, `fit`. This writes the `tags`
   object — and it is the only place `material` can be written.
3. **Set top-level fields** with `wardrowbe_update_item`: `colors`, `primary_color`
   (and `name`/`type`/`brand` if needed). `wardrowbe_set_item_tags` only writes the
   `tags` object — the **top-level** `colors`/`primary_color` are separate user-facing
   fields and need `wardrowbe_update_item` too. (`wardrowbe_update_item` has **no**
   `material` parameter; the top-level material field is AI-derived.)
4. **Write a description** with `wardrowbe_update_item` `notes` (or
   `wardrowbe_set_item_description`). The backend's `ai_description` caption is **not
   settable** via the API — your accurate text lives in `notes`.

Honor the user's filename for the display `name` even when the photo shows a different
shade (e.g. they named it "Olive Pants" but it's forest green) — record the reality in
`notes`/`colors` and flag the mismatch rather than renaming silently.

## ⚠️ The async-clobber trap (most important)

The backend auto-tagger runs **asynchronously, 1–3.5 hours after creation**, flipping
`status: processing → ready` and `ai_processed: false → true`. When it runs it
**overwrites** `colors`, `material`, `pattern` and the `tags` object with its weak
guesses — silently undoing your work if you tagged too early.

**Rule: only apply your tags after `ai_processed == true`.** Tags applied while an item
is still `processing` will be clobbered. Workflow:

- Tag the items that are **already** `ai_processed: true` now (safe — the gate is
  one-shot; editing won't re-trigger it).
- For items still `processing`, **wait** (poll `wardrowbe_get_item` /
  `wardrowbe_list_items` for `ai_processed`), then apply tags. A
  `ScheduleWakeup`/loop to re-check in ~30–60 min works well.
- After tagging, re-verify with `wardrowbe_get_item`: top-level
  `colors`/`primary_color` and the `tags` object should all match your values.

## Gotchas

- **`wardrowbe_archive_item` `reason` ≤ 50 chars** — the tool rejects longer.
- **`wardrowbe_list_items` is paginated** (default `page_size` 25, max 100) — the
  response echoes the effective `page`/`page_size`, with the backend payload under
  `result`; iterate `page` until a page comes back short to cover a large wardrobe.
  Per-page output is still sizable full JSON; parse saved results with
  `python3`/`jq` for the fields you need rather than re-reading whole pages.
- **`wardrowbe_update_item` has no `material` parameter** — write material via
  `wardrowbe_set_item_tags` (`tags.material`). Top-level `colors`/`primary_color`
  **do** update via `wardrowbe_update_item`.
- **No exact-duplicate detection in the backend** — if importing from a zip, hash
  files (`md5sum`) and view look-alikes before creating, to avoid double entries.
- Shell state doesn't persist between Bash calls and `cd` can reset; use absolute
  paths and self-contained scripts.

## Minimal end-to-end recipe

```
1. (confirm with user first) upload local images → litterbox → filename|||url map
2. wardrowbe_create_item_from_url per image, passing name/type/primary_color/brand
3. convert each webp → jpg, Read it, decide true attributes
4. wait until ai_processed == true for the item
5. wardrowbe_set_item_tags  (colors, primary_color, material, pattern, formality, season, style, fit)
6. wardrowbe_update_item    (colors, primary_color, notes)
7. wardrowbe_get_item → verify top-level + tags match; done
```
