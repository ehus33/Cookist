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
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("authorization was not forwarded")
		}
		_, _ = w.Write([]byte("core"))
	}))
	defer core.Close()
	handler, err := newGateway(core.URL, t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/me", nil)
	req.Header.Set("Authorization", "Bearer token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Body.String() != "core" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}

func TestServiceErrorsAndOriginPolicy(t *testing.T) {
	handler, err := newGateway("http://127.0.0.1:1", t.TempDir(), []string{"http://localhost:5173"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		method, path, origin string
		want                 int
	}{
		{"GET", "/api/me", "", 502}, {"GET", "/health", "", 503},
		{"GET", "/api/me", "https://evil.example", 403},
		{"OPTIONS", "/api/me", "http://localhost:5173", 204},
	} {
		req := httptest.NewRequest(test.method, test.path, nil)
		req.Header.Set("Origin", test.origin)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != test.want {
			t.Errorf("%s %s: got %d want %d", test.method, test.path, rec.Code, test.want)
		}
		if test.method == "OPTIONS" && !strings.Contains(rec.Header().Get("Access-Control-Allow-Methods"), "DELETE") {
			t.Error("allowed origin cannot preflight deployment cleanup")
		}
	}
}

func TestHealthDependsOnCore(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("unexpected health path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer core.Close()
	handler, err := newGateway(core.URL, t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}

func TestOversizedBodiesRejectedBeforeProxying(t *testing.T) {
	handler, err := newGateway("http://127.0.0.1:1", t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/example", strings.NewReader(strings.Repeat("x", 65537)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 413 {
		t.Fatalf("got %d want 413", rec.Code)
	}
}

func TestDeploymentsDisabledByDefault(t *testing.T) {
	handler, err := newGateway("http://127.0.0.1:1", t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("POST", "/api/deployments", nil))
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "deployments_disabled") {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}

func TestStaticFallbackDoesNotMaskAPIErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>Cookist</h1>"), 0600); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()
	handler, err := newGateway(upstream.URL, dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	for _, test := range []struct {
		path string
		want int
	}{
		{"/workspace", 200}, {"/api/missing", 404}, {"/missing.js", 404},
	} {
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

func TestCognitoGateProtectsAPIsAndFailsClosed(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/me" {
			t.Errorf("unexpected auth path %s", r.URL.Path)
		}
		switch r.Header.Get("Authorization") {
		case "Bearer valid":
			w.WriteHeader(200)
		case "Bearer forbidden":
			w.WriteHeader(403)
		case "Bearer unavailable":
			w.WriteHeader(500)
		default:
			w.WriteHeader(401)
		}
	}))
	defer core.Close()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	handler, err := requireCognito(next, core.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		method, path, auth string
		want               int
	}{
		{"GET", "/api/me", "", 401}, {"GET", "/api/example", "Bearer invalid", 401},
		{"GET", "/api/example", "Bearer forbidden", 403}, {"GET", "/api/example", "Bearer unavailable", 503},
		{"GET", "/api/example", "Bearer valid", 204}, {"GET", "/health", "", 204},
		{"GET", "/", "", 204}, {"OPTIONS", "/api/me", "", 204},
	} {
		req := httptest.NewRequest(test.method, test.path, nil)
		req.Header.Set("Authorization", test.auth)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != test.want {
			t.Errorf("%s %s: got %d want %d", test.method, test.path, rec.Code, test.want)
		}
	}
}
