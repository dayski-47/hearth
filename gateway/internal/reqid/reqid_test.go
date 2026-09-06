package reqid

import (
	"testing"
)

func TestNewReturnsHex16(t *testing.T) {
	id := New()
	if len(id) != 16 {
		t.Fatalf("New() returned %d chars, want 16", len(id))
	}
	for _, ch := range id {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			t.Fatalf("New() returned non-hex character: %c", ch)
		}
	}
}

func TestNewReturnsDifferentValues(t *testing.T) {
	id1 := New()
	id2 := New()
	if id1 == id2 {
		t.Fatalf("Two calls to New() returned the same value: %q", id1)
	}
}
