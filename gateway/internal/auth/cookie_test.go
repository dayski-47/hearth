package auth

import "testing"

func TestCookieRoundTrip(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	v := signValue(secret, "abc123")
	id, ok := parseValue(secret, v)
	if !ok || id != "abc123" {
		t.Fatalf("round trip failed: id=%q ok=%v", id, ok)
	}
}

func TestCookieRejectsTamperedID(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	v := signValue(secret, "abc123")
	if _, ok := parseValue(secret, "xyz"+v[3:]); ok {
		t.Fatal("tampered id accepted")
	}
}

func TestCookieRejectsWrongSecret(t *testing.T) {
	v := signValue([]byte("0123456789abcdef0123456789abcdef"), "abc123")
	if _, ok := parseValue([]byte("ffffffffffffffffffffffffffffffff"), v); ok {
		t.Fatal("wrong secret accepted")
	}
}

func TestCookieRejectsMalformed(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	for _, v := range []string{"", "nodot", ".onlysig", "id."} {
		if _, ok := parseValue(secret, v); ok {
			t.Fatalf("malformed value %q accepted", v)
		}
	}
}
