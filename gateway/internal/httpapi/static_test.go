package httpapi_test

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServesStaticFiles(t *testing.T) {
	web := t.TempDir()
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<h1>hearth</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "app.js"), []byte("// x"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv, _ := newTestServer(t, web)
	c := srv.Client()

	get := func(path string) *http.Response {
		t.Helper()
		r, err := c.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	body := func(r *http.Response) string {
		t.Helper()
		b, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	// GET / -> the index
	r := get("/")
	if r.StatusCode != http.StatusOK {
		t.Fatalf("GET /: %d", r.StatusCode)
	}
	if !strings.Contains(body(r), "hearth") {
		t.Fatalf("GET / body missing marker")
	}

	// GET /app.js -> the real file, served as JavaScript
	r = get("/app.js")
	if r.StatusCode != http.StatusOK {
		t.Fatalf("GET /app.js: %d", r.StatusCode)
	}
	if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("GET /app.js content-type = %q", ct)
	}
	_ = body(r)

	// GET a client-side route -> fall back to index.html
	r = get("/nope/deep")
	if r.StatusCode != http.StatusOK {
		t.Fatalf("GET /nope/deep: %d", r.StatusCode)
	}
	if !strings.Contains(body(r), "hearth") {
		t.Fatalf("SPA fallback body missing marker")
	}

	// GET /healthz -> still the health endpoint, not shadowed by the static handler
	r = get("/healthz")
	if r.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz: %d", r.StatusCode)
	}
	if !strings.Contains(body(r), `"status"`) {
		t.Fatalf("GET /healthz body is not the health JSON")
	}

	// GET /api/auth/me -> still the API: 401 without a session, not a 404 file
	r = get("/api/auth/me")
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /api/auth/me: %d", r.StatusCode)
	}
	_ = body(r)
}
