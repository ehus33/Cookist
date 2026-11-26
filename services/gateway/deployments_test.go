package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("COOKIST_TEST_TERRAFORM_HELPER") == "1" {
		if len(os.Args) < 2 {
			os.Exit(2)
		}
		switch os.Args[1] {
		case "init":
		case "apply":
			if err := os.WriteFile("terraform.tfstate", []byte("state"), 0600); err != nil {
				os.Exit(1)
			}
		case "output":
			fmt.Print("https://example.cloudfront.net")
		default:
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func testWorker(t *testing.T) (*deploymentWorker, *httptest.Server) {
	t.Helper()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer owner":
			_, _ = w.Write([]byte(`{"actorId":"owner"}`))
		case "Bearer other":
			_, _ = w.Write([]byte(`{"actorId":"other"}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	worker, err := newDeploymentWorker(core.URL, "owner", t.TempDir(), exe, "123456789012")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Close)
	return worker, core
}

func requestDeployment(w *deploymentWorker, method, auth, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/deployments", strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	w.ServeHTTP(rec, req)
	return rec
}

func waitDeployment(t *testing.T, w *deploymentWorker, status string) deployment {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rec := requestDeployment(w, "GET", "Bearer owner", "")
		if rec.Code != 200 {
			t.Fatalf("list returned %d: %s", rec.Code, rec.Body)
		}
		var deployments []deployment
		if err := json.Unmarshal(rec.Body.Bytes(), &deployments); err != nil {
			t.Fatal(err)
		}
		if len(deployments) == 1 && deployments[0].Status == status {
			_, lockErr := os.Stat(filepath.Join(w.root, ".worker.lock"))
			if status != "ready" || errors.Is(lockErr, os.ErrNotExist) {
				return deployments[0]
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("deployment never reached %s", status)
	return deployment{}
}

func TestDeploymentOperatorAndInputRestrictions(t *testing.T) {
	w, _ := testWorker(t)
	body := `{"name":"Site","region":"us-west-2","template":"blank"}`
	for _, test := range []struct {
		auth string
		want int
	}{
		{"", 401}, {"Bearer invalid", 401}, {"Bearer other", 403},
	} {
		if got := requestDeployment(w, "POST", test.auth, body).Code; got != test.want {
			t.Errorf("%q: got %d, want %d", test.auth, got, test.want)
		}
	}
	for _, body := range []string{
		`{"name":"Site","region":"${file(\"secret\")}","template":"blank"}`,
		`{"name":"Site","region":"us-west-2","template":"business"}`,
		`{"name":"Site","region":"us-west-2","template":"blank","terraform":"arbitrary"}`,
	} {
		if got := requestDeployment(w, "POST", "Bearer owner", body).Code; got != 400 {
			t.Errorf("invalid request got %d", got)
		}
	}
}

func TestDeploymentPersistsStateAndSerializesJobs(t *testing.T) {
	w, core := testWorker(t)
	started := make(chan struct{})
	release := make(chan struct{})
	w.execute = func(_ context.Context, dir string, _ io.Writer, args ...string) ([]byte, error) {
		switch args[0] {
		case "apply":
			close(started)
			<-release
			if err := os.WriteFile(filepath.Join(dir, "terraform.tfstate"), []byte("state"), 0600); err != nil {
				return nil, err
			}
		case "output":
			return []byte("https://example.cloudfront.net"), nil
		}
		return nil, nil
	}
	body := `{"name":"Shop","region":"eu-west-1","template":"business","businessName":"Garage","listTitle":"Services","items":"Tire"}`
	first := requestDeployment(w, "POST", "Bearer owner", body)
	if first.Code != 202 {
		t.Fatalf("create returned %d: %s", first.Code, first.Body)
	}
	<-started
	if got := requestDeployment(w, "POST", "Bearer owner", body).Code; got != 409 {
		t.Fatalf("second job got %d", got)
	}
	close(release)
	d := waitDeployment(t, w, "ready")
	if d.URL != "https://example.cloudfront.net" || d.Region != "eu-west-1" {
		t.Fatalf("unexpected deployment: %+v", d)
	}
	data, err := os.ReadFile(filepath.Join(w.root, d.ID, "terraform.tfvars.json"))
	if err != nil {
		t.Fatal(err)
	}
	var variables map[string]string
	if err := json.Unmarshal(data, &variables); err != nil {
		t.Fatal(err)
	}
	if variables["account_id"] != w.account || variables["deployment_id"] != d.ID || variables["region"] != d.Region {
		t.Fatalf("unexpected Terraform variables: %+v", variables)
	}
	files, err := siteTerraform.ReadDir("terraform/site")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		want, err := siteTerraform.ReadFile("terraform/site/" + file.Name())
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(w.root, d.ID, file.Name()))
		if err != nil || string(got) != string(want) {
			t.Fatalf("deployment template %s was not persisted: %v", file.Name(), err)
		}
	}
	restarted, err := newDeploymentWorker(core.URL, "owner", w.root, w.terraform, w.account)
	if err != nil {
		t.Fatal(err)
	}
	if got := waitDeployment(t, restarted, "ready"); got.ID != d.ID {
		t.Fatalf("deployment was not persisted: %+v", got)
	}
	if state, err := os.ReadFile(filepath.Join(restarted.root, d.ID, "terraform.tfstate")); err != nil || string(state) != "state" {
		t.Fatalf("Terraform state was not retained: %q, %v", state, err)
	}
}

func TestDeploymentRunsTerraformProcess(t *testing.T) {
	t.Setenv("COOKIST_TEST_TERRAFORM_HELPER", "1")
	w, _ := testWorker(t)
	body := `{"name":"Site","region":"us-west-2","template":"blank"}`
	if got := requestDeployment(w, "POST", "Bearer owner", body).Code; got != 202 {
		t.Fatalf("create got %d", got)
	}
	d := waitDeployment(t, w, "ready")
	if d.URL != "https://example.cloudfront.net" {
		t.Fatalf("unexpected URL: %q", d.URL)
	}
	if state, err := os.ReadFile(filepath.Join(w.root, d.ID, "terraform.tfstate")); err != nil || string(state) != "state" {
		t.Fatalf("Terraform process did not persist state: %q, %v", state, err)
	}
}

func TestFailedDeploymentAndStaleLock(t *testing.T) {
	w, _ := testWorker(t)
	w.execute = func(_ context.Context, _ string, _ io.Writer, _ ...string) ([]byte, error) {
		return nil, errors.New("failed")
	}
	body := `{"name":"Site","region":"us-west-2","template":"blank"}`
	if got := requestDeployment(w, "POST", "Bearer owner", body).Code; got != 202 {
		t.Fatalf("create got %d", got)
	}
	waitDeployment(t, w, "failed")
	lock := filepath.Join(w.root, ".worker.lock")
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("failed deployment must retain its lock: %v", err)
	}
	if got := requestDeployment(w, "POST", "Bearer owner", body).Code; got != 409 {
		t.Fatalf("failed worker lock got %d", got)
	}
}

func TestDeploymentLimitCountsAllReservedSlots(t *testing.T) {
	for _, status := range []string{"ready", "running", "failed", "pending", "invalid_metadata"} {
		t.Run(status, func(t *testing.T) {
			w, core := testWorker(t)
			for i := 1; i <= 4; i++ {
				id := fmt.Sprintf("%032x", i)
				name := id
				if status == "pending" {
					name = "." + id + ".pending"
				}
				dir := filepath.Join(w.root, name)
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if status != "pending" && status != "invalid_metadata" {
					if err := saveDeployment(dir, deployment{ID: id, Status: status}); err != nil {
						t.Fatal(err)
					}
				}
			}
			restarted, err := newDeploymentWorker(core.URL, "other", w.root, w.terraform, w.account)
			if err != nil {
				t.Fatal(err)
			}
			restarted.execute = func(context.Context, string, io.Writer, ...string) ([]byte, error) {
				t.Error("a capped request must not run Terraform")
				return nil, errors.New("unexpected execution")
			}
			for i := 0; i < 2; i++ {
				rec := requestDeployment(restarted, "POST", "Bearer other", `{"name":"Fifth","region":"us-west-2","template":"blank"}`)
				if rec.Code != 429 || !strings.Contains(rec.Body.String(), `"code":"deployment_limit"`) {
					t.Fatalf("fifth deployment returned %d: %s", rec.Code, rec.Body)
				}
				if _, err := os.Stat(filepath.Join(w.root, ".worker.lock")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("quota rejection left the worker locked: %v", err)
				}
			}
		})
	}
}

func TestDeploymentLimitSerializesFourthSlotAcrossWorkers(t *testing.T) {
	w, core := testWorker(t)
	for i, status := range []string{"ready", "failed", "running"} {
		id := fmt.Sprintf("%032x", i+1)
		dir := filepath.Join(w.root, id)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := saveDeployment(dir, deployment{ID: id, Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	other, err := newDeploymentWorker(core.URL, "other", w.root, w.terraform, w.account)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	defer close(release)
	execute := func(_ context.Context, _ string, _ io.Writer, args ...string) ([]byte, error) {
		if args[0] == "apply" {
			<-release
		}
		if args[0] == "output" {
			return []byte("https://example.cloudfront.net"), nil
		}
		return nil, nil
	}
	w.execute, other.execute = execute, execute
	responses := make(chan *httptest.ResponseRecorder, 2)
	body := `{"name":"Fourth","region":"us-west-2","template":"blank"}`
	go func() { responses <- requestDeployment(w, "POST", "Bearer owner", body) }()
	go func() { responses <- requestDeployment(other, "POST", "Bearer other", body) }()
	first, second := <-responses, <-responses
	if !((first.Code == 202 && second.Code == 409) || (first.Code == 409 && second.Code == 202)) {
		t.Fatalf("expected one accepted deployment and one busy worker, got %d and %d", first.Code, second.Code)
	}
	accepted := first
	if second.Code == 202 {
		accepted = second
	}
	var d deployment
	if err := json.Unmarshal(accepted.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	release <- struct{}{}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := os.Stat(filepath.Join(w.root, ".worker.lock"))
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil || time.Now().After(deadline) {
			t.Fatalf("fourth deployment did not release the worker: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, err := os.ReadFile(filepath.Join(w.root, d.ID, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &d); err != nil || d.Status != "ready" {
		t.Fatalf("fourth deployment did not finish: %s, %v", data, err)
	}
	for _, test := range []struct {
		worker *deploymentWorker
		auth   string
	}{{w, "Bearer owner"}, {other, "Bearer other"}} {
		rec := requestDeployment(test.worker, "POST", test.auth, body)
		if rec.Code != 429 {
			t.Fatalf("fifth deployment returned %d: %s", rec.Code, rec.Body)
		}
	}
	entries, err := os.ReadDir(w.root)
	if err != nil || len(entries) != 4 {
		t.Fatalf("expected exactly four deployment directories, got %d: %v", len(entries), err)
	}
}

func TestTerraformVariablesEscapeHTMLAndPreserveLiteralText(t *testing.T) {
	config, err := terraformVariables(deploymentRequest{Name: "Site", Region: "us-west-2", Template: "business",
		BusinessName: `<garage> ${file("secret")}`, ListTitle: "<Services>", Items: "Oil & filter\n\n%{ if true }\n$${literal}"}, "123456789012", strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Page string `json:"page_html"`
	}
	if err := json.Unmarshal(config, &parsed); err != nil {
		t.Fatal(err)
	}
	page := parsed.Page
	for _, want := range []string{"&lt;garage&gt;", `${file(&#34;secret&#34;)}`, "<h2>&lt;Services&gt;</h2>", "Oil &amp; filter", "%{ if true }", "$${literal}"} {
		if !strings.Contains(page, want) {
			t.Errorf("page does not contain %q: %s", want, page)
		}
	}
	if strings.Contains(page, "<li></li>") || strings.Contains(page, "<garage>") {
		t.Fatalf("unexpected unescaped or empty item: %s", page)
	}
}

func TestTerraformBlankTemplate(t *testing.T) {
	config, err := terraformVariables(deploymentRequest{Name: "<Site>", Region: "us-west-2", Template: "blank",
		BusinessName: "Ignored", ListTitle: "Ignored", Items: "Ignored"}, "123456789012", strings.Repeat("b", 32))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Page string `json:"page_html"`
	}
	if err := json.Unmarshal(config, &parsed); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(parsed.Page, "<title>&lt;Site&gt;</title>") || !strings.Contains(parsed.Page, "<main></main>") || strings.Contains(parsed.Page, "Ignored") {
		t.Fatalf("unexpected blank page: %s", parsed.Page)
	}
}

func TestTerraformPreparationRejectsInvalidInput(t *testing.T) {
	valid := deploymentRequest{Name: "Site", Region: "us-west-2", Template: "blank"}
	for _, test := range []struct {
		req         deploymentRequest
		account, id string
	}{
		{valid, "invalid", strings.Repeat("a", 32)},
		{valid, "123456789012", "../site"},
		{deploymentRequest{Name: "Site", Region: "us-west-2", Template: "unknown"}, "123456789012", strings.Repeat("a", 32)},
	} {
		dir := t.TempDir()
		if err := writeTerraformConfig(dir, test.req, test.account, test.id); err == nil {
			t.Fatal("invalid Terraform inputs were accepted")
		}
		files, err := os.ReadDir(dir)
		if err != nil || len(files) != 0 {
			t.Fatalf("invalid inputs wrote configuration: %v", err)
		}
	}
}
