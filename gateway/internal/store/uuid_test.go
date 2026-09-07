package store_test

import (
	"testing"

	"github.com/dayski-47/hearth/gateway/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestUUIDRoundTrip(t *testing.T) {
	const want = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
	u, err := store.ParseUUID(want)
	if err != nil {
		t.Fatalf("ParseUUID: %v", err)
	}
	if !u.Valid {
		t.Fatal("parsed uuid not valid")
	}
	if got := store.UUIDString(u); got != want {
		t.Fatalf("round-trip: got %q want %q", got, want)
	}
}

func TestUUIDParseInvalid(t *testing.T) {
	if _, err := store.ParseUUID("nope"); err == nil {
		t.Fatal("expected error for invalid uuid text")
	}
}

func TestUUIDStringZero(t *testing.T) {
	if got := store.UUIDString(pgtype.UUID{}); got != "" {
		t.Fatalf("zero uuid: got %q want \"\"", got)
	}
}
