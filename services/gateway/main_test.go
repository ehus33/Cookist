package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoutingAndCredentialForwarding(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer demo" {
			t.Error("authorization was not forwarded")
		}
		_, _ = w.Write([]byte("core"))
	}))
	defer core.Close()
	search := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "pizza" {
			t.Error("query was not preserved")
		}
		_, _ = w.Write([]byte("search"))
	}))
	defer search.Close()
	handler, err := newGateway(core.URL, search.URL, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ path, want string }{{"/api/orders", "core"}, {"/api/search?q=pizza", "search"}} {
		req := httptest.NewRequest("GET", test.path, nil)
		req.Header.Set("Authorization", "Bearer demo")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 200 || rec.Body.String() != test.want {
			t.Fatalf("got %d %q", rec.Code, rec.Body.String())
		}
	}
}

func TestServiceErrorsAndOriginPolicy(t *testing.T) {
	handler, err := newGateway("http://127.0.0.1:1", "http://127.0.0.1:1", t.TempDir(), []string{"http://localhost:5173"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		method, path, origin string
		want                 int
	}{{"GET", "/api/stores", "", 502}, {"GET", "/health", "", 503}, {"GET", "/api/stores", "https://evil.example", 403},
		{"OPTIONS", "/api/stores", "http://localhost:5173", 204}, {"POST", "/api/search", "", 405}} {
		req := httptest.NewRequest(test.method, test.path, nil)
		req.Header.Set("Origin", test.origin)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != test.want {
			t.Errorf("%s %s: got %d want %d", test.method, test.path, rec.Code, test.want)
		}
	}
}

func TestOversizedBodiesRejectedBeforeProxying(t *testing.T) {
	handler, err := newGateway("http://127.0.0.1:1", "http://127.0.0.1:1", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/orders", strings.NewReader(strings.Repeat("x", 65537)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 413 {
		t.Fatalf("got %d want 413", rec.Code)
	}
}

func TestStaticFallbackDoesNotMaskAPIErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>Cookist</h1>"), 0600); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()
	handler, err := newGateway(upstream.URL, upstream.URL, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	for _, test := range []struct {
		path string
		want int
	}{{"/orders/123", 200}, {"/api/missing", 404}, {"/missing.js", 404}} {
		res, err := http.Get(server.URL + test.path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != test.want {
			t.Fatalf("%s: got %d", test.path, res.StatusCode)
		}
		if test.want == 404 && strings.Contains(string(body), "Cookist") {
			t.Fatal("API error masked by SPA fallback")
		}
	}
}
