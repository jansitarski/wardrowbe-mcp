# Phase 2 external-tagging gaps — implementation & test spec

Status: proposed · Scope: `jansitarski/wardrowbe-mcp` (with two backend-coordinated items called out)

## Context

The Wardrowbe backend gained a **Phase 2 "item tagging"** surface that lets an
external agent own tagging when the internal vision model is disabled
(`AI_INTERNAL_ENABLED=false` / vision off). New backend surface:

- Item fields: `tagging_status` (`pending` | `tagged`), `tagged_by`
  (`auto` | `user` | `agent`), `tagged_at`.
- `GET /items?tagging_status=pending` — the agent's work queue.
- `POST /items` accepts `auto_tag` (form bool). With vision off, or
  `auto_tag=false`, the item is created `status=ready` + `tagging_status=pending`
  (no auto-tag job) and waits for an external tagger.
- `PATCH /items/{id}` filling a **pending** item's tags flips it to `tagged` and
  records `tagged_by` **server-side** from a signed JWT `actor` claim (absent ⇒
  `user`). The transition is one-way: editing an already-`tagged` item never
  rewrites `tagged_by`/`tagged_at`.
- `POST /items/{id}/retag` — reset an item back to `pending`, clearing origin.

This MCP server is the agent's hands on that surface. Live testing against the
homelab instance (2026-06-19, vision **off**) confirmed the backend behaves
correctly end-to-end, and surfaced four gaps where the MCP does not yet expose or
correctly drive the Phase 2 surface. Each gap below is independently shippable.

### What live testing showed (evidence)

- Creating an item via `wardrowbe_create_item_from_url` returned
  `status=ready`, `tagging_status=pending`, `tagged_by=null` — correct deferral,
  but the MCP can't then **list** what's pending (Gap 1) or force-defer when
  vision is on (Gap 3).
- `wardrowbe_set_item_tags` on the pending item flipped it to `tagged` with
  `tagged_at` set — but `tagged_by` came back `"user"`, not `"agent"` (Gap 4),
  and the attributes landed **only** in the `tags` JSONB; the top-level columns
  (`primary_color`, `colors`, `pattern`, …) stayed empty (Gap 2).
- A second `set_item_tags` correctly did **not** rewrite `tagged_by`/`tagged_at`
  (backend one-way gate works) — but it **replaced** the whole `tags` block
  rather than merging (documentation gap, see Gap 3).

---

## Gap 1 — Expose the pending work queue (MCP-only) — PRIORITY 1

**Problem.** `wardrowbe_list_items` has no `tagging_status` filter, so the agent
cannot ask "what still needs tagging?" — the core entry point of the workflow.
The backend already supports `GET /items?tagging_status=pending`.

**Change** — `internal/mcpserver/tools_items.go`:

1. In `registerItemTools()`, add a param to the `wardrowbe_list_items` tool:
   ```go
   mcp.WithString("tagging_status",
       mcp.Description("Filter by tagging state: pending (needs tags) or tagged."),
       mcp.Enum("pending", "tagged")),
   ```
2. In `handleListItems`, forward it (mirror the existing `category` handling):
   ```go
   if ts := req.GetString("tagging_status", ""); ts != "" {
       q.Set("tagging_status", ts)
   }
   ```
3. Optional convenience tool, mirroring `wardrowbe_get_items_to_wash`:
   `wardrowbe_list_untagged_items` → `handleListItems`-equivalent that hard-sets
   `tagging_status=pending` and a `limit`. Keeps the agent's common "show me the
   queue" call to one tool. Skip if you prefer the single filtered tool.

**Tests** (`tools_items_test.go`, `httptest` backend like
`internal/wardrowbe/client_test.go`):
- `list_items` with `tagging_status=pending` ⇒ backend receives
  `?tagging_status=pending` (assert on `r.URL.Query()`).
- omitted ⇒ no `tagging_status` key in the query.
- invalid value is rejected by the enum at the tool layer (no backend call).

**Acceptance.** Agent can retrieve only pending items and only tagged items.

---

## Gap 2 — Populate attribute columns, not just the `tags` JSONB — PARTIAL (MCP) + backend follow-up — PRIORITY 2

**Problem.** `wardrowbe_set_item_tags` sends every attribute nested under `tags`
(`wardrowbe.ItemUpdate{Tags: &tags}` in `tools_writeback.go:130`). The backend
writes those to the **`tags` JSONB only**. The internal vision worker writes
**both** the JSONB and the first-class columns (`primary_color`, `colors`,
`pattern`, `material`, `style`, `formality`, `season`). So agent-tagged items
have empty columns and are invisible to column-based filters/scoring/pairing.

**Root cause is the backend's dual storage** (columns + JSONB with no sync).
Split the fix:

**MCP-actionable now** (covers the two attributes the backend `ItemUpdate` exposes
as top-level columns): in `handleSetItemTags`, also set the top-level fields so
the columns populate, in addition to the JSONB —
`internal/mcpserver/tools_writeback.go`:
```go
patch := wardrowbe.ItemUpdate{Tags: &tags}
if len(tags.Colors) > 0 {
    patch.Colors = tags.Colors        // populates the colors[] column
}
if tags.PrimaryColor != nil {
    patch.PrimaryColor = tags.PrimaryColor // populates the primary_color column
}
raw, err := s.client.UpdateItem(ctx, itemID, patch)
```
(`ItemUpdate` already has `Colors`/`PrimaryColor` — `types.go:55-56`. The backend
applies top-level columns and the `tags` JSONB in the same PATCH.)

**Residual — needs a BACKEND change (track separately, do NOT silently drop):**
`pattern`, `material`, `style`, `formality`, `season` have **no** top-level
column path in the backend `ItemUpdate`, so the MCP cannot populate those columns.
The clean fix is on the backend: have the `tags`-write path also project these
into their columns (single source of truth), so any client that PATCHes `tags`
gets column + JSONB parity — matching what the worker does. Until then, those five
attributes live only in the JSONB for agent-tagged items. File this against the
backend repo (`wardrowbe`, the Phase 2/3 series).

**Tests:**
- `set_item_tags` with `colors` + `primary_color` ⇒ backend PATCH body contains
  top-level `colors`/`primary_color` AND `tags.colors`/`tags.primary_color`
  (decode the captured request body).
- `set_item_tags` with only `pattern` ⇒ body carries `tags.pattern` (documents the
  JSONB-only residual; assert it does NOT silently claim a column write).

**Acceptance.** Agent-set `colors`/`primary_color` are queryable by the
column-based filters; the JSONB-only residual is documented and tracked upstream.

---

## Gap 3 — Create-time defer control & retag, plus replace-semantics docs (MCP-only) — PRIORITY 2

Three small items:

**3a. `auto_tag` passthrough on create.** The backend `POST /items` accepts an
`auto_tag` form bool; when `false` the item is left `pending` even with vision on.
The create tools don't expose it. In `internal/mcpserver/tools_create.go`, add a
boolean `auto_tag` param to both create tools and forward it via `itemFields`:
```go
// in itemFields(req)
if v, present, errRes := argBool(req, "auto_tag"); errRes != nil {
    return nil, errRes
} else if present {
    fields["auto_tag"] = boolStr(v)
}
```
`CreateItemFromImage` already sends `fields` as multipart form fields, so no
client change is needed. (Only meaningful when backend vision is enabled; with
vision off every create already defers. Note that in the tool description.)

**3b. `wardrowbe_retag_item` tool.** Backend `POST /items/{id}/retag` resets an
item to `pending` and clears origin — the inverse of tagging, and what lets an
agent re-queue an item it (or the user) wants re-done. Add to
`internal/mcpserver/tools_items.go` using the existing `itemAction` helper:
```go
s.add(mcp.NewTool("wardrowbe_retag_item",
    mcp.WithDescription("Reset an item to the pending tagging queue, clearing its "+
        "current tags' origin. Does not itself run internal AI."),
    mcp.WithDestructiveHintAnnotation(false),
    mcp.WithIdempotentHintAnnotation(true),
    mcp.WithString("item_id", mcp.Required(), mcp.Description("Item id.")),
), s.handleRetagItem)

func (s *Server) handleRetagItem(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
    itemID, errRes := requireID(req, "item_id")
    if errRes != nil {
        return errRes, nil
    }
    return s.itemAction(ctx, itemID, "retag", nil) // POST /items/{id}/retag
}
```

**3c. Document replace-semantics.** `set_item_tags` replaces the entire `tags`
block (a partial call drops untouched attributes — confirmed live). Update the
tool description in `tools_writeback.go` to say so explicitly, e.g. append:
"This replaces the item's full attribute set — include every attribute you want
to keep, not just the ones you're changing." (Behavioral change to merge is
optional and larger; documenting the current contract is the minimal fix.)

**Tests:**
- create with `auto_tag=false` ⇒ multipart form includes `auto_tag=false`.
- `retag_item` ⇒ backend receives `POST /api/v1/items/{id}/retag`; unknown id ⇒
  the backend 404 surfaces as a tool error (reuse the `itemAction` error path).

**Acceptance.** Agent can defer at create time, re-queue an item, and the
replace-semantics are no longer a surprise.

---

## Gap 4 — Agent attribution via signed `actor` claim (MCP + BACKEND) — PRIORITY 3

**Problem.** Every MCP write records `tagged_by="user"`, because the backend mints
the access token in `POST /auth/sync` via `create_access_token(external_id)` with
no `actor` claim, and the MCP never requests one. The backend correctly defaults
an absent claim to `user`; the `agent` value is simply unreachable today.

This is a **coordinated** change — ship the MCP side and the backend side
together, else the MCP field is inert:

**MCP side** (`internal/wardrowbe/types.go`, `auth.go`, `internal/config`):
1. Add to `SyncPayload`:
   ```go
   Actor string `json:"actor,omitempty"` // "agent" marks this client as a non-human writer
   ```
2. Set it from a config switch (default empty ⇒ unchanged behavior). Add an
   `Actor`/`MCP_ACTOR` flag in `internal/config/config.go` (mirror an existing
   string flag like `external-id`), validate it ∈ {"", "agent"}, and have
   `DevTokenProvider`/`OIDCTokenProvider.SyncPayload` copy it into the payload.
   Most deployments fronting an LLM should set `MCP_ACTOR=agent`.

**Backend side** (track in the `wardrowbe` repo — REQUIRED for the claim to take
effect): `POST /auth/sync` must read the requested `actor`, and mint
`create_access_token(external_id, actor="agent")` **only for trusted agent
clients** — do not blindly trust the body. Options: gate on an agent API key /
client credential the deployment already holds, or a dedicated agent-sync path.
The signed claim is then honored by the backend's `get_current_actor`, and writes
record `tagged_by="agent"`.

**Tests:**
- MCP: with `MCP_ACTOR=agent`, the `/auth/sync` request body carries
  `"actor":"agent"` (extend `TestSyncForwardsIDTokenInBody` in
  `internal/wardrowbe/client_test.go`); default ⇒ no `actor` key.
- Backend (its repo): trusted agent sync ⇒ token has `actor=agent` ⇒ a tag
  write-back yields `tagged_by=agent`; untrusted body `actor=agent` ⇒ ignored ⇒
  `tagged_by=user`.

**Acceptance.** A deployment configured as an agent records `tagged_by=agent`;
the security property "origin is server-derived, never trusted from the body"
still holds.

---

## Ordering & independence

| # | Gap | Owner | Depends on | Priority |
|---|-----|-------|-----------|----------|
| 1 | `tagging_status` list filter | MCP | — | P1 |
| 3 | `auto_tag` + `retag` + docs | MCP | — | P2 |
| 2 | column population (colors/primary_color) | MCP | — | P2 |
| 2′| pattern/material/style/formality/season columns | backend | backend tags→columns sync | P2 (tracked upstream) |
| 4 | `actor=agent` attribution | MCP + backend | backend `/auth/sync` minting | P3 |

Gaps 1, 2, 3 are pure MCP and can land in any order / one PR. Gaps 2′ and 4 need
backend changes — open companion issues on `wardrowbe` and land each pair
together.

## How to test (general)

- **Unit:** follow the existing pattern — `httptest.NewServer` returning canned
  JSON for `/api/v1/auth/sync` plus the target path, `wardrowbe.NewClient(srv.URL,
  wardrowbe.DevTokenProvider{ExternalID: "t"}, …)`, then call the tool handler and
  assert on the captured request (method, path, `r.URL.Query()`, decoded body).
  See `internal/wardrowbe/client_test.go` and `internal/mcpserver/*_test.go`
  (`newTestClient` / `testServer`).
- **Make targets:** `make test` (and `make lint`) before each PR.
- **Live smoke (homelab, vision off):** create a throwaway item → confirm it lists
  under `tagging_status=pending` (Gap 1) → `set_item_tags` with colors/primary_color
  → `get_item` shows populated **columns** + JSONB and `tagged_by` per config
  (Gaps 2/4) → `retag_item` → confirm back to `pending` with origin cleared
  (Gap 3) → `archive_item` to clean up. (This mirrors the 2026-06-19 validation;
  always archive the throwaway afterwards.)

## Out of scope

- Backend data-model unification (Gap 2′) and `/auth/sync` agent minting (Gap 4
  backend half) — companion `wardrowbe` issues, referenced above.
- Changing `set_item_tags` from replace to merge semantics — documenting the
  current contract (Gap 3c) is the minimal fix; a merge mode can follow if needed.
