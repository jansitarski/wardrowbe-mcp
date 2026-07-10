package wardrowbe

// SyncPayload is the body sent to POST /api/v1/auth/sync in dev mode and the
// projected identity in OIDC mode.
type SyncPayload struct {
	ExternalID  string `json:"external_id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	// IDToken is the raw OIDC id_token. It is sent in OIDC mode so a backend
	// that validates the token against the issuer's JWKS (rather than trusting
	// the projected claims) can authenticate the request. Empty in dev mode.
	IDToken string `json:"id_token,omitempty"`
}

// syncResponse is the backend reply to /auth/sync.
type syncResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// ItemTags is the structured attribute block written back to the backend.
// Every field is optional; only set fields are sent.
type ItemTags struct {
	Colors       []string `json:"colors,omitempty"`
	PrimaryColor *string  `json:"primary_color,omitempty"`
	Pattern      *string  `json:"pattern,omitempty"`
	Material     *string  `json:"material,omitempty"`
	Style        []string `json:"style,omitempty"`
	Season       []string `json:"season,omitempty"`
	Formality    *string  `json:"formality,omitempty"`
	Fit          *string  `json:"fit,omitempty"`
}

// StudioOutfit is the POST /outfits/studio body — a manually composed outfit
// persisted from explicit item ids (the backend rejects unknown fields, so this
// must mirror StudioCreateRequest exactly).
type StudioOutfit struct {
	Items        []string `json:"items"`    // 1-20 item ids
	Occasion     string   `json:"occasion"` // validated against the occasion enum (see helpers.go)
	Name         *string  `json:"name,omitempty"`
	ScheduledFor *string  `json:"scheduled_for,omitempty"` // YYYY-MM-DD
	MarkWorn     bool     `json:"mark_worn"`
	SourceItemID *string  `json:"source_item_id,omitempty"`
	OutfitAttributes
}

// OutfitAttributes are the optional descriptive attributes an outfit author can
// record (jansitarski/wardrowbe#3). Free-form on the backend, but the tool layer
// restricts season/formality to the canonical item-tag vocabulary.
type OutfitAttributes struct {
	Season    *string  `json:"season,omitempty"`
	Formality *string  `json:"formality,omitempty"`
	Palette   []string `json:"palette,omitempty"` // dominant colors, most prominent first (max 10)
	Notes     *string  `json:"notes,omitempty"`
}

// AuthoredSuggestion is the POST /outfits/suggestions body — an outfit
// suggestion authored by the agent, persisted as Outfit(source=external) and
// left pending for the user to accept or reject.
type AuthoredSuggestion struct {
	Items        []string `json:"items"`    // 1-20 item ids; positions follow this order
	Occasion     string   `json:"occasion"` // validated against the occasion enum (see helpers.go)
	Name         *string  `json:"name,omitempty"`
	ScheduledFor *string  `json:"scheduled_for,omitempty"` // YYYY-MM-DD; backend defaults to the user's today
	Reasoning    *string  `json:"reasoning,omitempty"`
	StyleNotes   *string  `json:"style_notes,omitempty"`
	OutfitAttributes
}

// AuthoredPairing is the POST /pairings/item/{id} body — a pairing authored by
// the agent around a source item, persisted as Outfit(source=external). The
// backend prepends the source item when it is absent from Items.
type AuthoredPairing struct {
	Items        []string `json:"items"`                   // partner item ids, 1-20
	ScheduledFor *string  `json:"scheduled_for,omitempty"` // YYYY-MM-DD; backend defaults to the user's today
	Reasoning    *string  `json:"reasoning,omitempty"`
	StyleNotes   *string  `json:"style_notes,omitempty"`
	OutfitAttributes
}

// ItemUpdate is the PATCH /items/{id} body. All fields optional — pointers and
// slices are omitted when nil so a partial update never clears untouched fields.
type ItemUpdate struct {
	Type         *string   `json:"type,omitempty"`
	Subtype      *string   `json:"subtype,omitempty"`
	Name         *string   `json:"name,omitempty"`
	Brand        *string   `json:"brand,omitempty"`
	Notes        *string   `json:"notes,omitempty"`
	Favorite     *bool     `json:"favorite,omitempty"`
	Colors       []string  `json:"colors,omitempty"`
	PrimaryColor *string   `json:"primary_color,omitempty"`
	WashInterval *int      `json:"wash_interval,omitempty"`
	Tags         *ItemTags `json:"tags,omitempty"`
}
