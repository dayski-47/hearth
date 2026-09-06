package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCSRFAllowsSafeMethods(t *testing.T) {
	h := RequireCSRFHeader(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/x", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestCSRFBlocksUnsafeWithoutHeader(t *testing.T) {
	h := RequireCSRFHeader(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/x", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestCSRFAllowsUnsafeWithHeader(t *testing.T) {
	h := RequireCSRFHeader(okHandler())
	req := httptest.NewRequest(http.MethodPost, "/api/x", nil)
	req.Header.Set("X-Hearth-CSRF", "1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
}
