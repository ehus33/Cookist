package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"mime"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/golang-jwt/jwt/v5"
)

type dynamicConfig struct {
	Collections []dynamicCollection `json:"collections"`
}

type dynamicCollection struct {
	ID     string         `json:"id"`
	Name   string         `json:"name"`
	Fields []dynamicField `json:"fields"`
	Read   string         `json:"read"`
	Create string         `json:"create"`
	Update string         `json:"update"`
	Delete string         `json:"delete"`
}

type dynamicField struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

func (c *dynamicConfig) UnmarshalJSON(data []byte) error {
	type plain dynamicConfig
	var value plain
	if err := dynamicDecodeJSON(data, []string{"collections"}, &value); err != nil {
		return err
	}
	*c = dynamicConfig(value)
	return nil
}

func (c *dynamicCollection) UnmarshalJSON(data []byte) error {
	type plain dynamicCollection
	var value plain
	if err := dynamicDecodeJSON(data, []string{"id", "name", "fields", "read", "create", "update", "delete"}, &value); err != nil {
		return err
	}
	*c = dynamicCollection(value)
	return nil
}

func (f *dynamicField) UnmarshalJSON(data []byte) error {
	type plain dynamicField
	var value plain
	if err := dynamicDecodeJSON(data, []string{"id", "label", "type", "required"}, &value); err != nil {
		return err
	}
	*f = dynamicField(value)
	return nil
}

var dynamicIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
var dynamicClientPattern = regexp.MustCompile(`^[A-Za-z0-9]{1,128}$`)
var dynamicTablePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,255}$`)

func dynamicDisplayName(s string) bool {
	return utf8.ValidString(s) && strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= 80 &&
		!strings.ContainsAny(s, "\x00\r\n")
}

func validateDynamicConfig(cfg *dynamicConfig) error {
	if cfg == nil {
		return nil
	}
	if len(cfg.Collections) < 1 || len(cfg.Collections) > 8 {
		return errors.New("Dynamic apps require between 1 and 8 collections.")
	}
	ids := map[string]bool{}
	for _, c := range cfg.Collections {
		if !dynamicIDPattern.MatchString(c.ID) || ids[c.ID] || !dynamicDisplayName(c.Name) {
			return errors.New("Collections require unique valid IDs and names of 1–80 characters.")
		}
		ids[c.ID] = true
		if len(c.Fields) < 1 || len(c.Fields) > 12 {
			return errors.New("Collections require between 1 and 12 fields.")
		}
		if (c.Read != "public" && c.Read != "members" && c.Read != "own") ||
			(c.Create != "members" && c.Create != "none") ||
			(c.Update != "own" && c.Update != "none") || (c.Delete != "own" && c.Delete != "none") {
			return errors.New("Invalid collection permissions.")
		}
		fields := map[string]bool{}
		for _, f := range c.Fields {
			if !dynamicIDPattern.MatchString(f.ID) || fields[f.ID] || !dynamicDisplayName(f.Label) {
				return errors.New("Fields require unique valid IDs and labels of 1–80 characters.")
			}
			fields[f.ID] = true
			if f.Type != "text" && f.Type != "email" && f.Type != "number" && f.Type != "boolean" {
				return errors.New("Unsupported collection field type.")
			}
		}
	}
	return nil
}

func validateDynamicBlocks(request deploymentRequest) error {
	if err := validateDynamicConfig(request.Dynamic); err != nil {
		return err
	}
	collections := map[string]bool{}
	if request.Dynamic != nil {
		for _, c := range request.Dynamic.Collections {
			collections[c.ID] = true
		}
	}
	for _, b := range request.Blocks {
		switch b.Type {
		case "data-list", "data-form":
			if !collections[b.Collection] {
				return errors.New("Data elements must reference an enabled collection.")
			}
		case "member-account":
			if request.Dynamic == nil {
				return errors.New("Member accounts require an enabled dynamic app.")
			}
		default:
			if b.Collection != "" {
				return errors.New("Only data elements can reference a collection.")
			}
		}
	}
	return nil
}

// Published collections are immutable, even when empty, avoiding a schema/data
// race with visitor writes. Only appending entirely new collections is supported.
func validateDynamicUpdate(old, next *dynamicConfig) error {
	if err := validateDynamicConfig(next); err != nil {
		return err
	}
	if old == nil {
		return nil
	}
	if next == nil || len(next.Collections) < len(old.Collections) {
		return errors.New("Existing dynamic collections cannot be removed or disabled.")
	}
	for i, c := range old.Collections {
		if !reflect.DeepEqual(c, next.Collections[i]) {
			return errors.New("Existing dynamic collections, fields and permissions are immutable. Append a new collection instead.")
		}
	}
	return nil
}

type dynamicRate struct {
	start        time.Time
	auth, writes int
}

type dynamicService struct {
	store                    dynamicStore
	region, poolID, clientID string
	issuer, endpoint         string
	client                   *http.Client
	keyMu                    sync.Mutex
	keys                     map[string]*rsa.PublicKey
	keysUntil, lastFetch     time.Time
	rateMu                   sync.Mutex
	rates                    map[string]dynamicRate
}

func newDynamicService(ctx context.Context, region, table, poolID, clientID string) (*dynamicService, error) {
	if table == "" && poolID == "" && clientID == "" {
		return nil, nil
	}
	if !regionPattern.MatchString(region) || !dynamicTablePattern.MatchString(table) ||
		!strings.HasPrefix(poolID, region+"_") || !dynamicClientPattern.MatchString(strings.TrimPrefix(poolID, region+"_")) ||
		!dynamicClientPattern.MatchString(clientID) {
		return nil, errors.New("dynamic apps require a region, DynamoDB table, dedicated visitor pool ID and public client ID together")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("load dynamic AWS configuration: %w", err)
	}
	endpoint := "https://cognito-idp." + region + ".amazonaws.com"
	if strings.HasPrefix(region, "cn-") {
		endpoint += ".cn"
	}
	return &dynamicService{
		store:  &dynamoDynamicStore{client: dynamodb.NewFromConfig(cfg), table: table},
		region: region, poolID: poolID, clientID: clientID, issuer: endpoint + "/" + poolID, endpoint: endpoint,
		client: &http.Client{Timeout: 6 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		keys:   map[string]*rsa.PublicKey{}, rates: map[string]dynamicRate{},
	}, nil
}

func (s *dynamicService) allow(app string, auth bool) bool {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	now := time.Now()
	if s.rates == nil {
		s.rates = map[string]dynamicRate{}
	}
	r, found := s.rates[app]
	if !found || now.Sub(r.start) >= time.Minute {
		for id, rate := range s.rates {
			if now.Sub(rate.start) >= time.Minute {
				delete(s.rates, id)
			}
		}
		if len(s.rates) >= 1024 {
			return false
		}
		r = dynamicRate{start: now}
	}
	if auth {
		if r.auth >= 30 {
			return false
		}
		r.auth++
	} else {
		if r.writes >= 120 {
			return false
		}
		r.writes++
	}
	s.rates[app] = r
	return true
}

type dynamicClaims struct {
	jwt.RegisteredClaims
	TokenUse string `json:"token_use"`
	ClientID string `json:"client_id"`
}

func (s *dynamicService) signingKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	now := time.Now()
	if key := s.keys[kid]; key != nil && now.Before(s.keysUntil) {
		return key, nil
	}
	// Unknown key IDs cannot turn token failures into unbounded JWKS requests.
	if !s.lastFetch.IsZero() && now.Sub(s.lastFetch) < time.Minute {
		return nil, errors.New("visitor signing key unavailable")
	}
	s.lastFetch = now
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.issuer+"/.well-known/jwks.json", nil)
	if err != nil {
		return nil, &deploymentError{503, "visitor_auth_unavailable", "Visitor signing keys unavailable. Retry later."}
	}
	res, err := s.client.Do(req)
	if err != nil {
		return nil, &deploymentError{503, "visitor_auth_unavailable", "Visitor signing keys unavailable. Retry later."}
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, &deploymentError{503, "visitor_auth_unavailable", "Visitor signing keys unavailable. Retry later."}
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 65537))
	if err != nil || len(data) > 65536 {
		return nil, errors.New("invalid visitor signing keys")
	}
	var jwks struct {
		Keys []struct{ Kid, Kty, Alg, Use, N, E string }
	}
	if err := json.Unmarshal(data, &jwks); err != nil || len(jwks.Keys) > 16 {
		return nil, errors.New("invalid visitor signing keys")
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" || k.Alg != "RS256" || k.Use != "sig" || k.Kid == "" || len(k.Kid) > 256 {
			continue
		}
		n, nErr := base64.RawURLEncoding.DecodeString(k.N)
		e, eErr := base64.RawURLEncoding.DecodeString(k.E)
		if nErr != nil || eErr != nil || len(n) < 256 || len(n) > 512 || len(e) < 1 || len(e) > 4 {
			continue
		}
		exponent := new(big.Int).SetBytes(e).Int64()
		if exponent < 3 || exponent > math.MaxInt32 || exponent%2 == 0 || keys[k.Kid] != nil {
			return nil, errors.New("invalid visitor signing keys")
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(exponent)}
	}
	s.keys, s.keysUntil = keys, now.Add(time.Hour)
	if keys[kid] == nil {
		return nil, errors.New("visitor signing key unavailable")
	}
	return keys[kid], nil
}

func (s *dynamicService) verifyToken(ctx context.Context, raw string) (string, error) {
	if raw == "" || len(raw) > 16384 {
		return "", errors.New("invalid visitor token")
	}
	claims := &dynamicClaims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		kid, ok := t.Header["kid"].(string)
		if !ok || kid == "" || len(kid) > 256 {
			return nil, errors.New("invalid visitor signing key")
		}
		return s.signingKey(ctx, kid)
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(s.issuer), jwt.WithExpirationRequired(),
		jwt.WithJSONNumber(), jwt.WithIssuedAt())
	if err != nil || !token.Valid || claims.TokenUse != "access" || claims.ClientID != s.clientID ||
		claims.Subject == "" || len(claims.Subject) > 128 {
		var de *deploymentError
		if errors.As(err, &de) {
			return "", de
		}
		return "", errors.New("invalid or expired visitor access token")
	}
	return claims.Subject, nil
}

func (s *dynamicService) visitor(ctx context.Context, r *http.Request) (string, error) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", nil
	}
	if !strings.HasPrefix(header, "Bearer ") {
		return "", &deploymentError{401, "visitor_unauthorized", "A valid visitor access token is required."}
	}
	sub, err := s.verifyToken(ctx, strings.TrimPrefix(header, "Bearer "))
	if err != nil {
		var de *deploymentError
		if errors.As(err, &de) {
			return "", de
		}
		return "", &deploymentError{401, "visitor_unauthorized", "A valid visitor access token is required."}
	}
	// Cognito revocations are not represented in JWT signatures. GetUser rejects
	// globally signed-out, revoked and disabled visitor sessions without retaining
	// bearer tokens in this service.
	if err := s.activeVisitor(ctx, strings.TrimPrefix(header, "Bearer "), sub); err != nil {
		return "", err
	}
	return sub, nil
}

func (s *dynamicService) activeVisitor(ctx context.Context, token, sub string) error {
	data, _ := json.Marshal(map[string]string{"AccessToken": token})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(data))
	if err != nil {
		return &deploymentError{503, "visitor_auth_unavailable", "Visitor authentication unavailable. Retry later."}
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AWSCognitoIdentityProviderService.GetUser")
	res, err := s.client.Do(req)
	if err != nil {
		return &deploymentError{503, "visitor_auth_unavailable", "Visitor authentication unavailable. Retry later."}
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 65537))
	if err != nil || len(body) > 65536 {
		return &deploymentError{503, "visitor_auth_unavailable", "Visitor authentication unavailable. Retry later."}
	}
	if res.StatusCode != 200 {
		var failure struct {
			Type string `json:"__type"`
		}
		_ = json.Unmarshal(body, &failure)
		if strings.HasSuffix(failure.Type, "NotAuthorizedException") || strings.HasSuffix(failure.Type, "UserNotFoundException") {
			return &deploymentError{401, "visitor_unauthorized", "Visitor session expired or was revoked. Sign in again."}
		}
		if strings.HasSuffix(failure.Type, "TooManyRequestsException") {
			return &deploymentError{429, "rate_limited", "Visitor authentication limit reached. Retry later."}
		}
		return &deploymentError{503, "visitor_auth_unavailable", "Visitor authentication unavailable. Retry later."}
	}
	var result struct {
		UserAttributes []struct{ Name, Value string }
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return &deploymentError{503, "visitor_auth_unavailable", "Visitor authentication unavailable. Retry later."}
	}
	for _, attr := range result.UserAttributes {
		if attr.Name == "sub" && attr.Value == sub {
			return nil
		}
	}
	return &deploymentError{401, "visitor_unauthorized", "Invalid visitor session."}
}

func dynamicCheckJSON(dec *json.Decoder, depth int) error {
	if depth > 8 {
		return errors.New("JSON nesting exceeds the limit")
	}
	t, err := dec.Token()
	if err != nil {
		return err
	}
	if t == nil {
		return errors.New("null values are not supported")
	}
	d, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch d {
	case '{':
		keys := map[string]bool{}
		for dec.More() {
			t, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := t.(string)
			if !ok || keys[key] {
				return errors.New("duplicate JSON key")
			}
			keys[key] = true
			if err := dynamicCheckJSON(dec, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := dynamicCheckJSON(dec, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid JSON")
	}
	_, err = dec.Token()
	return err
}

func dynamicDecodeJSON(data []byte, allowed []string, dst any) error {
	if len(data) == 0 || len(data) > 65536 || !utf8.Valid(data) {
		return errors.New("JSON must be valid UTF-8 and at most 64 KiB")
	}
	check := json.NewDecoder(bytes.NewReader(data))
	check.UseNumber()
	if err := dynamicCheckJSON(check, 0); err != nil {
		return err
	}
	if _, err := check.Token(); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return errors.New("JSON object required")
	}
	for k := range fields {
		found := false
		for _, a := range allowed {
			found = found || k == a
		}
		if !found {
			return errors.New("unknown JSON field")
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func dynamicReadJSON(r *http.Request, allowed []string, dst any) error {
	if contentType := r.Header.Get("Content-Type"); contentType != "" {
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil || mediaType != "application/json" {
			return &deploymentError{415, "invalid_content_type", "Use application/json."}
		}
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 65537))
	if err != nil {
		return &deploymentError{400, "invalid_json", "Could not read request JSON."}
	}
	if len(data) > 65536 {
		return &deploymentError{413, "request_too_large", "Request exceeds 64 KiB."}
	}
	if err := dynamicDecodeJSON(data, allowed, dst); err != nil {
		return &deploymentError{400, "invalid_json", "Use a bounded JSON object without unknown, duplicate or trailing fields."}
	}
	return nil
}

func validateDynamicValues(c dynamicCollection, values map[string]any) error {
	if values == nil || len(values) > len(c.Fields) {
		return errors.New("values must be an object containing only configured fields")
	}
	fields := map[string]dynamicField{}
	for _, f := range c.Fields {
		fields[f.ID] = f
		value, exists := values[f.ID]
		if f.Required && (!exists || value == nil || (f.Type != "boolean" && f.Type != "number" && strings.TrimSpace(fmt.Sprint(value)) == "")) {
			return errors.New("required field missing")
		}
	}
	for id, value := range values {
		f, ok := fields[id]
		if !ok || value == nil {
			return errors.New("unknown or null field")
		}
		switch f.Type {
		case "text", "email":
			text, ok := value.(string)
			if !ok || !utf8.ValidString(text) || utf8.RuneCountInString(text) > 2000 {
				return errors.New("invalid text field")
			}
			if f.Type == "email" && text != "" {
				addr, err := mail.ParseAddress(text)
				if err != nil || addr.Address != text || len(text) > 254 {
					return errors.New("invalid email field")
				}
			}
		case "number":
			number, ok := value.(json.Number)
			if !ok {
				return errors.New("invalid number field")
			}
			v, err := number.Float64()
			if err != nil || math.IsInf(v, 0) || math.IsNaN(v) {
				return errors.New("invalid number field")
			}
		case "boolean":
			if _, ok := value.(bool); !ok {
				return errors.New("invalid boolean field")
			}
		}
	}
	return nil
}

func dynamicReply(rw http.ResponseWriter, status int, value any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("X-Content-Type-Options", "nosniff")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(value)
}

func dynamicError(rw http.ResponseWriter, err error) {
	var de *deploymentError
	if !errors.As(err, &de) {
		de = &deploymentError{503, "dynamic_unavailable", "Dynamic service unavailable. Retry later."}
	}
	if de.status == 429 {
		rw.Header().Set("Retry-After", "60")
	}
	jsonError(rw, de.status, de.code, de.message)
}

func dynamicNotFound(rw http.ResponseWriter) {
	jsonError(rw, 404, "not_found", "Dynamic app or collection not found.")
}

func (w *deploymentWorker) dynamicLock(ctx context.Context) error {
	if ctx.Err() != nil {
		return &deploymentError{503, "dynamic_busy", "Dynamic app is busy. Retry later."}
	}
	if w.mu.TryLock() {
		return nil
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return &deploymentError{503, "dynamic_busy", "Dynamic app is busy. Retry later."}
		case <-ticker.C:
			if w.mu.TryLock() {
				return nil
			}
		}
	}
}

func (w *deploymentWorker) dynamicApp(id string) (deployment, *dynamicConfig, error) {
	if !idPattern.MatchString(id) {
		return deployment{}, nil, errDynamicNotFound
	}
	dir := filepath.Join(w.root, id)
	d, err := readDeployment(dir, id)
	if err != nil {
		return d, nil, errDynamicNotFound
	}
	d, err = w.ownedDeployment(d)
	if err != nil || d.Status != "ready" || !websiteURLPattern.MatchString(d.URL) {
		return d, nil, errDynamicNotFound
	}
	req, err := readBuilderDesign(dir)
	if err != nil || req.Dynamic == nil || validateDynamicBlocks(req) != nil {
		return d, nil, errDynamicNotFound
	}
	return d, req.Dynamic, nil
}

func (w *deploymentWorker) dynamicPublicRoute(rw http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/apps/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/apps/"), "/")
	if len(parts) < 2 || !idPattern.MatchString(parts[0]) {
		dynamicNotFound(rw)
		return true
	}
	if w.dynamic == nil {
		jsonError(rw, 503, "dynamic_disabled", "Dynamic apps are not configured.")
		return true
	}
	// Deployment updates/deletion use this same mutex. Hold it through storage
	// operations so no in-flight write can outlive the transition to deleting.
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	if err := w.dynamicLock(ctx); err != nil {
		dynamicError(rw, err)
		return true
	}
	defer w.mu.Unlock()
	d, cfg, err := w.dynamicApp(parts[0])
	if err != nil {
		dynamicNotFound(rw)
		return true
	}
	rw.Header().Add("Vary", "Origin")
	if r.Header.Get("Origin") != d.URL {
		jsonError(rw, 403, "origin_forbidden", "Use the published app origin.")
		return true
	}
	rw.Header().Set("Access-Control-Allow-Origin", d.URL)
	if r.Method == http.MethodOptions {
		rw.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		rw.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, If-Match")
		rw.Header().Set("Access-Control-Max-Age", "300")
		rw.WriteHeader(http.StatusNoContent)
		return true
	}
	ctx = withDatabaseTenant(ctx, d.TenantID)
	r = r.WithContext(ctx)
	if !(len(parts) == 3 && parts[1] == "auth" && parts[2] == "logout") && !w.requireDatabase(rw, d.TenantID) {
		return true
	}
	switch {
	case len(parts) == 2 && parts[1] == "config" && r.Method == http.MethodGet:
		dynamicReply(rw, 200, map[string]any{"collections": cfg.Collections, "authentication": true})
	case len(parts) == 3 && parts[1] == "auth" &&
		(parts[2] == "signup" || parts[2] == "confirm" || parts[2] == "login" ||
			parts[2] == "logout" || parts[2] == "forgot" || parts[2] == "reset"):
		w.dynamic.auth(rw, r, parts[0], parts[2])
	case len(parts) == 2 && parts[1] == "me" && r.Method == http.MethodGet:
		sub, err := w.dynamic.visitor(ctx, r)
		if err != nil {
			dynamicError(rw, err)
		} else if sub == "" {
			dynamicError(rw, &deploymentError{401, "visitor_unauthorized", "Sign in as an app visitor."})
		} else {
			dynamicReply(rw, 200, map[string]string{"id": sub})
		}
	case len(parts) >= 4 && parts[1] == "collections":
		w.dynamic.records(rw, r, parts[0], cfg, parts[2:], false, "")
	default:
		dynamicNotFound(rw)
	}
	return true
}

func (w *deploymentWorker) dynamicAdminRoute(rw http.ResponseWriter, r *http.Request, access deploymentAccess) bool {
	const prefix = "/api/deployments/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, prefix), "/")
	if len(parts) < 2 || parts[1] != "collections" {
		return false
	}
	if len(parts) < 4 || !idPattern.MatchString(parts[0]) {
		dynamicNotFound(rw)
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	if err := w.dynamicLock(ctx); err != nil {
		dynamicError(rw, err)
		return true
	}
	defer w.mu.Unlock()
	// Tenant authorization precedes the disabled/config checks to avoid disclosing
	// the existence or dynamic status of another tenant's deployment.
	d, err := readDeployment(filepath.Join(w.root, parts[0]), parts[0])
	if err == nil {
		d, err = w.ownedDeployment(d)
	}
	if err != nil {
		dynamicNotFound(rw)
		return true
	}
	if err := access.check("delete", &d); err != nil {
		dynamicError(rw, err)
		return true
	}
	if w.dynamic == nil {
		jsonError(rw, 503, "dynamic_disabled", "Dynamic apps are not configured.")
		return true
	}
	_, cfg, err := w.dynamicApp(parts[0])
	if err != nil {
		dynamicNotFound(rw)
		return true
	}
	if access.Role != "platform_owner" && r.Method != http.MethodGet && r.Method != http.MethodDelete && !w.requireDatabase(rw, d.TenantID) {
		return true
	}
	w.dynamic.records(rw, r.WithContext(withDatabaseTenant(ctx, d.TenantID)), parts[0], cfg, parts[2:], true, access.ActorID)
	return true
}

func (s *dynamicService) auth(rw http.ResponseWriter, r *http.Request, app, action string) {
	if r.Method != http.MethodPost {
		jsonError(rw, 405, "method_not_allowed", "Use POST for visitor authentication.")
		return
	}
	if !s.allow(app, true) {
		rw.Header().Set("Retry-After", "60")
		jsonError(rw, 429, "rate_limited", "Visitor authentication limit reached. Retry later.")
		return
	}
	var input struct{ Email, Password, Code string }
	fields := []string{"email", "password"}
	if action == "confirm" {
		fields = []string{"email", "code"}
	} else if action == "forgot" {
		fields = []string{"email"}
	} else if action == "reset" {
		fields = []string{"email", "code", "password"}
	} else if action == "logout" {
		fields = []string{}
	}
	if action != "logout" {
		if err := dynamicReadJSON(r, fields, &input); err != nil {
			dynamicError(rw, err)
			return
		}
	}
	if action == "logout" && r.ContentLength != 0 {
		var empty struct{}
		if err := dynamicReadJSON(r, nil, &empty); err != nil {
			dynamicError(rw, err)
			return
		}
	}
	if action == "logout" {
		sub, err := s.visitor(r.Context(), r)
		if err != nil {
			dynamicError(rw, err)
			return
		}
		if sub == "" {
			dynamicError(rw, &deploymentError{401, "visitor_unauthorized", "Sign in as an app visitor."})
			return
		}
	}
	address, err := mail.ParseAddress(input.Email)
	if action != "logout" && (err != nil || address.Address != input.Email || len(input.Email) > 254 ||
		((action == "signup" || action == "login" || action == "reset") && (len(input.Password) < 8 || len(input.Password) > 256)) ||
		((action == "confirm" || action == "reset") && (len(input.Code) < 1 || len(input.Code) > 64))) {
		jsonError(rw, 400, "invalid_auth_request", "Use a valid email and bounded password or confirmation code.")
		return
	}
	target := ""
	payload := map[string]any{"ClientId": s.clientID}
	switch action {
	case "logout":
		target = "GlobalSignOut"
		payload = map[string]any{"AccessToken": strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")}
	case "forgot":
		target = "ForgotPassword"
		payload["Username"] = input.Email
	case "reset":
		target = "ConfirmForgotPassword"
		payload["Username"], payload["ConfirmationCode"], payload["Password"] = input.Email, input.Code, input.Password
	case "signup":
		target = "SignUp"
		payload["Username"], payload["Password"] = input.Email, input.Password
		payload["UserAttributes"] = []map[string]string{{"Name": "email", "Value": input.Email}}
	case "confirm":
		target = "ConfirmSignUp"
		payload["Username"], payload["ConfirmationCode"] = input.Email, input.Code
	case "login":
		target = "InitiateAuth"
		payload["AuthFlow"] = "USER_PASSWORD_AUTH"
		payload["AuthParameters"] = map[string]string{"USERNAME": input.Email, "PASSWORD": input.Password}
	}
	data, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, s.endpoint, bytes.NewReader(data))
	if err != nil {
		dynamicError(rw, err)
		return
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AWSCognitoIdentityProviderService."+target)
	res, err := s.client.Do(req)
	if err != nil {
		dynamicError(rw, err)
		return
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 65537))
	if err != nil || len(body) > 65536 {
		dynamicError(rw, errors.New("invalid visitor auth response"))
		return
	}
	var result struct {
		Type                 string `json:"__type"`
		UserConfirmed        bool
		ChallengeName        string
		AuthenticationResult struct {
			AccessToken string
			ExpiresIn   int
		}
	}
	if err := json.Unmarshal(body, &result); err != nil {
		dynamicError(rw, err)
		return
	}
	if res.StatusCode != 200 {
		code := result.Type
		if i := strings.LastIndex(code, "#"); i >= 0 {
			code = code[i+1:]
		}
		status, message := 400, "Visitor authentication failed."
		switch code {
		case "NotAuthorizedException", "UserNotFoundException":
			status, code, message = 401, "invalid_credentials", "Email or password is incorrect."
		case "UserNotConfirmedException":
			code, message = "confirmation_required", "Confirm your email before signing in."
		case "UsernameExistsException":
			code, message = "account_exists", "An account already exists. Sign in or confirm your email."
		case "CodeMismatchException", "ExpiredCodeException":
			code, message = "invalid_confirmation", "Confirmation code is incorrect or expired."
		case "InvalidPasswordException", "InvalidParameterException":
			code, message = "invalid_auth_request", "Email, password or confirmation code is invalid."
		case "TooManyRequestsException", "LimitExceededException":
			status, code, message = 429, "rate_limited", "Visitor authentication limit reached. Retry later."
		default:
			status, code, message = 503, "visitor_auth_unavailable", "Visitor authentication unavailable. Retry later."
		}
		jsonError(rw, status, code, message)
		return
	}
	switch action {
	case "forgot":
		dynamicReply(rw, 200, map[string]bool{"confirmationRequired": true})
	case "reset":
		dynamicReply(rw, 200, map[string]bool{"reset": true})
	case "logout":
		dynamicReply(rw, 200, map[string]bool{"signedOut": true})
	case "signup":
		dynamicReply(rw, 200, map[string]bool{"confirmationRequired": !result.UserConfirmed})
	case "confirm":
		dynamicReply(rw, 200, map[string]bool{"confirmed": true})
	case "login":
		if result.ChallengeName != "" {
			jsonError(rw, 409, "unsupported_auth_challenge", "This visitor pool requires an unsupported sign-in challenge.")
			return
		}
		if result.AuthenticationResult.AccessToken == "" || result.AuthenticationResult.ExpiresIn < 1 {
			dynamicError(rw, errors.New("missing visitor access token"))
			return
		}
		if _, err := s.verifyToken(r.Context(), result.AuthenticationResult.AccessToken); err != nil {
			dynamicError(rw, errors.New("invalid visitor access token response"))
			return
		}
		dynamicReply(rw, 200, map[string]any{
			"accessToken": result.AuthenticationResult.AccessToken, "expiresIn": result.AuthenticationResult.ExpiresIn,
		})
	}
}

type dynamicCursor struct {
	App        string `json:"app"`
	Collection string `json:"collection"`
	Owner      string `json:"owner"`
	After      string `json:"after"`
}

func dynamicPage(r *http.Request, app, collection, owner string) (int, string, error) {
	query := r.URL.Query()
	for key, values := range query {
		if (key != "limit" && key != "cursor") || len(values) != 1 {
			return 0, "", errors.New("invalid pagination parameters")
		}
	}
	limit := 50
	if q := query.Get("limit"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 1 || n > 50 {
			return 0, "", errors.New("page size must be between 1 and 50")
		}
		limit = n
	}
	raw := query.Get("cursor")
	if raw == "" {
		return limit, "", nil
	}
	if len(raw) > 1024 {
		return 0, "", errors.New("invalid cursor")
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	var cursor dynamicCursor
	if err != nil || dynamicDecodeJSON(data, []string{"app", "collection", "owner", "after"}, &cursor) != nil ||
		cursor.App != app || cursor.Collection != collection || cursor.Owner != owner || !idPattern.MatchString(cursor.After) {
		return 0, "", errors.New("cursor does not match this collection and visitor")
	}
	return limit, cursor.After, nil
}

func (s *dynamicService) records(rw http.ResponseWriter, r *http.Request, app string, cfg *dynamicConfig, parts []string, admin bool, actor string) {
	if (len(parts) != 2 && len(parts) != 3) || parts[1] != "records" || !dynamicIDPattern.MatchString(parts[0]) ||
		(len(parts) == 3 && !idPattern.MatchString(parts[2])) {
		dynamicNotFound(rw)
		return
	}
	var c *dynamicCollection
	for i := range cfg.Collections {
		if cfg.Collections[i].ID == parts[0] {
			c = &cfg.Collections[i]
			break
		}
	}
	if c == nil {
		dynamicNotFound(rw)
		return
	}
	id := ""
	if len(parts) == 3 {
		id = parts[2]
	}
	if (id == "" && r.Method != http.MethodGet && r.Method != http.MethodPost) ||
		(id != "" && r.Method != http.MethodGet && r.Method != http.MethodPatch && r.Method != http.MethodDelete) {
		jsonError(rw, 405, "method_not_allowed", "Unsupported record method.")
		return
	}
	owner := ""
	if !admin {
		sub, err := s.visitor(r.Context(), r)
		if err != nil {
			dynamicError(rw, err)
			return
		}
		policy := c.Read
		switch r.Method {
		case http.MethodPost:
			policy = c.Create
		case http.MethodPatch:
			policy = c.Update
		case http.MethodDelete:
			policy = c.Delete
		}
		if policy == "none" {
			jsonError(rw, 403, "record_forbidden", "This collection does not permit this action.")
			return
		}
		if policy != "public" && sub == "" {
			jsonError(rw, 401, "visitor_unauthorized", "Sign in as an app visitor.")
			return
		}
		if policy == "own" {
			owner = sub
		}
		actor = sub
	}
	if r.Method != http.MethodGet && !s.allow(app, false) {
		rw.Header().Set("Retry-After", "60")
		jsonError(rw, 429, "rate_limited", "App write limit reached. Retry later.")
		return
	}
	if r.Method == http.MethodGet && id == "" {
		limit, after, err := dynamicPage(r, app, c.ID, owner)
		if err != nil {
			dynamicError(rw, &deploymentError{400, "invalid_cursor", err.Error()})
			return
		}
		records, next, err := s.store.List(r.Context(), app, c.ID, owner, after, limit)
		if err != nil {
			dynamicError(rw, dynamicStoreFailure(err))
			return
		}
		if records == nil {
			records = []dynamicRecord{}
		}
		for i := range records {
			dynamicRecordPermissions(&records[i], *c, actor, admin)
		}
		result := map[string]any{"records": records}
		if next != "" {
			data, _ := json.Marshal(dynamicCursor{app, c.ID, owner, next})
			result["next"] = base64.RawURLEncoding.EncodeToString(data)
		}
		dynamicReply(rw, 200, result)
		return
	}
	var record dynamicRecord
	if id != "" {
		var err error
		record, err = s.store.Get(r.Context(), app, c.ID, id)
		if err != nil {
			dynamicError(rw, dynamicStoreFailure(err))
			return
		}
		if owner != "" && record.Owner != owner {
			dynamicError(rw, dynamicStoreFailure(errDynamicNotFound))
			return
		}
		if r.Method == http.MethodGet {
			dynamicRecordPermissions(&record, *c, actor, admin)
			dynamicReply(rw, 200, map[string]any{"record": record})
			return
		}
	}
	if r.Method == http.MethodDelete {
		rawVersion := r.Header.Get("If-Match")
		version, err := strconv.ParseInt(rawVersion, 10, 64)
		if err != nil || version < 1 || strconv.FormatInt(version, 10) != rawVersion {
			jsonError(rw, 400, "invalid_version", "If-Match requires a positive numeric record version.")
			return
		}
		if err := s.store.Delete(r.Context(), app, c.ID, id, version, owner); err != nil {
			dynamicError(rw, dynamicStoreFailure(err))
			return
		}
		dynamicReply(rw, 200, map[string]bool{"deleted": true})
		return
	}
	var input struct {
		Values  map[string]any `json:"values"`
		Version int64          `json:"version"`
	}
	fields := []string{"values"}
	if r.Method == http.MethodPatch {
		fields = append(fields, "version")
	}
	if err := dynamicReadJSON(r, fields, &input); err != nil {
		dynamicError(rw, err)
		return
	}
	// PATCH merges fields; omitted required fields remain unchanged. Every resulting
	// value is revalidated against the saved schema before an atomic version check.
	values := input.Values
	if r.Method == http.MethodPatch {
		if input.Values == nil || input.Version < 1 || input.Version == math.MaxInt64 {
			jsonError(rw, 400, "invalid_version", "PATCH requires values and a positive record version.")
			return
		}
		values = make(map[string]any, len(record.Values)+len(input.Values))
		for k, v := range record.Values {
			values[k] = v
		}
		for k, v := range input.Values {
			values[k] = v
		}
	}
	if err := validateDynamicValues(*c, values); err != nil {
		jsonError(rw, 400, "invalid_values", err.Error())
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if r.Method == http.MethodPost {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			dynamicError(rw, err)
			return
		}
		if admin {
			actor = "admin:" + actor
		}
		record = dynamicRecord{ID: hex.EncodeToString(random[:]), Values: values, Version: 1, CreatedAt: now, UpdatedAt: now, Owner: actor}
		if err := s.store.Create(r.Context(), app, c.ID, record); err != nil {
			dynamicError(rw, dynamicStoreFailure(err))
			return
		}
		dynamicRecordPermissions(&record, *c, actor, admin)
		dynamicReply(rw, 201, map[string]any{"record": record})
		return
	}
	record.Values, record.Version, record.UpdatedAt = values, input.Version+1, now
	if err := s.store.Update(r.Context(), app, c.ID, record, input.Version, owner); err != nil {
		dynamicError(rw, dynamicStoreFailure(err))
		return
	}
	dynamicRecordPermissions(&record, *c, actor, admin)
	dynamicReply(rw, 200, map[string]any{"record": record})
}

func dynamicRecordPermissions(record *dynamicRecord, c dynamicCollection, actor string, admin bool) {
	record.CanUpdate = admin || (actor != "" && record.Owner == actor && c.Update == "own")
	record.CanDelete = admin || (actor != "" && record.Owner == actor && c.Delete == "own")
}

// Call while holding w.mu, after saving status=deleting, and before removing
// design.json/the deployment directory. On error retain state and retry cleanup.
// A 2-minute bound includes DynamoDB's bounded retry policy and batch backoff.
func (w *deploymentWorker) dynamicDeleteApp(ctx context.Context, id string) error {
	if !idPattern.MatchString(id) {
		return errors.New("invalid dynamic cleanup app ID")
	}
	dir := filepath.Join(w.root, id)
	d, err := readDeployment(dir, id)
	if err != nil {
		return err
	}
	if d.Status != "deleting" && d.Status != "cleanup_failed" {
		return errors.New("dynamic cleanup requires a stopped deployment")
	}
	design, err := readBuilderDesign(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil // A deployment without a saved design never enabled data access.
	}
	if err != nil {
		return errors.New("cannot safely read dynamic cleanup configuration")
	}
	if design.Dynamic == nil {
		return nil
	}
	if w.dynamic == nil {
		return errors.New("dynamic cleanup requires the configured managed database")
	}
	collections := make([]string, 0, len(design.Dynamic.Collections))
	for _, c := range design.Dynamic.Collections {
		collections = append(collections, c.ID)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := w.dynamic.store.DeleteApp(withDatabaseTenant(ctx, d.TenantID), id, collections); err != nil {
		return errors.New("managed dynamic data cleanup incomplete; retain deployment state and retry")
	}
	return nil
}
