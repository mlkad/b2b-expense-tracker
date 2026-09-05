package shared

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Pagination limits. The maximum is not a round number chosen for looks: it is
// the largest page the JSON encoder fills without the response outgrowing the
// 1 MiB proxy buffer, measured against the widest expense row.
const (
	DefaultPageSize = 25
	MaxPageSize     = 200
)

// Cursor is a keyset pagination position: the last row of the previous page.
//
// Keyset, not OFFSET. `OFFSET 50000` makes PostgreSQL walk and discard fifty
// thousand rows on every request, so the last page of a large tenant's history
// costs the most - and rows inserted while a user pages shift the window, so
// they see duplicates or miss rows entirely. A cursor over (spent_at, id)
// resumes exactly where the previous page stopped and costs the same at page
// one and page one thousand, because it is an index seek into
// expenses_tenant_spent_at_idx.
//
// The id tiebreak is required, not cosmetic: many expenses share a spent_at,
// and a cursor on the date alone either repeats or skips the rows that tie.
type Cursor struct {
	SpentAt time.Time
	ID      uuid.UUID
}

// Encode renders a cursor as an opaque token.
//
// It is not signed. A forged cursor can only name a position inside the
// caller's own tenant - RLS bounds what the resulting query can return no
// matter what the token says - so a signature would protect nothing that is
// not already protected, at the cost of making cursors non-portable across a
// key rotation. What it must be is unguessable in form, so that clients treat
// it as opaque instead of building "page 3" URLs by hand.
func (c Cursor) Encode() string {
	raw := fmt.Sprintf("%s|%s", c.SpentAt.UTC().Format(time.RFC3339), c.ID)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor parses a token produced by Encode. Every failure returns the
// same field error: the client's only correct response to any of them is to
// restart from the first page.
func DecodeCursor(token string) (Cursor, error) {
	bad := FieldError{Field: "cursor", Detail: "is not a valid pagination cursor"}

	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return Cursor{}, bad
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return Cursor{}, bad
	}
	ts, err := time.Parse(time.RFC3339, parts[0])
	if err != nil {
		return Cursor{}, bad
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return Cursor{}, bad
	}
	return Cursor{SpentAt: ts, ID: id}, nil
}

// Page is one slice of a listing plus the token for the next one.
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more"`
}

// NewPage builds a response from limit+1 rows.
//
// Callers query one row beyond the page size. That extra row is what makes
// has_more exact without a second COUNT query - and a COUNT over a filtered
// tenant history is the query that turns a fast list endpoint into a slow one.
func NewPage[T any](rows []T, limit int, cursorOf func(T) Cursor) Page[T] {
	if limit <= 0 {
		limit = DefaultPageSize
	}
	if len(rows) <= limit {
		return Page[T]{Items: rows, HasMore: false}
	}
	items := rows[:limit]
	return Page[T]{
		Items:      items,
		HasMore:    true,
		NextCursor: cursorOf(items[len(items)-1]).Encode(),
	}
}

// ClampLimit normalises a client-supplied page size. Zero means "unspecified",
// which is the default rather than an error, because a client that omits the
// parameter is not making a mistake.
func ClampLimit(n int) int {
	switch {
	case n <= 0:
		return DefaultPageSize
	case n > MaxPageSize:
		return MaxPageSize
	default:
		return n
	}
}
