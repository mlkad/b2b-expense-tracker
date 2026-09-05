package shared

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCursorRoundTrip(t *testing.T) {
	original := Cursor{
		SpentAt: time.Date(2026, 3, 14, 9, 26, 53, 0, time.UTC),
		ID:      uuid.MustParse("11111111-2222-3333-4444-555555555555"),
	}

	decoded, err := DecodeCursor(original.Encode())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !decoded.SpentAt.Equal(original.SpentAt) || decoded.ID != original.ID {
		t.Fatalf("round trip lost data: %+v -> %+v", original, decoded)
	}
}

// A token is opaque, so every way of corrupting it must give the client the
// same instruction: start again from the first page.
func TestDecodeCursorRejectsGarbage(t *testing.T) {
	for name, token := range map[string]string{
		"empty":         "",
		"not base64":    "!!!not-base64!!!",
		"no separator":  "bm8tc2VwYXJhdG9y",
		"bad timestamp": Cursor{}.Encode()[:4] + "xxxx",
		"truncated":     Cursor{SpentAt: time.Now(), ID: uuid.New()}.Encode()[:8],
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeCursor(token); !errors.Is(err, ErrValidation) {
				t.Fatalf("got %v, want ErrValidation", err)
			}
		})
	}
}

// The extra row is what makes has_more exact without a COUNT over the tenant's
// whole history.
func TestNewPageUsesTheExtraRow(t *testing.T) {
	type row struct {
		at time.Time
		id uuid.UUID
	}
	cursorOf := func(r row) Cursor { return Cursor{SpentAt: r.at, ID: r.id} }

	make := func(n int) []row {
		out := make([]row, n)
		for i := range out {
			out[i] = row{at: time.Now().Add(-time.Duration(i) * time.Hour), id: uuid.New()}
		}
		return out
	}

	t.Run("a short page has no more", func(t *testing.T) {
		page := NewPage(make(3), 5, cursorOf)
		if page.HasMore || page.NextCursor != "" || len(page.Items) != 3 {
			t.Fatalf("%+v", page)
		}
	})

	t.Run("an exactly full page has no more", func(t *testing.T) {
		// Five rows for a limit of five means there was no sixth, so this is
		// the last page - the caller asked for limit+1 and got only limit.
		page := NewPage(make(5), 5, cursorOf)
		if page.HasMore || len(page.Items) != 5 {
			t.Fatalf("%+v", page)
		}
	})

	t.Run("the extra row is trimmed and becomes the cursor", func(t *testing.T) {
		rows := make(6)
		page := NewPage(rows, 5, cursorOf)
		if !page.HasMore {
			t.Fatal("has_more is false despite an extra row")
		}
		if len(page.Items) != 5 {
			t.Fatalf("page carries %d items, want 5 - the probe row must not be returned", len(page.Items))
		}
		if page.NextCursor != cursorOf(rows[4]).Encode() {
			t.Fatal("the cursor must point at the last returned row, not the probe row: " +
				"pointing at the probe skips it on the next page")
		}
	})
}

func TestClampLimit(t *testing.T) {
	cases := map[int]int{
		0:               DefaultPageSize, // unspecified is not an error
		-1:              DefaultPageSize,
		10:              10,
		MaxPageSize:     MaxPageSize,
		MaxPageSize + 1: MaxPageSize,
		1_000_000:       MaxPageSize,
	}
	for in, want := range cases {
		if got := ClampLimit(in); got != want {
			t.Errorf("ClampLimit(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestValidatorAggregates(t *testing.T) {
	var v Validator
	if err := v.Err(); err != nil {
		t.Fatalf("an empty validator must return nil, got %v", err)
	}

	v.Add("email", "is required")
	v.Addf("amount_minor", "must be at most %d", 100)

	err := v.Err()
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("got %v, want ErrValidation", err)
	}

	var fields FieldErrors
	if !errors.As(err, &fields) || len(fields) != 2 {
		t.Fatalf("expected two field errors, got %v", err)
	}
	if fields[1].Detail != "must be at most 100" {
		t.Errorf("Addf did not format: %q", fields[1].Detail)
	}
}
