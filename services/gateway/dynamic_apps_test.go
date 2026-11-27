package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// This store is a test fixture only; production always uses managed DynamoDB.
type memoryDynamicStore struct {
	mu      sync.Mutex
	records map[string]map[string]dynamicRecord
	fail    error
}

func copyDynamicRecord(r dynamicRecord) dynamicRecord {
	values := make(map[string]any, len(r.Values))
	for key, value := range r.Values {
		values[key] = value
	}
	r.Values = values
	return r
}

func (s *memoryDynamicStore) List(_ context.Context, app, collection, owner, after string, limit int) ([]dynamicRecord, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return nil, "", s.fail
	}
	var ids []string
	for id := range s.records[dynamicPK(app, collection)] {
		if id > after {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	next := ""
	if len(ids) > limit {
		ids, next = ids[:limit], ids[limit-1]
	}
	records := []dynamicRecord{}
	for _, id := range ids {
		record := s.records[dynamicPK(app, collection)][id]
		if owner == "" || record.Owner == owner {
			records = append(records, copyDynamicRecord(record))
		}
	}
	return records, next, nil
}

func (s *memoryDynamicStore) Get(_ context.Context, app, collection, id string) (dynamicRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return dynamicRecord{}, s.fail
	}
	record, ok := s.records[dynamicPK(app, collection)][id]
	if !ok {
		return record, errDynamicNotFound
	}
	return copyDynamicRecord(record), nil
}

func (s *memoryDynamicStore) Create(_ context.Context, app, collection string, record dynamicRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	key := dynamicPK(app, collection)
	if s.records[key] == nil {
		s.records[key] = map[string]dynamicRecord{}
	}
	if len(s.records[key]) >= 1000 {
		return errDynamicCapacity
	}
	if _, ok := s.records[key][record.ID]; ok {
		return errDynamicConflict
	}
	s.records[key][record.ID] = copyDynamicRecord(record)
	return nil
}

func (s *memoryDynamicStore) Update(_ context.Context, app, collection string, record dynamicRecord, version int64, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	old, ok := s.records[dynamicPK(app, collection)][record.ID]
	if !ok || old.Version != version || (owner != "" && old.Owner != owner) {
		return errDynamicConflict
	}
	s.records[dynamicPK(app, collection)][record.ID] = copyDynamicRecord(record)
	return nil
}

func (s *memoryDynamicStore) Delete(_ context.Context, app, collection, id string, version int64, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	old, ok := s.records[dynamicPK(app, collection)][id]
	if !ok || old.Version != version || (owner != "" && old.Owner != owner) {
		return errDynamicConflict
	}
	delete(s.records[dynamicPK(app, collection)], id)
	return nil
}

func (s *memoryDynamicStore) DeleteApp(_ context.Context, app string, collections []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	for _, c := range collections {
		delete(s.records, dynamicPK(app, c))
	}
	return nil
}

type dynamicFixture struct {
	worker      *deploymentWorker
	store       *memoryDynamicStore
	service     *dynamicService
	key         *rsa.PrivateKey
	app         string
	design      deploymentRequest
	access      deploymentAccess
	mu          sync.Mutex
	tokens      map[string]string
	revoked     map[string]bool
	calls       []string
	loginToken  string
	authError   string
	activeSub   string
	activeError string
	jwksCalls   int
}

func dynamicTestConfig() *dynamicConfig {
	c := dynamicCollection{
		ID: "public", Name: "Public notes", Read: "public", Create: "members", Update: "own", Delete: "own",
		Fields: []dynamicField{
			{ID: "title", Label: "Title", Type: "text", Required: true},
			{ID: "email", Label: "Email", Type: "email"},
			{ID: "score", Label: "Score", Type: "number"},
			{ID: "done", Label: "Done", Type: "boolean"},
		},
	}
	member, own := c, c
	member.ID, member.Name, member.Read = "members", "Member notes", "members"
	own.ID, own.Name, own.Read = "own", "Private notes", "own"
	return &dynamicConfig{Collections: []dynamicCollection{c, member, own}}
}

func newDynamicFixture(t *testing.T) *dynamicFixture {
	t.Helper()
	// Keep fixture files in the project directory rather than system temp storage.
	dir, err := os.MkdirTemp(".", ".dynamic-test-")
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &dynamicFixture{
		key: key, app: strings.Repeat("a", 32), store: &memoryDynamicStore{records: map[string]map[string]dynamicRecord{}},
		tokens: map[string]string{}, revoked: map[string]bool{},
		access: deploymentAccess{ActorID: "admin", TenantID: "tenant-one", Role: "customer_admin", CanDelete: true},
	}
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/.well-known/jwks.json") {
			f.jwksCalls++
			dynamicReply(rw, 200, map[string]any{"keys": []map[string]string{{
				"kid": "visitor-key", "kty": "RSA", "alg": "RS256", "use": "sig",
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB",
			}}})
			return
		}
		target := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "AWSCognitoIdentityProviderService.")
		f.calls = append(f.calls, target)
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Errorf("Cognito input: %v", err)
		}
		if target == "GetUser" || target == "GlobalSignOut" {
			raw, _ := input["AccessToken"].(string)
			sub := f.tokens[raw]
			if target == "GetUser" && f.activeError != "" {
				dynamicReply(rw, 400, map[string]string{"__type": f.activeError})
				return
			}
			if target == "GetUser" && f.activeSub != "" {
				sub = f.activeSub
			}
			if sub == "" || f.revoked[raw] {
				dynamicReply(rw, 400, map[string]string{"__type": "NotAuthorizedException"})
				return
			}
			if target == "GlobalSignOut" {
				f.revoked[raw] = true
				dynamicReply(rw, 200, map[string]any{})
			} else {
				dynamicReply(rw, 200, map[string]any{"UserAttributes": []map[string]string{{"Name": "sub", "Value": sub}}})
			}
			return
		}
		if input["ClientId"] != "visitorclient" || input["SecretHash"] != nil {
			t.Errorf("visitor auth must use the fixed public client: %v", input["ClientId"])
		}
		if f.authError != "" {
			dynamicReply(rw, 400, map[string]string{"__type": f.authError})
			return
		}
		switch target {
		case "SignUp":
			dynamicReply(rw, 200, map[string]bool{"UserConfirmed": false})
		case "ConfirmSignUp", "ForgotPassword", "ConfirmForgotPassword":
			dynamicReply(rw, 200, map[string]any{})
		case "InitiateAuth":
			if input["AuthFlow"] != "USER_PASSWORD_AUTH" {
				t.Errorf("unexpected visitor auth flow: %v", input["AuthFlow"])
			}
			dynamicReply(rw, 200, map[string]any{"AuthenticationResult": map[string]any{"AccessToken": f.loginToken, "ExpiresIn": 3600}})
		default:
			t.Errorf("unexpected Cognito target %s", target)
			dynamicReply(rw, 500, map[string]any{})
		}
	}))
	t.Cleanup(server.Close)
	f.service = &dynamicService{
		store: f.store, clientID: "visitorclient", issuer: server.URL + "/us-west-2_visitors",
		endpoint: server.URL, client: server.Client(), rates: map[string]dynamicRate{},
	}
	f.worker = &deploymentWorker{root: root, owner: "owner", dynamic: f.service, localAccess: true}
	f.design = deploymentRequest{Name: "Dynamic test", Region: "us-west-2", Template: "builder", Dynamic: dynamicTestConfig(),
		Blocks: []pageBlock{{ID: "heading", Type: "heading", Text: "Notes", Align: "left", Size: "medium"}}}
	f.saveApp(t, f.app, "ready", "tenant-one", f.design)
	f.loginToken = f.token(t, "visitor-one", nil)
	return f
}

func (f *dynamicFixture) saveApp(t *testing.T, id, status, tenant string, req deploymentRequest) {
	t.Helper()
	dir := filepath.Join(f.worker.root, id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if status == "running" {
		_ = os.Remove(filepath.Join(dir, "result.json"))
	}
	if err := saveDeployment(dir, deployment{ID: id, Status: status, URL: "https://example.cloudfront.net", TenantID: tenant, CreatedBy: "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := saveBuilderDesign(dir, req); err != nil {
		t.Fatal(err)
	}
}

func (f *dynamicFixture) token(t *testing.T, sub string, modify func(jwt.MapClaims)) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss": f.service.issuer, "sub": sub, "exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Add(-time.Minute).Unix(), "token_use": "access", "client_id": "visitorclient",
	}
	if modify != nil {
		modify(claims)
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "visitor-key"
	raw, err := token.SignedString(f.key)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.tokens[raw] = sub
	f.mu.Unlock()
	return raw
}

func (f *dynamicFixture) request(t *testing.T, method, path, token, body string, access *deploymentAccess, version string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Origin", "https://example.cloudfront.net")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if version != "" {
		r.Header.Set("If-Match", version)
	}
	rw := httptest.NewRecorder()
	handled := false
	if access != nil {
		handled = f.worker.dynamicAdminRoute(rw, r, *access)
	} else {
		handled = f.worker.dynamicPublicRoute(rw, r)
	}
	if !handled {
		t.Fatalf("dynamic route did not handle %s", path)
	}
	return rw
}

func dynamicWantStatus(t *testing.T, r *httptest.ResponseRecorder, status int) {
	t.Helper()
	if r.Code != status {
		t.Fatalf("status=%d want=%d body=%s", r.Code, status, r.Body)
	}
}

func dynamicRecordResponse(t *testing.T, rw *httptest.ResponseRecorder) dynamicRecord {
	t.Helper()
	var envelope struct{ Record dynamicRecord }
	dec := json.NewDecoder(bytes.NewReader(rw.Body.Bytes()))
	dec.UseNumber()
	if err := dec.Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Record
}

func TestDynamicConfigAndImmutableUpdate(t *testing.T) {
	valid := dynamicTestConfig()
	if err := validateDynamicConfig(valid); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		edit func(*dynamicConfig)
	}{
		{"bad ID", func(c *dynamicConfig) { c.Collections[0].ID = "../notes" }},
		{"duplicate", func(c *dynamicConfig) { c.Collections[1].ID = c.Collections[0].ID }},
		{"name bound", func(c *dynamicConfig) { c.Collections[0].Name = strings.Repeat("n", 81) }},
		{"empty name", func(c *dynamicConfig) { c.Collections[0].Name = " " }},
		{"read", func(c *dynamicConfig) { c.Collections[0].Read = "any" }},
		{"write", func(c *dynamicConfig) { c.Collections[0].Create = "public" }},
		{"field ID", func(c *dynamicConfig) { c.Collections[0].Fields[0].ID = "Title" }},
		{"field type", func(c *dynamicConfig) { c.Collections[0].Fields[0].Type = "object" }},
		{"field duplicate", func(c *dynamicConfig) { c.Collections[0].Fields[1].ID = "title" }},
		{"field label", func(c *dynamicConfig) { c.Collections[0].Fields[0].Label = strings.Repeat("a", 81) }},
		{"field bound", func(c *dynamicConfig) {
			for len(c.Collections[0].Fields) < 13 {
				c.Collections[0].Fields = append(c.Collections[0].Fields, dynamicField{ID: fmt.Sprintf("f%d", len(c.Collections[0].Fields)), Label: "Field", Type: "text"})
			}
		}},
		{"collection bound", func(c *dynamicConfig) {
			for len(c.Collections) < 9 {
				next := c.Collections[0]
				next.ID = fmt.Sprintf("c%d", len(c.Collections))
				c.Collections = append(c.Collections, next)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := dynamicTestConfig()
			tc.edit(c)
			if validateDynamicConfig(c) == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
	if validateDynamicConfig(nil) != nil || validateDynamicConfig(&dynamicConfig{}) == nil {
		t.Fatal("disabled/empty config handling")
	}
	next := dynamicTestConfig()
	add := next.Collections[0]
	add.ID = "new"
	next.Collections = append(next.Collections, add)
	if err := validateDynamicUpdate(valid, next); err != nil {
		t.Fatal(err)
	}
	next.Collections[0].Fields[0].Required = false
	if validateDynamicUpdate(valid, next) == nil || validateDynamicUpdate(valid, nil) == nil {
		t.Fatal("existing schema can be changed/disabled")
	}
	request := deploymentRequest{Dynamic: valid, Blocks: []pageBlock{{Type: "data-list", Collection: "public"}, {Type: "member-account"}}}
	if validateDynamicBlocks(request) != nil {
		t.Fatal("bound blocks rejected")
	}
	request.Blocks[0].Collection = "missing"
	if validateDynamicBlocks(request) == nil {
		t.Fatal("unbound block accepted")
	}
	request.Dynamic, request.Blocks = nil, []pageBlock{{Type: "member-account"}}
	if validateDynamicBlocks(request) == nil {
		t.Fatal("account block enabled without config")
	}
	for _, raw := range []string{
		`{"Collections":[]}`, `{"collections":[],"collections":[]}`,
		`{"collections":[{"ID":"notes"}]}`,
		`{"collections":[{"fields":[{"id":"title","Type":"text"}]}]}`,
	} {
		var cfg dynamicConfig
		if json.Unmarshal([]byte(raw), &cfg) == nil {
			t.Fatal("ambiguous or unknown dynamic configuration keys accepted")
		}
	}
}

func TestDynamicJWTVerifiedPoolClientAndSignature(t *testing.T) {
	f := newDynamicFixture(t)
	for _, tc := range []struct {
		name string
		edit func(jwt.MapClaims)
	}{
		{"wrong pool", func(c jwt.MapClaims) { c["iss"] = f.service.issuer + "-portal" }},
		{"wrong client", func(c jwt.MapClaims) { c["client_id"] = "portalclient" }},
		{"ID token", func(c jwt.MapClaims) { c["token_use"] = "id" }},
		{"expired", func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Minute).Unix() }},
		{"missing expiry", func(c jwt.MapClaims) { delete(c, "exp") }},
		{"missing subject", func(c jwt.MapClaims) { delete(c, "sub") }},
		{"future issued", func(c jwt.MapClaims) { c["iat"] = time.Now().Add(time.Hour).Unix() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.service.verifyToken(context.Background(), f.token(t, "visitor-one", tc.edit)); err == nil {
				t.Fatal("invalid JWT accepted")
			}
		})
	}
	if sub, err := f.service.verifyToken(context.Background(), f.loginToken); err != nil || sub != "visitor-one" {
		t.Fatalf("valid token rejected: %s %v", sub, err)
	}
	parts := strings.Split(f.loginToken, ".")
	signature, _ := base64.RawURLEncoding.DecodeString(parts[2])
	signature[0] ^= 1
	parts[2] = base64.RawURLEncoding.EncodeToString(signature)
	if _, err := f.service.verifyToken(context.Background(), strings.Join(parts, ".")); err == nil {
		t.Fatal("invalid signature accepted")
	}
	hmac := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"iss": f.service.issuer, "sub": "visitor-one", "exp": time.Now().Add(time.Hour).Unix()})
	hmac.Header["kid"] = "visitor-key"
	raw, _ := hmac.SignedString([]byte("not-a-public-key"))
	if _, err := f.service.verifyToken(context.Background(), raw); err == nil {
		t.Fatal("algorithm confusion accepted")
	}
	// Unknown kids do not force repeated network fetches.
	unknown := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"iss": f.service.issuer, "sub": "visitor-one", "exp": time.Now().Add(time.Hour).Unix()})
	unknown.Header["kid"] = "unknown"
	raw, _ = unknown.SignedString(f.key)
	for i := 0; i < 4; i++ {
		if _, err := f.service.verifyToken(context.Background(), raw); err == nil {
			t.Fatal("unknown key accepted")
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.jwksCalls != 1 {
		t.Fatalf("unbounded JWKS fetch: %d", f.jwksCalls)
	}
}

func TestDynamicPublicAndAdminCRUDIsolation(t *testing.T) {
	f := newDynamicFixture(t)
	base := "/apps/" + f.app + "/collections/"
	token := f.loginToken
	other := f.token(t, "visitor-two", nil)
	rw := f.request(t, "POST", base+"own/records", token, `{"values":{"title":"First","score":2,"done":false}}`, nil, "")
	dynamicWantStatus(t, rw, 201)
	record := dynamicRecordResponse(t, rw)
	if record.Version != 1 || !record.CanUpdate || !record.CanDelete || record.ID == "" || record.CreatedAt == "" {
		t.Fatal("created record envelope is incomplete")
	}
	if strings.Contains(rw.Body.String(), `"owner"`) || strings.Contains(rw.Body.String(), "visitor-one") {
		t.Fatal("visitor ownership exposed")
	}
	item := base + "own/records/" + record.ID
	dynamicWantStatus(t, f.request(t, "GET", item, "", "", nil, ""), 401)
	dynamicWantStatus(t, f.request(t, "GET", item, other, "", nil, ""), 404)
	dynamicWantStatus(t, f.request(t, "PATCH", item, other, `{"values":{"title":"Stolen"},"version":1}`, nil, ""), 404)
	dynamicWantStatus(t, f.request(t, "DELETE", item, other, "", nil, "1"), 404)
	rw = f.request(t, "GET", base+"own/records", other, "", nil, "")
	dynamicWantStatus(t, rw, 200)
	if !strings.Contains(rw.Body.String(), `"records":[]`) {
		t.Fatalf("own list leaked data: %s", rw.Body)
	}
	rw = f.request(t, "PATCH", item, token, `{"values":{"title":"Updated"},"version":1}`, nil, "")
	dynamicWantStatus(t, rw, 200)
	updated := dynamicRecordResponse(t, rw)
	if updated.Version != 2 || updated.Values["score"] != json.Number("2") || updated.CreatedAt != record.CreatedAt || updated.UpdatedAt == record.UpdatedAt {
		t.Fatalf("patch did not preserve fields/creation metadata: %+v", updated)
	}
	dynamicWantStatus(t, f.request(t, "PATCH", item, token, `{"values":{"title":"Stale"},"version":1}`, nil, ""), 409)
	dynamicWantStatus(t, f.request(t, "DELETE", item, token, "", nil, "1"), 409)
	dynamicWantStatus(t, f.request(t, "DELETE", item, token, "", nil, ""), 400)
	dynamicWantStatus(t, f.request(t, "POST", base+"public/records", "", `{"values":{"title":"Anon"}}`, nil, ""), 401)
	dynamicWantStatus(t, f.request(t, "GET", base+"members/records", "", "", nil, ""), 401)
	dynamicWantStatus(t, f.request(t, "GET", base+"members/records", other, "", nil, ""), 200)

	// Public reads do not grant mutation rights to another visitor.
	rw = f.request(t, "POST", base+"public/records", token, `{"values":{"title":"Public"}}`, nil, "")
	dynamicWantStatus(t, rw, 201)
	public := dynamicRecordResponse(t, rw)
	publicItem := base + "public/records/" + public.ID
	rw = f.request(t, "GET", publicItem, other, "", nil, "")
	dynamicWantStatus(t, rw, 200)
	public = dynamicRecordResponse(t, rw)
	if public.CanUpdate || public.CanDelete {
		t.Fatal("public read exposed another visitor's edit capabilities")
	}
	dynamicWantStatus(t, f.request(t, "GET", publicItem, "", "", nil, ""), 200)
	dynamicWantStatus(t, f.request(t, "PATCH", publicItem, other, `{"values":{"title":"Stolen"},"version":1}`, nil, ""), 404)

	// Record IDs never cross app partitions, even with a valid shared-pool token.
	appTwo := strings.Repeat("b", 32)
	f.saveApp(t, appTwo, "ready", "tenant-two", f.design)
	dynamicWantStatus(t, f.request(t, "GET", "/apps/"+appTwo+"/collections/own/records/"+record.ID, token, "", nil, ""), 404)
	adminItem := "/api/deployments/" + f.app + "/collections/own/records/" + record.ID
	foreign := f.access
	foreign.TenantID = "tenant-two"
	dynamicWantStatus(t, f.request(t, "GET", adminItem, "", "", &foreign, ""), 404)
	viewer := f.access
	viewer.CanDelete, viewer.Role = false, "viewer"
	dynamicWantStatus(t, f.request(t, "GET", adminItem, "", "", &viewer, ""), 403)
	rw = f.request(t, "PATCH", adminItem, "", `{"values":{"title":"Managed"},"version":2}`, &f.access, "")
	dynamicWantStatus(t, rw, 200)
	dynamicWantStatus(t, f.request(t, "DELETE", adminItem, "", "", &f.access, "3"), 200)
	dynamicWantStatus(t, f.request(t, "GET", item, token, "", nil, ""), 404)
}

func TestDynamicRoutingCORSAndSavedDesign(t *testing.T) {
	f := newDynamicFixture(t)
	configPath := "/apps/" + f.app + "/config"
	rw := f.request(t, "GET", configPath, "", "", nil, "")
	dynamicWantStatus(t, rw, 200)
	if !strings.Contains(rw.Body.String(), `"authentication":true`) || rw.Header().Get("Access-Control-Allow-Origin") != "https://example.cloudfront.net" ||
		rw.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("config/CORS contract mismatch")
	}
	rw = f.request(t, "OPTIONS", configPath, "", "", nil, "")
	dynamicWantStatus(t, rw, 204)
	for _, origin := range []string{"", "https://evil.test", "https://example.cloudfront.net/", "null", "*"} {
		r := httptest.NewRequest("GET", configPath, nil)
		r.Header.Set("Origin", origin)
		rw := httptest.NewRecorder()
		f.worker.dynamicPublicRoute(rw, r)
		dynamicWantStatus(t, rw, 403)
		if rw.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("foreign origin received CORS grant")
		}
	}
	for _, path := range []string{"/apps/../config", "/apps/" + f.app + "/bad", configPath + "/", "/apps/" + strings.Repeat("c", 32) + "/config"} {
		dynamicWantStatus(t, f.request(t, "GET", path, "", "", nil, ""), 404)
	}
	r := httptest.NewRequest("GET", "/not-apps", nil)
	if f.worker.dynamicPublicRoute(httptest.NewRecorder(), r) ||
		f.worker.dynamicAdminRoute(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/deployments/"+f.app+"/design", nil), f.access) {
		t.Fatal("dynamic routing captured unrelated routes")
	}
	for _, status := range []string{"running", "failed", "deleting", "cleanup_failed"} {
		f.saveApp(t, f.app, status, "tenant-one", f.design)
		dynamicWantStatus(t, f.request(t, "GET", configPath, "", "", nil, ""), 404)
	}
	f.saveApp(t, f.app, "ready", "tenant-one", f.design)
	if err := os.Remove(filepath.Join(f.worker.root, f.app, "design.json")); err != nil {
		t.Fatal(err)
	}
	dynamicWantStatus(t, f.request(t, "GET", configPath, "", "", nil, ""), 404)
	f.saveApp(t, f.app, "ready", "tenant-one", f.design)
	disabled := f.design
	disabled.Dynamic = nil
	f.saveApp(t, f.app, "ready", "tenant-one", disabled)
	dynamicWantStatus(t, f.request(t, "GET", configPath, "", "", nil, ""), 404)
	f.worker.dynamic = nil
	dynamicWantStatus(t, f.request(t, "GET", configPath, "", "", nil, ""), 503)
	foreign := f.access
	foreign.TenantID = "tenant-two"
	dynamicWantStatus(t, f.request(t, "GET", "/api/deployments/"+f.app+"/collections/public/records", "", "", &foreign, ""), 404)
}

func TestDynamicValuesBoundedJSONAndPagination(t *testing.T) {
	f := newDynamicFixture(t)
	path := "/apps/" + f.app + "/collections/public/records"
	for _, body := range []string{
		`{"values":{}}`, `{"values":{"title":null}}`, `{"values":{"title":42}}`,
		`{"values":{"title":"x","extra":1}}`, `{"values":{"title":"x","done":"false"}}`,
		`{"values":{"title":"x","score":"2"}}`, `{"values":{"title":"x","score":1e999}}`,
		`{"values":{"title":"x","email":"not an email"}}`,
		`{"values":{"title":"x","title":"y"}}`, `{"values":{"title":"x"},"values":{"title":"y"}}`,
		`{"values":{"title":"x"}} {}`, `{"Values":{"title":"x"}}`, `{"values":{"title":"x"},"owner":"spoof"}`,
		`{"values":{"title":"` + strings.Repeat("x", 2001) + `"}}`,
	} {
		dynamicWantStatus(t, f.request(t, "POST", path, f.loginToken, body, nil, ""), 400)
	}
	dynamicWantStatus(t, f.request(t, "POST", path, f.loginToken, `{"values":{"title":"`+strings.Repeat("x", 65536)+`"}}`, nil, ""), 413)
	for _, title := range []string{"One", "Two", "Three"} {
		rw := f.request(t, "POST", path, f.loginToken, fmt.Sprintf(`{"values":{"title":%q,"email":"person@example.com","score":0,"done":false}}`, title), nil, "")
		dynamicWantStatus(t, rw, 201)
	}
	rw := f.request(t, "GET", path+"?limit=1", "", "", nil, "")
	dynamicWantStatus(t, rw, 200)
	var page struct {
		Records []dynamicRecord `json:"records"`
		Next    string          `json:"next"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &page); err != nil || len(page.Records) != 1 || page.Next == "" {
		t.Fatalf("invalid first page: %s %v", rw.Body, err)
	}
	dynamicWantStatus(t, f.request(t, "GET", path+"?limit=1&cursor="+page.Next, "", "", nil, ""), 200)
	dynamicWantStatus(t, f.request(t, "GET", "/apps/"+f.app+"/collections/own/records?cursor="+page.Next, f.loginToken, "", nil, ""), 400)
	appTwo := strings.Repeat("b", 32)
	f.saveApp(t, appTwo, "ready", "tenant-two", f.design)
	dynamicWantStatus(t, f.request(t, "GET", "/apps/"+appTwo+"/collections/public/records?cursor="+page.Next, "", "", nil, ""), 400)
	for _, query := range []string{"?limit=51", "?limit=0", "?limit=one", "?limit=1&limit=2", "?cursor=bad", "?cursor=one&cursor=two", "?next=bad", "?unexpected=1"} {
		dynamicWantStatus(t, f.request(t, "GET", path+query, "", "", nil, ""), 400)
	}
	ownPath := "/apps/" + f.app + "/collections/own/records"
	for _, title := range []string{"Private one", "Private two"} {
		dynamicWantStatus(t, f.request(t, "POST", ownPath, f.loginToken, fmt.Sprintf(`{"values":{"title":%q}}`, title), nil, ""), 201)
	}
	rw = f.request(t, "GET", ownPath+"?limit=1", f.loginToken, "", nil, "")
	if err := json.Unmarshal(rw.Body.Bytes(), &page); err != nil || page.Next == "" {
		t.Fatal("own list did not paginate")
	}
	dynamicWantStatus(t, f.request(t, "GET", ownPath+"?cursor="+page.Next, f.token(t, "visitor-two", nil), "", nil, ""), 400)
	f.store.fail = context.DeadlineExceeded
	dynamicWantStatus(t, f.request(t, "GET", path, "", "", nil, ""), 503)
}

func TestDynamicDisabledWritesAdminOverrideAndCapacity(t *testing.T) {
	f := newDynamicFixture(t)
	f.design.Dynamic.Collections[0].Create = "none"
	f.design.Dynamic.Collections[0].Update = "none"
	f.design.Dynamic.Collections[0].Delete = "none"
	f.saveApp(t, f.app, "ready", "tenant-one", f.design)
	path := "/apps/" + f.app + "/collections/public/records"
	dynamicWantStatus(t, f.request(t, "POST", path, f.loginToken, `{"values":{"title":"Denied"}}`, nil, ""), 403)
	adminPath := "/api/deployments/" + f.app + "/collections/public/records"
	rw := f.request(t, "POST", adminPath, "", `{"values":{"title":"Managed"}}`, &f.access, "")
	dynamicWantStatus(t, rw, 201)
	record := dynamicRecordResponse(t, rw)
	if !record.CanUpdate || !record.CanDelete {
		t.Fatal("managers cannot manage records when visitor writes are disabled")
	}
	rw = f.request(t, "GET", path+"/"+record.ID, f.loginToken, "", nil, "")
	dynamicWantStatus(t, rw, 200)
	if got := dynamicRecordResponse(t, rw); got.CanUpdate || got.CanDelete {
		t.Fatal("disabled collection exposed visitor mutation capabilities")
	}
	dynamicWantStatus(t, f.request(t, "PATCH", path+"/"+record.ID, f.loginToken, `{"values":{"title":"Denied"},"version":1}`, nil, ""), 403)
	dynamicWantStatus(t, f.request(t, "DELETE", adminPath+"/"+record.ID, "", "", &f.access, "1"), 200)
	for i := 0; i < 1000; i++ {
		record.ID = fmt.Sprintf("%032x", i)
		if err := f.store.Create(context.Background(), f.app, "public", record); err != nil {
			t.Fatal(err)
		}
	}
	dynamicWantStatus(t, f.request(t, "POST", adminPath, "", `{"values":{"title":"Full"}}`, &f.access, ""), 409)
}

func TestDynamicVisitorAuthAndRevokedSession(t *testing.T) {
	f := newDynamicFixture(t)
	base := "/apps/" + f.app + "/auth/"
	for _, tc := range []struct{ action, body, envelope string }{
		{"signup", `{"email":"person@example.com","password":"secret-pass"}`, `"confirmationRequired":true`},
		{"confirm", `{"email":"person@example.com","code":"123456"}`, `"confirmed":true`},
		{"forgot", `{"email":"person@example.com"}`, `"confirmationRequired":true`},
		{"reset", `{"email":"person@example.com","code":"123456","password":"new-secret-pass"}`, `"reset":true`},
		{"login", `{"email":"person@example.com","password":"secret-pass"}`, `"expiresIn":3600`},
	} {
		rw := f.request(t, "POST", base+tc.action, "", tc.body, nil, "")
		dynamicWantStatus(t, rw, 200)
		if !strings.Contains(rw.Body.String(), tc.envelope) {
			t.Fatalf("auth envelope mismatch: %s", rw.Body)
		}
	}
	dynamicWantStatus(t, f.request(t, "GET", "/apps/"+f.app+"/me", f.loginToken, "", nil, ""), 200)
	dynamicWantStatus(t, f.request(t, "POST", base+"logout", "", "", nil, ""), 401)
	rw := f.request(t, "POST", base+"logout", f.loginToken, `{}`, nil, "")
	dynamicWantStatus(t, rw, 200)
	if !strings.Contains(rw.Body.String(), `"signedOut":true`) {
		t.Fatal("logout envelope mismatch")
	}
	dynamicWantStatus(t, f.request(t, "GET", "/apps/"+f.app+"/me", f.loginToken, "", nil, ""), 401)
	dynamicWantStatus(t, f.request(t, "POST", "/apps/"+f.app+"/collections/public/records", f.loginToken, `{"values":{"title":"Revoked"}}`, nil, ""), 401)
	for _, tc := range []struct {
		cognito string
		status  int
		code    string
	}{
		{"NotAuthorizedException", 401, "invalid_credentials"},
		{"UserNotConfirmedException", 400, "confirmation_required"},
		{"CodeMismatchException", 400, "invalid_confirmation"},
		{"TooManyRequestsException", 429, "rate_limited"},
		{"InternalErrorException", 503, "visitor_auth_unavailable"},
	} {
		f.mu.Lock()
		f.authError = tc.cognito
		f.mu.Unlock()
		rw := f.request(t, "POST", base+"login", "", `{"email":"person@example.com","password":"secret-pass"}`, nil, "")
		dynamicWantStatus(t, rw, tc.status)
		if !strings.Contains(rw.Body.String(), tc.code) || strings.Contains(rw.Body.String(), "secret-pass") {
			t.Fatalf("auth error is unsafe or ambiguous: %s", rw.Body)
		}
	}
}

func TestDynamicRateLimitsAndCleanup(t *testing.T) {
	f := newDynamicFixture(t)
	for i := 0; i < 30; i++ {
		if !f.service.allow(f.app, true) {
			t.Fatal("auth limit applied early")
		}
	}
	dynamicWantStatus(t, f.request(t, "POST", "/apps/"+f.app+"/auth/login", "", `{}`, nil, ""), 429)
	for i := 0; i < 120; i++ {
		if !f.service.allow(f.app, false) {
			t.Fatal("write limit applied early")
		}
	}
	dynamicWantStatus(t, f.request(t, "POST", "/apps/"+f.app+"/collections/public/records", f.loginToken, `{"values":{"title":"Limited"}}`, nil, ""), 429)
	f.service.rates[f.app] = dynamicRate{start: time.Now().Add(-2 * time.Minute), auth: 30, writes: 120}
	if !f.service.allow(f.app, true) {
		t.Fatal("auth window did not reset")
	}
	if err := f.worker.dynamicDeleteApp(context.Background(), f.app); err == nil {
		t.Fatal("cleanup permitted while app is ready")
	}
	appTwo := strings.Repeat("b", 32)
	f.saveApp(t, appTwo, "ready", "tenant-two", f.design)
	record := dynamicRecord{ID: strings.Repeat("c", 32), Owner: "visitor-one", Version: 1, Values: map[string]any{"title": "Retain"}}
	_ = f.store.Create(context.Background(), appTwo, "public", record)
	_ = f.store.Create(context.Background(), f.app, "public", record)
	f.saveApp(t, f.app, "deleting", "tenant-one", f.design)
	f.store.fail = errors.New("Dynamo unavailable")
	if err := f.worker.dynamicDeleteApp(context.Background(), f.app); err == nil {
		t.Fatal("failed cleanup was silently accepted")
	}
	if _, err := os.Stat(filepath.Join(f.worker.root, f.app, "design.json")); err != nil {
		t.Fatal("failed cleanup discarded saved design")
	}
	f.store.fail = nil
	if err := f.worker.dynamicDeleteApp(context.Background(), f.app); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Get(context.Background(), f.app, "public", record.ID); !errors.Is(err, errDynamicNotFound) {
		t.Fatal("app records survived cleanup")
	}
	if _, err := f.store.Get(context.Background(), appTwo, "public", record.ID); err != nil {
		t.Fatal("cleanup deleted another app's records")
	}
	f.worker.dynamic = nil
	if err := f.worker.dynamicDeleteApp(context.Background(), f.app); err == nil {
		t.Fatal("disabled managed database silently orphaned dynamic data")
	}
	if err := os.Remove(filepath.Join(f.worker.root, f.app, "design.json")); err != nil {
		t.Fatal(err)
	}
	if err := f.worker.dynamicDeleteApp(context.Background(), f.app); err != nil {
		t.Fatalf("legacy deployment without a design cannot be cleaned: %v", err)
	}
}

type dynamicRoundTripFunc func(*http.Request) (*http.Response, error)

func (f dynamicRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDynamicActiveVisitorIdentityAndUnavailableAuth(t *testing.T) {
	f := newDynamicFixture(t)
	path := "/apps/" + f.app + "/me"
	f.mu.Lock()
	f.activeSub = "different-visitor"
	f.mu.Unlock()
	dynamicWantStatus(t, f.request(t, "GET", path, f.loginToken, "", nil, ""), 401)
	f.mu.Lock()
	f.activeSub, f.activeError = "", "TooManyRequestsException"
	f.mu.Unlock()
	dynamicWantStatus(t, f.request(t, "GET", path, f.loginToken, "", nil, ""), 429)
	f.mu.Lock()
	f.activeError = "InternalErrorException"
	f.mu.Unlock()
	dynamicWantStatus(t, f.request(t, "GET", path, f.loginToken, "", nil, ""), 503)
	f.service.client = &http.Client{Transport: dynamicRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})}
	dynamicWantStatus(t, f.request(t, "GET", path, f.loginToken, "", nil, ""), 503)
	f.service.keys = nil
	f.service.lastFetch = time.Time{}
	dynamicWantStatus(t, f.request(t, "GET", path, f.loginToken, "", nil, ""), 503)
}

func TestDynamicGatewayRoutesBeforePortalOriginGate(t *testing.T) {
	f := newDynamicFixture(t)
	gateway, err := newGateway("http://127.0.0.1:1", f.worker.root, []string{"https://portal.example.com"}, f.worker)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "OPTIONS"} {
		r := httptest.NewRequest(method, "/apps/"+f.app+"/config", nil)
		r.Header.Set("Origin", "https://example.cloudfront.net")
		rw := httptest.NewRecorder()
		gateway.ServeHTTP(rw, r)
		want := 200
		if method == "OPTIONS" {
			want = 204
		}
		dynamicWantStatus(t, rw, want)
	}
}

func TestDynamicFullGatewayHTTPVisitorAndOwnerRoutes(t *testing.T) {
	f := newDynamicFixture(t)
	var ownerChecks atomic.Int32
	core := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		ownerChecks.Add(1)
		if r.URL.Path != "/api/me" || r.Header.Get("Authorization") != "Bearer owner-test-token" {
			dynamicReply(rw, 401, map[string]any{})
			return
		}
		dynamicReply(rw, 200, map[string]string{"actorId": "owner"})
	}))
	defer core.Close()
	f.worker.coreURL, f.worker.client = core.URL, core.Client()
	const appOrigin = "https://example.cloudfront.net"
	const portalOrigin = "https://portal.example.com"
	gateway, err := newGateway(core.URL, f.worker.root, []string{portalOrigin}, f.worker)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := requireCognito(gateway, core.URL)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client := &http.Client{Timeout: 3 * time.Second}
	request := func(method, path, origin, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		r, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Origin", origin)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if method == http.MethodOptions {
			r.Header.Set("Access-Control-Request-Method", "POST")
			r.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 65537))
		if err != nil || len(data) > 65536 {
			t.Fatalf("HTTP response exceeded its bound: %v", err)
		}
		rw := httptest.NewRecorder()
		rw.HeaderMap = response.Header.Clone()
		rw.WriteHeader(response.StatusCode)
		_, _ = rw.Body.Write(data)
		return rw
	}
	base := "/apps/" + f.app
	rw := request("GET", base+"/config", appOrigin, "", "")
	dynamicWantStatus(t, rw, 200)
	if !strings.Contains(rw.Body.String(), `"authentication":true`) || rw.Header().Get("Access-Control-Allow-Origin") != appOrigin {
		t.Fatal("published config did not pass through the full gateway")
	}
	recordsPath := base + "/collections/own/records"
	rw = request("OPTIONS", recordsPath, appOrigin, "", "")
	dynamicWantStatus(t, rw, 204)
	if rw.Header().Get("Access-Control-Allow-Origin") != appOrigin ||
		!strings.Contains(rw.Header().Get("Access-Control-Allow-Headers"), "Authorization") ||
		rw.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("full gateway visitor preflight has incorrect CORS")
	}
	rw = request("POST", recordsPath, appOrigin, f.loginToken, `{"values":{"title":"Through HTTP","score":2,"done":false}}`)
	dynamicWantStatus(t, rw, 201)
	record := dynamicRecordResponse(t, rw)
	rw = request("GET", recordsPath+"/"+record.ID, appOrigin, f.loginToken, "")
	dynamicWantStatus(t, rw, 200)
	if fetched := dynamicRecordResponse(t, rw); fetched.ID != record.ID || fetched.Values["score"] != json.Number("2") {
		t.Fatal("HTTP visitor record did not round trip")
	}
	dynamicWantStatus(t, request("GET", recordsPath, appOrigin, "", ""), 401)
	dynamicWantStatus(t, request("GET", base+"/config", portalOrigin, "", ""), 403)
	dynamicWantStatus(t, request("OPTIONS", recordsPath, "https://foreign.example.com", "", ""), 403)
	if ownerChecks.Load() != 0 {
		t.Fatal("public visitor routes were sent to Cookist owner authentication")
	}
	adminPath := "/api/deployments/" + f.app + "/collections/own/records/" + record.ID
	rw = request("GET", adminPath, portalOrigin, "owner-test-token", "")
	dynamicWantStatus(t, rw, 200)
	if managed := dynamicRecordResponse(t, rw); managed.ID != record.ID || !managed.CanUpdate || !managed.CanDelete {
		t.Fatal("owner-admin route failed through the full authenticated gateway")
	}
	if ownerChecks.Load() < 2 {
		t.Fatal("owner-admin route bypassed the gateway or worker identity checks")
	}
}

type blockingDynamicStore struct {
	*memoryDynamicStore
	entered, release chan struct{}
}

func (s *blockingDynamicStore) Create(ctx context.Context, app, collection string, record dynamicRecord) error {
	close(s.entered)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.release:
		return s.memoryDynamicStore.Create(ctx, app, collection, record)
	}
}

func TestDynamicCleanupWaitsForWritesAndStopsFurtherAccess(t *testing.T) {
	f := newDynamicFixture(t)
	blocked := &blockingDynamicStore{memoryDynamicStore: f.store, entered: make(chan struct{}), release: make(chan struct{})}
	f.service.store = blocked
	requestDone := make(chan *httptest.ResponseRecorder, 1)
	r := httptest.NewRequest("POST", "/apps/"+f.app+"/collections/public/records", strings.NewReader(`{"values":{"title":"Final write"}}`))
	r.Header.Set("Origin", "https://example.cloudfront.net")
	r.Header.Set("Authorization", "Bearer "+f.loginToken)
	go func() {
		rw := httptest.NewRecorder()
		f.worker.dynamicPublicRoute(rw, r)
		requestDone <- rw
	}()
	select {
	case <-blocked.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("write did not reach storage")
	}
	cleanupDone := make(chan error, 1)
	go func() {
		f.worker.mu.Lock()
		defer f.worker.mu.Unlock()
		dir := filepath.Join(f.worker.root, f.app)
		d, err := readDeployment(dir, f.app)
		if err == nil {
			d.Status, d.URL = "deleting", ""
			err = saveDeployment(dir, d)
		}
		if err == nil {
			err = f.worker.dynamicDeleteApp(context.Background(), f.app)
		}
		cleanupDone <- err
	}()
	select {
	case err := <-cleanupDone:
		t.Fatalf("cleanup raced an in-flight write: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(blocked.release)
	dynamicWantStatus(t, <-requestDone, 201)
	if err := <-cleanupDone; err != nil {
		t.Fatal(err)
	}
	f.store.mu.Lock()
	count := len(f.store.records[dynamicPK(f.app, "public")])
	f.store.mu.Unlock()
	if count != 0 {
		t.Fatal("cleanup left a late visitor record")
	}
	dynamicWantStatus(t, f.request(t, "GET", "/apps/"+f.app+"/config", "", "", nil, ""), 404)
	f.worker.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := f.worker.dynamicLock(ctx)
	f.worker.mu.Unlock()
	if err == nil {
		t.Fatal("canceled operations can wait indefinitely for the worker lock")
	}
}
