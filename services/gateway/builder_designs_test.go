package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func builderRouteRequest(t *testing.T, w *deploymentWorker, access deploymentAccess, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	if !w.builderDesignRoute(rec, httptest.NewRequest(method, path, strings.NewReader(body)), access) {
		t.Fatalf("builder route did not handle %s", path)
	}
	return rec
}

func builderTestAccess() deploymentAccess {
	return deploymentAccess{ActorID: "actor-one", TenantID: "tenant-one", Role: "customer_admin",
		Subscribed: true, CanList: true, CanCreate: true, CanDelete: true}
}

func TestBuilderDraftIncompleteRoundTripAndIdentityIsolation(t *testing.T) {
	w, _ := testWorker(t)
	access := builderTestAccess()
	req := deploymentRequest{Template: "builder", Blocks: []pageBlock{
		{ID: "unfinished", Type: "button", URL: "https://", Align: "left", Size: "medium"},
		{ID: "form", Type: "contact", Align: "left", Size: "medium", Beside: true},
	}, DraftProducts: []builderProductDraft{{Price: "19.", PaymentLink: "https://buy."}}}
	data, _ := json.Marshal(req)
	rec := builderRouteRequest(t, w, access, "PUT", "/api/builder-draft", string(data))
	if rec.Code != 200 {
		t.Fatalf("save incomplete: %d %s", rec.Code, rec.Body)
	}
	rec = builderRouteRequest(t, w, access, "GET", "/api/builder-draft", "")
	var envelope struct {
		Draft *deploymentRequest `json:"draft"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil || envelope.Draft == nil || !reflect.DeepEqual(*envelope.Draft, req) {
		t.Fatalf("draft did not round trip: %s (%v)", rec.Body, err)
	}
	for _, identity := range []deploymentAccess{
		{ActorID: "actor-two", TenantID: access.TenantID},
		{ActorID: access.ActorID, TenantID: "tenant-two"},
		{ActorID: "one", TenantID: "tenant-oneactor-"},
	} {
		rec = builderRouteRequest(t, w, identity, "GET", "/api/builder-draft", "")
		if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"draft":null}` {
			t.Fatalf("draft leaked across identities: %s", rec.Body)
		}
		builderRouteRequest(t, w, identity, "DELETE", "/api/builder-draft", "")
	}
	rec = builderRouteRequest(t, w, access, "GET", "/api/builder-draft", "")
	if strings.Contains(rec.Body.String(), `"draft":null`) {
		t.Fatal("another identity deleted the draft")
	}
	entries, err := os.ReadDir(filepath.Join(w.root, ".drafts"))
	if err != nil || len(entries) != 1 || len(entries[0].Name()) != 69 {
		t.Fatalf("draft filename must be a private digest: %v %v", entries, err)
	}
	if entries[0].Name() == access.ActorID+".json" {
		t.Fatal("draft filename contains actor identity")
	}
	for _, body := range []string{`{"template":"builder","blocks":[]}`, `{"template":"builder"}`} {
		rec = builderRouteRequest(t, w, access, "PUT", "/api/builder-draft", body)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"blocks":[]`) {
			t.Fatalf("zero-element PUT must return an array: %d %s", rec.Code, rec.Body)
		}
		rec = builderRouteRequest(t, w, access, "GET", "/api/builder-draft", "")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"blocks":[]`) {
			t.Fatalf("zero-element GET must return an array: %d %s", rec.Code, rec.Body)
		}
	}
	for i := 0; i < 2; i++ {
		rec = builderRouteRequest(t, w, access, "DELETE", "/api/builder-draft", "")
		if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"draft":null}` {
			t.Fatalf("delete must return JSON and be idempotent: %s", rec.Body)
		}
	}
}

func TestBuilderDraftRejectsMalformedAndInvalidStructure(t *testing.T) {
	w, _ := testWorker(t)
	access := builderTestAccess()
	for name, body := range map[string]string{
		"unknown":         `{"template":"builder","secret":true}`,
		"trailing":        `{"template":"builder"} {}`,
		"malformed":       `{"template":"builder"`,
		"null request":    `null`,
		"null string":     `{"template":"builder","name":null}`,
		"null bool":       `{"template":"builder","blocks":[{"beside":null}]}`,
		"wrong type":      `{"template":"builder","blocks":"text"}`,
		"wrong bool":      `{"template":"builder","blocks":[{"beside":"true"}]}`,
		"wrong price":     `{"template":"builder","products":[{"priceCents":1.5}]}`,
		"duplicate field": `{"template":"builder","template":"blank"}`,
		"oversized":       `{"template":"builder","items":"` + strings.Repeat("x", builderRequestLimit) + `"}`,
		"wrong template":  `{"template":"business"}`,
		"bad region":      `{"template":"builder","region":"../../elsewhere"}`,
		"bad style":       `{"template":"builder","style":"evil"}`,
		"bad element":     `{"template":"builder","blocks":[{"id":"a","type":"script","align":"left","size":"medium"}]}`,
		"bad id":          `{"template":"builder","blocks":[{"id":"../a","type":"text","align":"left","size":"medium"}]}`,
		"bad row":         `{"template":"builder","blocks":[{"id":"a","type":"text","align":"left","size":"medium","beside":true}]}`,
		"duplicate id":    `{"template":"builder","blocks":[{"id":"a","type":"text","align":"left","size":"medium"},{"id":"a","type":"text","align":"left","size":"medium"}]}`,
		"bad target":      `{"template":"builder","draftDeploymentId":"../a"}`,
		"long raw price":  `{"template":"builder","draftProducts":[{"price":"` + strings.Repeat("1", 65) + `"}]}`,
		"long draft link": `{"template":"builder","draftProducts":[{"paymentLink":"` + strings.Repeat("a", 2001) + `"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := builderRouteRequest(t, w, access, "PUT", "/api/builder-draft", body)
			if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"error"`) {
				t.Fatalf("invalid draft accepted or error not JSON: %d %s", rec.Code, rec.Body)
			}
		})
	}
	rec := httptest.NewRecorder()
	if w.builderDesignRoute(rec, httptest.NewRequest("GET", "/api/deployments", nil), access) {
		t.Fatal("builder route intercepted the normal deployments route")
	}
}

func builderPublishedFixture(t *testing.T, w *deploymentWorker) (deployment, deploymentRequest) {
	t.Helper()
	d := deployment{ID: strings.Repeat("b", 32), Name: "Original", Region: "us-west-2",
		Style: "classic", Status: "ready", URL: "https://example.cloudfront.net",
		TenantID: "tenant-one", CreatedBy: "actor-one", CreatedAt: "2026-01-01T00:00:00Z"}
	req := deploymentRequest{Name: d.Name, Region: d.Region, Template: "builder", Style: d.Style,
		Blocks: []pageBlock{{ID: "heading", Type: "heading", Text: "Published", Align: "left", Size: "large"}}}
	dir := filepath.Join(w.root, d.ID)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveDeployment(dir, d); err != nil {
		t.Fatal(err)
	}
	if err := saveBuilderDesign(dir, req); err != nil {
		t.Fatal(err)
	}
	vars, err := terraformVariables(req, w.account, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfvars.json"), vars, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfstate"), []byte(`{"version":4,"resources":[{"mode":"managed"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	return d, req
}

func waitBuilderUpdate(t *testing.T, w *deploymentWorker, id string) deployment {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(w.root, ".operation.lock")); errors.Is(err, os.ErrNotExist) {
			w.mu.Lock()
			d, readErr := readDeployment(filepath.Join(w.root, id), id)
			w.mu.Unlock()
			if readErr != nil {
				t.Fatal(readErr)
			}
			return d
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("builder update did not complete")
	return deployment{}
}

func safeBuilderTestPlan() []byte {
	return []byte(`{"format_version":"1.2","complete":true,"resource_changes":[
		{"address":"aws_s3_bucket.site","mode":"managed","type":"aws_s3_bucket","name":"site","change":{"actions":["no-op"]}},
		{"address":"aws_s3_object.index","mode":"managed","type":"aws_s3_object","name":"index","change":{
			"actions":["update"],"before":{"bucket":"existing","key":"index.html","content":"old"},
			"after":{"bucket":"existing","key":"index.html","content":"new"},"after_unknown":{}}}]}`)
}

func TestBuilderUpdateUsesSafeSavedPlanAndPreservesIdentity(t *testing.T) {
	w, _ := testWorker(t)
	d, req := builderPublishedFixture(t, w)
	dir := filepath.Join(w.root, d.ID)
	oldVars, _ := os.ReadFile(filepath.Join(dir, "terraform.tfvars.json"))
	req.Name, req.Style, req.Blocks[0].Text = "Edited <site>", "warm", "Edited <script>alert(1)</script>"
	expected, err := terraformVariables(req, w.account, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	var commands []string
	var planned string
	started := make(chan struct{})
	resume := make(chan struct{})
	w.execute = func(ctx context.Context, workingDir string, out io.Writer, args ...string) ([]byte, error) {
		commands = append(commands, args[0])
		if workingDir != dir {
			t.Errorf("wrong working directory: %s", workingDir)
		}
		switch args[0] {
		case "plan":
			close(started)
			<-resume
			var candidate string
			for _, arg := range args {
				if strings.HasPrefix(arg, "-var-file=") {
					candidate = strings.TrimPrefix(arg, "-var-file=")
				}
				if strings.HasPrefix(arg, "-out=") {
					planned = strings.TrimPrefix(arg, "-out=")
				}
			}
			live, _ := os.ReadFile(filepath.Join(dir, "terraform.tfvars.json"))
			value, readErr := os.ReadFile(candidate)
			var candidateVars map[string]string
			decodeErr := json.Unmarshal(value, &candidateVars)
			if readErr != nil || string(value) != string(expected) || string(live) != string(oldVars) ||
				decodeErr != nil || !strings.Contains(candidateVars["page_html"], "&lt;script&gt;") {
				t.Errorf("candidate must contain desired safe HTML without changing live variables: %v", readErr)
			}
			return nil, os.WriteFile(planned, []byte("saved plan"), 0600)
		case "show":
			if !reflect.DeepEqual(args, []string{"show", "-json", planned}) {
				t.Errorf("show must inspect the saved plan: %v", args)
			}
			return safeBuilderTestPlan(), nil
		case "apply":
			if !reflect.DeepEqual(args, []string{"apply", "-input=false", "-no-color", planned}) {
				t.Errorf("apply must use only the inspected saved plan: %v", args)
			}
		default:
			t.Errorf("unexpected Terraform operation: %v", args)
		}
		return nil, nil
	}
	body, _ := json.Marshal(req)
	path := "/api/deployments/" + d.ID + "/design"
	rec := builderRouteRequest(t, w, builderTestAccess(), "PATCH", path, string(body))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("PATCH returned %d: %s", rec.Code, rec.Body)
	}
	<-started
	running, err := readDeployment(dir, d.ID)
	if err != nil || running.Status != "running" {
		t.Fatalf("running metadata hidden by old result.json: %+v %v", running, err)
	}
	busy := builderRouteRequest(t, w, builderTestAccess(), "PATCH", path, string(body))
	if busy.Code != 409 {
		t.Errorf("parallel update accepted: %d", busy.Code)
	}
	close(resume)
	updated := waitBuilderUpdate(t, w, d.ID)
	if !reflect.DeepEqual(commands, []string{"plan", "show", "apply"}) {
		t.Fatalf("wrong execution order: %v", commands)
	}
	if updated.Status != "ready" || updated.LastUpdateError != "" || updated.Name != req.Name || updated.Style != req.Style ||
		updated.ID != d.ID || updated.URL != d.URL || updated.Region != d.Region ||
		updated.TenantID != d.TenantID || updated.CreatedBy != d.CreatedBy || updated.CreatedAt != d.CreatedAt {
		t.Fatalf("update changed deployment identity or did not finish: %+v", updated)
	}
	newVars, _ := os.ReadFile(filepath.Join(dir, "terraform.tfvars.json"))
	saved, err := readBuilderDesign(dir)
	if err != nil || string(newVars) != string(expected) || !reflect.DeepEqual(saved, req) {
		t.Fatalf("updated design/variables not saved: %+v %v", saved, err)
	}
	if _, err := os.Stat(planned); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("owned saved plan was not cleaned up")
	}
	if _, err := os.Stat(filepath.Join(w.root, ".worker.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("successful update retained worker lock")
	}
}

func TestBuilderUpdateRejectsUnsafePlansWithoutApplying(t *testing.T) {
	for name, replacement := range map[string][2]string{
		"other resource": {"aws_s3_object.index", "aws_s3_object.other"},
		"replacement":    {`["update"]`, `["delete","create"]`},
		"creation":       {`["update"]`, `["create"]`},
		"deletion":       {`["update"]`, `["delete"]`},
		"bucket change":  {`"bucket":"existing","key":"index.html","content":"new"`, `"bucket":"new","key":"index.html","content":"new"`},
		"key change":     {`"key":"index.html","content":"new"`, `"key":"other.html","content":"new"`},
		"unknown bucket": {`"after_unknown":{}`, `"after_unknown":{"bucket":true}`},
		"incomplete":     {`"complete":true`, `"complete":false`},
	} {
		t.Run(name, func(t *testing.T) {
			w, _ := testWorker(t)
			d, req := builderPublishedFixture(t, w)
			dir := filepath.Join(w.root, d.ID)
			oldVars, _ := os.ReadFile(filepath.Join(dir, "terraform.tfvars.json"))
			oldDesign, _ := os.ReadFile(filepath.Join(dir, "design.json"))
			req.Blocks[0].Text = "Not published"
			apply := false
			w.execute = func(ctx context.Context, dir string, out io.Writer, args ...string) ([]byte, error) {
				switch args[0] {
				case "plan":
					for _, arg := range args {
						if strings.HasPrefix(arg, "-out=") {
							return nil, os.WriteFile(strings.TrimPrefix(arg, "-out="), nil, 0600)
						}
					}
				case "show":
					return []byte(strings.Replace(string(safeBuilderTestPlan()), replacement[0], replacement[1], 1)), nil
				case "apply":
					apply = true
				}
				return nil, nil
			}
			body, _ := json.Marshal(req)
			rec := builderRouteRequest(t, w, builderTestAccess(), "PATCH", "/api/deployments/"+d.ID+"/design", string(body))
			if rec.Code != 202 {
				t.Fatalf("update failed to start: %s", rec.Body)
			}
			result := waitBuilderUpdate(t, w, d.ID)
			newVars, _ := os.ReadFile(filepath.Join(dir, "terraform.tfvars.json"))
			newDesign, _ := os.ReadFile(filepath.Join(dir, "design.json"))
			if apply || result.Status != "ready" || result.LastUpdateError == "" ||
				string(newVars) != string(oldVars) || string(newDesign) != string(oldDesign) || result.Name != d.Name {
				t.Fatalf("unsafe plan applied or overwrote published design: %+v apply=%v", result, apply)
			}
			if _, err := os.Stat(filepath.Join(w.root, ".worker.lock")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("rejected plan retained worker lock")
			}
		})
	}
}

func TestBuilderUpdateApplyFailureRetainsLockAndPublishedDesign(t *testing.T) {
	w, _ := testWorker(t)
	d, req := builderPublishedFixture(t, w)
	dir := filepath.Join(w.root, d.ID)
	oldVars, _ := os.ReadFile(filepath.Join(dir, "terraform.tfvars.json"))
	oldDesign, _ := os.ReadFile(filepath.Join(dir, "design.json"))
	req.Blocks[0].Text = "Uncertain"
	w.execute = func(ctx context.Context, dir string, out io.Writer, args ...string) ([]byte, error) {
		switch args[0] {
		case "plan":
			for _, arg := range args {
				if strings.HasPrefix(arg, "-out=") {
					return nil, os.WriteFile(strings.TrimPrefix(arg, "-out="), nil, 0600)
				}
			}
		case "show":
			return safeBuilderTestPlan(), nil
		case "apply":
			return nil, errors.New("apply interrupted")
		}
		return nil, nil
	}
	body, _ := json.Marshal(req)
	rec := builderRouteRequest(t, w, builderTestAccess(), "PATCH", "/api/deployments/"+d.ID+"/design", string(body))
	if rec.Code != 202 {
		t.Fatalf("update failed to start: %s", rec.Body)
	}
	result := waitBuilderUpdate(t, w, d.ID)
	newVars, _ := os.ReadFile(filepath.Join(dir, "terraform.tfvars.json"))
	newDesign, _ := os.ReadFile(filepath.Join(dir, "design.json"))
	lockOwner, err := os.ReadFile(filepath.Join(w.root, ".worker.lock"))
	if result.Status != "failed" || !strings.Contains(result.LastUpdateError, "operator recovery") ||
		string(newVars) != string(oldVars) || string(newDesign) != string(oldDesign) ||
		err != nil || string(lockOwner) != d.ID || result.URL != d.URL {
		t.Fatalf("uncertain update was not safely retained: %+v %v", result, err)
	}
}

func TestBuilderDesignOwnershipLegacyAndImmutableRegion(t *testing.T) {
	w, _ := testWorker(t)
	d, req := builderPublishedFixture(t, w)
	path := "/api/deployments/" + d.ID + "/design"
	other := builderTestAccess()
	other.TenantID = "tenant-two"
	rec := builderRouteRequest(t, w, other, "GET", path, "")
	if rec.Code != 404 {
		t.Fatalf("design leaked to another tenant: %d %s", rec.Code, rec.Body)
	}

	req.Region = "eu-west-1"
	body, _ := json.Marshal(req)
	rec = builderRouteRequest(t, w, builderTestAccess(), "PATCH", path, string(body))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "immutable_settings") {
		t.Fatalf("region changed: %d %s", rec.Code, rec.Body)
	}
	req.Region = d.Region
	req.Blocks[0].Text = ""
	body, _ = json.Marshal(req)
	rec = builderRouteRequest(t, w, builderTestAccess(), "PATCH", path, string(body))
	if rec.Code != 400 {
		t.Fatalf("incomplete deployment request accepted: %d %s", rec.Code, rec.Body)
	}
	if err := os.Remove(filepath.Join(w.root, d.ID, "design.json")); err != nil {
		t.Fatal(err)
	}
	rec = builderRouteRequest(t, w, builderTestAccess(), "GET", path, "")
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "design_unavailable") {
		t.Fatalf("legacy design failure not clear: %d %s", rec.Code, rec.Body)
	}
}

func TestBuilderDraftEditTargetRoundTripsOnlyForOwnedDeployment(t *testing.T) {
	w, _ := testWorker(t)
	d, _ := builderPublishedFixture(t, w)
	body := `{"template":"builder","draftDeploymentId":"` + d.ID + `"}`
	access := builderTestAccess()
	rec := builderRouteRequest(t, w, access, "PUT", "/api/builder-draft", body)
	if rec.Code != 200 {
		t.Fatalf("owned edit draft rejected: %d %s", rec.Code, rec.Body)
	}
	rec = builderRouteRequest(t, w, access, "GET", "/api/builder-draft", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"draftDeploymentId":"`+d.ID+`"`) {
		t.Fatalf("edit target did not round trip: %s", rec.Body)
	}
	access.TenantID = "tenant-two"
	rec = builderRouteRequest(t, w, access, "PUT", "/api/builder-draft", body)
	if rec.Code != 404 {
		t.Fatalf("unowned edit target accepted: %d %s", rec.Code, rec.Body)
	}
	missing := `{"template":"builder","draftDeploymentId":"` + strings.Repeat("c", 32) + `"}`
	rec = builderRouteRequest(t, w, access, "PUT", "/api/builder-draft", missing)
	if rec.Code != 404 {
		t.Fatalf("missing edit target accepted: %d %s", rec.Code, rec.Body)
	}
}
