package main

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func leadFixture(t *testing.T, w *deploymentWorker) deployment {
	t.Helper()
	d := deployment{ID: strings.Repeat("b", 32), Name: "Startup", Region: "us-west-2", Status: "ready", URL: "https://example.cloudfront.net", TenantID: "owner", CreatedBy: "owner"}
	dir := filepath.Join(w.root, d.ID)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveDeployment(dir, d); err != nil {
		t.Fatal(err)
	}
	req := deploymentRequest{Name: d.Name, Region: d.Region, Template: "builder", Style: "modern",
		Blocks: []pageBlock{{ID: "contact", Type: "contact", Text: "Get in touch", Align: "left", Size: "medium"},
			{ID: "waitlist", Type: "waitlist", Text: "Join us", Align: "left", Size: "medium"}}}
	if err := saveBuilderDesign(dir, req); err != nil {
		t.Fatal(err)
	}
	return d
}

func sendLead(w *deploymentWorker, d deployment, form, origin string, values url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/forms/"+d.ID+"/"+form, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", origin)
	rec := httptest.NewRecorder()
	w.receiveSubmission(rec, r)
	return rec
}

func TestLeadFormsPersistPrivateInboxAndDelete(t *testing.T) {
	w, _ := testWorker(t)
	d := leadFixture(t, w)
	values := url.Values{"name": {"Example visitor"}, "email": {"visitor@example.com"}, "message": {"Please send details"}, "consent": {"yes"}}
	if rec := sendLead(w, d, "contact", d.URL, values); rec.Code != 201 || !strings.Contains(rec.Body.String(), "Submission received") {
		t.Fatalf("submission failed: %d %s", rec.Code, rec.Body)
	}
	r := httptest.NewRequest("GET", "/api/deployments/"+d.ID+"/submissions", nil)
	r.Header.Set("Authorization", "Bearer owner")
	rec := httptest.NewRecorder()
	w.ServeHTTP(rec, r)
	var leads []leadSubmission
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &leads) != nil || len(leads) != 1 || leads[0].Message != "Please send details" {
		t.Fatalf("bad inbox: %d %s", rec.Code, rec.Body)
	}
	inaccessible := httptest.NewRecorder()
	w.submissionsRoute(inaccessible, r, deploymentAccess{ActorID: "outsider", TenantID: "other", Role: "customer_admin", CanDelete: true})
	if inaccessible.Code != 404 {
		t.Fatalf("cross-tenant inbox: %d", inaccessible.Code)
	}
	r = httptest.NewRequest("DELETE", "/api/deployments/"+d.ID+"/submissions/"+leads[0].ID, nil)
	r.Header.Set("Authorization", "Bearer owner")
	rec = httptest.NewRecorder()
	w.ServeHTTP(rec, r)
	if rec.Code != 200 {
		t.Fatalf("delete failed: %d %s", rec.Code, rec.Body)
	}
}

func TestLeadFormsRejectSpamInvalidFieldsAndRateLimit(t *testing.T) {
	w, _ := testWorker(t)
	d := leadFixture(t, w)
	for _, test := range []struct {
		form, origin, email, message, consent, trap string
		status                                      int
	}{
		{"contact", "https://attacker.example", "visitor@example.com", "Hi", "yes", "", 403},
		{"contact", d.URL, "not-an-email", "Hi", "yes", "", 400},
		{"contact", d.URL, "visitor@example.com", "", "yes", "", 400},
		{"waitlist", d.URL, "visitor@example.com", "", "", "", 400},
		{"waitlist", d.URL, "visitor@example.com", "", "yes", "bot", 400},
		{"missing", d.URL, "visitor@example.com", "", "yes", "", 404},
	} {
		rec := sendLead(w, d, test.form, test.origin, url.Values{"name": {"Visitor"}, "email": {test.email}, "message": {test.message}, "consent": {test.consent}, "website": {test.trap}})
		if rec.Code != test.status {
			t.Fatalf("%s returned %d: %s", test.form, rec.Code, rec.Body)
		}
	}
	values := url.Values{"name": {"Visitor"}, "email": {"visitor@example.com"}, "consent": {"yes"}}
	for i := 0; i < 30; i++ {
		if rec := sendLead(w, d, "waitlist", d.URL, values); rec.Code != 201 {
			t.Fatalf("valid waitlist failed: %d %s", rec.Code, rec.Body)
		}
	}
	if rec := sendLead(w, d, "waitlist", d.URL, values); rec.Code != 429 {
		t.Fatalf("rate limit not enforced: %d", rec.Code)
	}
}

func TestGeneratedLeadFormsUseConfiguredPublicEndpoint(t *testing.T) {
	req := deploymentRequest{Name: "Startup", Region: "us-west-2", Template: "builder", FormOrigin: "https://portal.example",
		Blocks: []pageBlock{{ID: "contact", Type: "contact", Text: "<Contact>", Align: "left", Size: "medium"}}}
	data, err := terraformVariables(req, "123456789012", strings.Repeat("c", 32))
	if err != nil {
		t.Fatal(err)
	}
	var variables map[string]string
	if err := json.Unmarshal(data, &variables); err != nil {
		t.Fatal(err)
	}
	page := variables["page_html"]
	if !strings.Contains(page, `action="https://portal.example/forms/`+strings.Repeat("c", 32)+`/contact"`) ||
		!strings.Contains(page, `method="post"`) || !strings.Contains(page, "&lt;Contact&gt;") || !strings.Contains(page, `name="consent"`) {
		t.Fatal("Missing safe working form output")
	}
	req.FormOrigin = ""
	if _, err := terraformVariables(req, "123456789012", strings.Repeat("c", 32)); err == nil {
		t.Fatal("Form rendered without a configured public endpoint")
	}
}
