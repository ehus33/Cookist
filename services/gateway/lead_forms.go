package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"mime"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type submissionRate struct {
	Started time.Time
	Count   int
}

type leadSubmission struct {
	ID        string `json:"id"`
	FormID    string `json:"formId"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	Message   string `json:"message"`
	CreatedAt string `json:"createdAt"`
	Consent   bool   `json:"consent"`
}

func validFormOrigin(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil &&
		u.Path == "" && u.RawQuery == "" && u.Fragment == "" && !strings.ContainsAny(value, "\\ \r\n\t")
}

func hasLeadForm(blocks []pageBlock) bool {
	for _, block := range blocks {
		if block.Type == "contact" || block.Type == "waitlist" {
			return true
		}
	}
	return false
}

func (w *deploymentWorker) formStorageError(rw http.ResponseWriter, err error) {
	log.Printf("lead form storage failed: %v", err)
	jsonError(rw, 500, "storage_error", "Unable to access submissions. Please try again.")
}

func (w *deploymentWorker) receiveSubmission(rw http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/forms/"), "/")
	if len(parts) != 2 || !idPattern.MatchString(parts[0]) || !pageBlockIDPattern.MatchString(parts[1]) || r.URL.RawQuery != "" {
		jsonError(rw, 404, "not_found", "Form not found.")
		return
	}
	if r.Method != http.MethodPost {
		rw.Header().Set("Allow", "POST")
		jsonError(rw, 405, "method_not_allowed", "Use POST.")
		return
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/x-www-form-urlencoded" {
		jsonError(rw, 415, "invalid_request", "Submit this form from the published site.")
		return
	}
	r.Body = http.MaxBytesReader(rw, r.Body, 8192)
	if err := r.ParseForm(); err != nil {
		jsonError(rw, 400, "invalid_request", "The form is invalid or too large.")
		return
	}
	for key, values := range r.PostForm {
		if (key != "name" && key != "email" && key != "message" && key != "website" && key != "consent") || len(values) != 1 {
			jsonError(rw, 400, "invalid_request", "The form contains invalid fields.")
			return
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	dir := filepath.Join(w.root, parts[0])
	d, err := readDeployment(dir, parts[0])
	if errors.Is(err, os.ErrNotExist) {
		jsonError(rw, 404, "not_found", "Form not found.")
		return
	}
	if err != nil {
		w.formStorageError(rw, err)
		return
	}
	if d.Status != "ready" || !websiteURLPattern.MatchString(d.URL) || r.Header.Get("Origin") != d.URL {
		jsonError(rw, 403, "origin_not_allowed", "Submit this form from the published site.")
		return
	}
	data, err := os.ReadFile(filepath.Join(dir, "design.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			jsonError(rw, 404, "not_found", "Form not found.")
		} else {
			w.formStorageError(rw, err)
		}
		return
	}
	var design deploymentRequest
	if err := json.Unmarshal(data, &design); err != nil {
		w.formStorageError(rw, err)
		return
	}
	if err := validateDeployment(design); err != nil {
		w.formStorageError(rw, err)
		return
	}
	kind := ""
	for _, block := range design.Blocks {
		if block.ID == parts[1] && (block.Type == "contact" || block.Type == "waitlist") {
			kind = block.Type
		}
	}
	if kind == "" {
		jsonError(rw, 404, "not_found", "Form not found.")
		return
	}
	name, email, message := strings.TrimSpace(r.PostForm.Get("name")), strings.TrimSpace(r.PostForm.Get("email")), strings.TrimSpace(r.PostForm.Get("message"))
	address, emailErr := mail.ParseAddress(email)
	if name == "" || len(name) > 160 || len(email) > 254 || emailErr != nil || address.Address != email ||
		len(message) > 2000 || (kind == "contact" && message == "") || (kind == "waitlist" && message != "") ||
		r.PostForm.Get("consent") != "yes" || r.PostForm.Get("website") != "" {
		jsonError(rw, 400, "invalid_request", "Enter a name, valid email, required message, and contact consent.")
		return
	}
	now := time.Now()
	if w.formRates == nil {
		w.formRates = make(map[string]submissionRate)
	}
	for key, rate := range w.formRates {
		if now.Sub(rate.Started) >= time.Minute {
			delete(w.formRates, key)
		}
	}
	key := parts[0] + "/" + parts[1]
	rate := w.formRates[key]
	if rate.Count >= 30 {
		rw.Header().Set("Retry-After", "60")
		jsonError(rw, 429, "rate_limited", "This form is receiving too many submissions. Try again in a minute.")
		return
	}
	if rate.Count == 0 {
		rate.Started = now
	}
	rate.Count++
	w.formRates[key] = rate
	inbox := filepath.Join(dir, "submissions")
	if err := os.MkdirAll(inbox, 0700); err != nil {
		w.formStorageError(rw, err)
		return
	}
	entries, err := os.ReadDir(inbox)
	if err != nil {
		w.formStorageError(rw, err)
		return
	}
	if len(entries) >= 1000 {
		jsonError(rw, 429, "inbox_full", "This site's inbox is full. Please contact the business another way.")
		return
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		w.formStorageError(rw, err)
		return
	}
	entry := leadSubmission{ID: hex.EncodeToString(random[:]), FormID: parts[1], Kind: kind, Name: name,
		Email: email, Message: message, Consent: true, CreatedAt: now.UTC().Format("2006-01-02T15:04:05.000000000Z")}
	data, err = json.Marshal(entry)
	if err == nil {
		var file *os.File
		file, err = os.OpenFile(filepath.Join(inbox, entry.ID+".json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			_, err = file.Write(data)
			closeErr := file.Close()
			if err == nil {
				err = closeErr
			}
		}
	}
	if err != nil {
		w.formStorageError(rw, err)
		return
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'")
	rw.WriteHeader(http.StatusCreated)
	fmt.Fprintf(rw, `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Submission received</title><h1>Thank you</h1><p>Your submission was saved for this business.</p><a href="%s">Back to site</a></html>`, html.EscapeString(d.URL))
}

func (w *deploymentWorker) submissionsRoute(rw http.ResponseWriter, r *http.Request, access deploymentAccess) bool {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/deployments/"), "/")
	if !strings.HasPrefix(r.URL.Path, "/api/deployments/") || len(parts) < 2 || parts[1] != "submissions" {
		return false
	}
	if len(parts) > 3 || !idPattern.MatchString(parts[0]) || (len(parts) == 3 && !idPattern.MatchString(parts[2])) {
		jsonError(rw, 404, "not_found", "Submission not found.")
		return true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	dir := filepath.Join(w.root, parts[0])
	d, err := readDeployment(dir, parts[0])
	if err == nil {
		d, err = w.ownedDeployment(d)
	}
	if errors.Is(err, os.ErrNotExist) {
		jsonError(rw, 404, "not_found", "Deployment not found.")
		return true
	}
	if err != nil {
		w.formStorageError(rw, err)
		return true
	}
	if err := access.check("delete", &d); err != nil {
		var apiErr *deploymentError
		if errors.As(err, &apiErr) {
			jsonError(rw, apiErr.status, apiErr.code, apiErr.message)
		}
		return true
	}
	inbox := filepath.Join(dir, "submissions")
	if r.Method == http.MethodDelete && len(parts) == 3 {
		if err := os.Remove(filepath.Join(inbox, parts[2]+".json")); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				jsonError(rw, 404, "not_found", "Submission not found.")
			} else {
				w.formStorageError(rw, err)
			}
			return true
		}
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(map[string]bool{"deleted": true})
		return true
	}
	if r.Method != http.MethodGet || len(parts) != 2 {
		jsonError(rw, 405, "method_not_allowed", "Use GET for the inbox or DELETE for a submission.")
		return true
	}
	entries, err := os.ReadDir(inbox)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		w.formStorageError(rw, err)
		return true
	}
	if len(entries) > 1000 {
		w.formStorageError(rw, errors.New("submission inbox exceeds the configured bound"))
		return true
	}
	result := make([]leadSubmission, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || !idPattern.MatchString(strings.TrimSuffix(entry.Name(), ".json")) {
			w.formStorageError(rw, errors.New("unexpected submission storage entry"))
			return true
		}
		data, err := os.ReadFile(filepath.Join(inbox, entry.Name()))
		var lead leadSubmission
		if err == nil {
			err = json.Unmarshal(data, &lead)
		}
		if err != nil {
			w.formStorageError(rw, err)
			return true
		}
		if lead.ID != strings.TrimSuffix(entry.Name(), ".json") {
			w.formStorageError(rw, errors.New("submission identity does not match its stored entry"))
			return true
		}
		result = append(result, lead)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt > result[j].CreatedAt })
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(rw).Encode(result)
	return true
}
