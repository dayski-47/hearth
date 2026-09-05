package password

import "testing"

func TestHashVerifyRoundTrip(t *testing.T) {
	h, err := Hash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := Verify(h, "correct horse battery staple")
	if err != nil || !ok {
		t.Fatalf("expected verify ok, got ok=%v err=%v", ok, err)
	}
	bad, err := Verify(h, "Tr0ub4dor&3")
	if err != nil || bad {
		t.Fatalf("expected verify fail, got ok=%v err=%v", bad, err)
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	if _, err := Verify("not-a-hash", "x"); err == nil {
		t.Fatal("expected error for malformed hash")
	}
}
