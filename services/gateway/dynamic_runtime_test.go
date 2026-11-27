package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDynamicPublishedRuntime(t *testing.T) {
	config := &dynamicConfig{Collections: []dynamicCollection{{
		ID: "requests", Name: "Requests", Read: "own", Create: "members", Update: "own", Delete: "own",
		Fields: []dynamicField{{ID: "title", Label: "Title", Type: "text", Required: true}},
	}}}
	request := deploymentRequest{
		Name: "Portal", Region: "us-west-2", Template: "builder", Style: "modern",
		Dynamic: config, FormOrigin: "https://portal.example.com",
		Blocks: []pageBlock{
			{ID: "account", Type: "member-account", Text: "<Member area>", Align: "left", Size: "medium"},
			{ID: "records", Type: "data-list", Text: "Records", Align: "left", Size: "medium", Collection: "requests"},
			{ID: "form", Type: "data-form", Text: "New request", Align: "left", Size: "medium", Collection: "requests"},
		},
	}
	data, err := terraformVariables(request, "123456789012", strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	var variables map[string]string
	if err := json.Unmarshal(data, &variables); err != nil {
		t.Fatal(err)
	}
	page := variables["page_html"]
	for _, expected := range []string{
		`data-dynamic="member-account"`, `data-dynamic="data-list"`, `data-dynamic="data-form"`,
		`data-collection="requests"`, `&lt;Member area&gt;`,
		`data-app-base="https://portal.example.com/apps/` + strings.Repeat("a", 32) + `"`,
		dynamicContentPolicy(request.FormOrigin), dynamicRuntimeJS,
	} {
		if !strings.Contains(page, expected) {
			t.Errorf("generated dynamic page lacks %q", expected[:min(len(expected), 100)])
		}
	}
	if strings.Contains(page, "<Member area>") {
		t.Fatal("element title was not escaped")
	}
	request.FormOrigin = ""
	if _, err := terraformVariables(request, "123456789012", strings.Repeat("a", 32)); err == nil {
		t.Fatal("dynamic publication accepted a missing public API origin")
	}
}

func TestStaticPageHasNoDynamicRuntime(t *testing.T) {
	request := deploymentRequest{Name: "Static", Region: "us-west-2", Template: "builder", Blocks: []pageBlock{
		{ID: "intro", Type: "heading", Text: "Hello", Align: "left", Size: "medium"},
	}}
	data, err := terraformVariables(request, "123456789012", strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	var variables map[string]string
	if err := json.Unmarshal(data, &variables); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(variables["page_html"], "<script") || strings.Contains(variables["page_html"], "Content-Security-Policy") {
		t.Fatal("static page unexpectedly gained dynamic runtime")
	}
}

func TestDynamicPublicRouteBypassesOwnerAuthentication(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("public dynamic route unexpectedly called owner identity service: %s", r.URL.Path)
		w.WriteHeader(401)
	}))
	defer core.Close()
	handler, err := newGateway(core.URL, t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler, err = requireCognito(handler, core.URL)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/apps/"+strings.Repeat("a", 32)+"/config", nil))
	if response.Code != 503 || !strings.Contains(response.Body.String(), "dynamic_unavailable") {
		t.Fatalf("unconfigured visitor API = %d %s", response.Code, response.Body.String())
	}
}

func TestDynamicCleanupRequiresDatabaseBeforeTerraformDestroy(t *testing.T) {
	root := t.TempDir()
	id := strings.Repeat("c", 32)
	dir := filepath.Join(root, id)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	d := deployment{ID: id, Name: "Portal", Region: "us-west-2", Status: "ready", TenantID: "owner", CreatedBy: "owner"}
	if err := saveDeployment(dir, d); err != nil {
		t.Fatal(err)
	}
	request := deploymentRequest{Name: "Portal", Region: "us-west-2", Template: "builder",
		Blocks: []pageBlock{{ID: "account", Type: "member-account", Text: "Members", Align: "left", Size: "medium"}},
		Dynamic: &dynamicConfig{Collections: []dynamicCollection{{
			ID: "requests", Name: "Requests", Read: "own", Create: "members", Update: "own", Delete: "own",
			Fields: []dynamicField{{ID: "title", Label: "Title", Type: "text", Required: true}},
		}}},
	}
	if err := saveBuilderDesign(dir, request); err != nil {
		t.Fatal(err)
	}
	called := false
	worker := &deploymentWorker{root: root, owner: "owner"}
	worker.execute = func(context.Context, string, io.Writer, ...string) ([]byte, error) {
		called = true
		return nil, nil
	}
	response := httptest.NewRecorder()
	worker.delete(response, id, deploymentAccess{ActorID: "owner", TenantID: "owner", Role: "platform_owner", CanDelete: true})
	if response.Code != 503 || called {
		t.Fatalf("cleanup without database = %d, Terraform called = %v: %s", response.Code, called, response.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "design.json")); err != nil {
		t.Fatal("cleanup removed app design despite missing database:", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".operation.lock")); !os.IsNotExist(err) {
		t.Fatal("rejected cleanup retained an operation lock:", err)
	}
}
