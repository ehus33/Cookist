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
			if err := os.WriteFile("terraform.tfstate", []byte(`{"version":4,"resources":[{"mode":"managed"}]}`), 0600); err != nil {
				os.Exit(1)
			}
		case "destroy":
			if err := os.WriteFile("terraform.tfstate", []byte(`{"version":4,"resources":[]}`), 0600); err != nil {
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
			_, operationErr := os.Stat(filepath.Join(w.root, ".operation.lock"))
			if (status != "ready" || errors.Is(lockErr, os.ErrNotExist)) &&
				errors.Is(operationErr, os.ErrNotExist) {
				return deployments[0]
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("deployment never reached %s", status)
	return deployment{}
}

func deleteDeploymentRequest(w *deploymentWorker, id, auth string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodDelete, "/api/deployments/"+id, nil)
	req.Header.Set("Authorization", auth)
	rec := httptest.NewRecorder()
	w.ServeHTTP(rec, req)
	return rec
}

func cleanupFixture(t *testing.T, w *deploymentWorker, status string) deployment {
	t.Helper()
	d := deployment{ID: strings.Repeat("a", 32), Name: "Site", Region: "us-west-2", Status: status}
	dir := filepath.Join(w.root, d.ID)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveDeployment(dir, d); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfstate"),
		[]byte(`{"version":4,"resources":[{"mode":"managed","type":"aws_s3_bucket"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	return d
}

func waitCleanup(t *testing.T, w *deploymentWorker, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, dirErr := os.Stat(filepath.Join(w.root, id))
		_, lockErr := os.Stat(filepath.Join(w.root, ".worker.lock"))
		_, operationErr := os.Stat(filepath.Join(w.root, ".operation.lock"))
		if errors.Is(dirErr, os.ErrNotExist) && errors.Is(lockErr, os.ErrNotExist) &&
			errors.Is(operationErr, os.ErrNotExist) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("cleanup did not remove the deployment and release its locks")
}

func TestDeploymentCleanupUsesStateAndSerializesAcrossWorkers(t *testing.T) {
	for _, status := range []string{"ready", "failed"} {
		t.Run(status, func(t *testing.T) {
			w, core := testWorker(t)
			d := cleanupFixture(t, w, status)
			if status == "failed" {
				// Legacy workers left an empty lock after a failed apply.
				if err := os.WriteFile(filepath.Join(w.root, ".worker.lock"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			started, release := make(chan struct{}), make(chan struct{})
			var commands []string
			w.execute = func(_ context.Context, dir string, _ io.Writer, args ...string) ([]byte, error) {
				commands = append(commands, strings.Join(args, " "))
				if dir != filepath.Join(w.root, d.ID) {
					return nil, errors.New("incorrect state directory")
				}
				if args[0] == "destroy" {
					close(started)
					<-release
					return nil, os.WriteFile(filepath.Join(dir, "terraform.tfstate"), []byte(`{"version":4,"resources":[]}`), 0600)
				}
				return nil, nil
			}
			for _, auth := range []string{"", "Bearer other"} {
				rec := deleteDeploymentRequest(w, d.ID, auth)
				if rec.Code != 401 && rec.Code != 403 {
					t.Fatalf("unauthorized cleanup returned %d", rec.Code)
				}
			}
			rec := deleteDeploymentRequest(w, d.ID, "Bearer owner")
			if rec.Code != 202 || !strings.Contains(rec.Body.String(), `"status":"deleting"`) {
				t.Fatalf("cleanup returned %d: %s", rec.Code, rec.Body)
			}
			<-started
			other, err := newDeploymentWorker(core.URL, "owner", w.root, w.terraform, w.account)
			if err != nil {
				t.Fatal(err)
			}
			if got := deleteDeploymentRequest(other, d.ID, "Bearer owner").Code; got != 409 {
				t.Errorf("concurrent cleanup returned %d", got)
			}
			if got := requestDeployment(other, "POST", "Bearer owner", `{"name":"New","region":"us-west-2","template":"blank"}`).Code; got != 409 {
				t.Errorf("create during cleanup returned %d", got)
			}
			close(release)
			waitCleanup(t, w, d.ID)
			if len(commands) != 2 || commands[0] != "init -input=false -lockfile=readonly -no-color" ||
				commands[1] != "destroy -input=false -auto-approve -no-color" {
				t.Fatalf("incorrect cleanup commands: %v", commands)
			}
			if rec := requestDeployment(w, "GET", "Bearer owner", ""); rec.Body.String() != "[]\n" {
				t.Fatalf("deleted deployment remains listed: %s", rec.Body)
			}
		})
	}
}

func TestDeploymentCleanupFailureRetainsStateAndCanRetry(t *testing.T) {
	for _, failure := range []string{"destroy_error", "resources_remain", "missing_state", "corrupt_state"} {
		t.Run(failure, func(t *testing.T) {
			w, _ := testWorker(t)
			d := cleanupFixture(t, w, "failed")
			dir := filepath.Join(w.root, d.ID)
			statePath := filepath.Join(dir, "terraform.tfstate")
			if failure == "missing_state" {
				if err := os.Remove(statePath); err != nil {
					t.Fatal(err)
				}
			} else if failure == "corrupt_state" {
				if err := os.WriteFile(statePath, []byte("broken"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			w.execute = func(_ context.Context, _ string, _ io.Writer, args ...string) ([]byte, error) {
				if failure == "missing_state" || failure == "corrupt_state" {
					t.Error("invalid state must not invoke Terraform")
				}
				if args[0] == "destroy" && failure == "destroy_error" {
					return nil, errors.New("AccessDenied")
				}
				return nil, nil
			}
			if rec := deleteDeploymentRequest(w, d.ID, "Bearer owner"); rec.Code != 202 {
				t.Fatalf("cleanup returned %d: %s", rec.Code, rec.Body)
			}
			waitDeployment(t, w, "cleanup_failed")
			if _, err := os.Stat(filepath.Join(w.root, ".worker.lock")); err != nil {
				t.Fatalf("cleanup failure lost the lock: %v", err)
			}
			if _, err := os.Stat(dir); err != nil {
				t.Fatalf("cleanup failure lost the directory: %v", err)
			}
			if failure != "missing_state" {
				if _, err := os.Stat(statePath); err != nil {
					t.Fatalf("cleanup failure lost the state: %v", err)
				}
			}
			if err := os.WriteFile(statePath, []byte(`{"version":4,"resources":[]}`), 0600); err != nil {
				t.Fatal(err)
			}
			w.execute = func(context.Context, string, io.Writer, ...string) ([]byte, error) { return nil, nil }
			if rec := deleteDeploymentRequest(w, d.ID, "Bearer owner"); rec.Code != 202 {
				t.Fatalf("cleanup retry returned %d: %s", rec.Code, rec.Body)
			}
			waitCleanup(t, w, d.ID)
		})
	}
}

func TestDeploymentCleanupRejectsUnsafeRecovery(t *testing.T) {
	for _, scenario := range []string{"running", "deleting", "another_lock", "legacy_active", "pending", "invalid_id"} {
		t.Run(scenario, func(t *testing.T) {
			w, _ := testWorker(t)
			d := cleanupFixture(t, w, "failed")
			switch scenario {
			case "running", "deleting":
				d.Status = scenario
				if scenario == "running" {
					if err := os.Remove(filepath.Join(w.root, d.ID, "result.json")); err != nil {
						t.Fatal(err)
					}
				}
				if err := saveDeployment(filepath.Join(w.root, d.ID), d); err != nil {
					t.Fatal(err)
				}
			case "another_lock":
				if err := os.WriteFile(filepath.Join(w.root, ".worker.lock"), []byte(strings.Repeat("b", 32)), 0600); err != nil {
					t.Fatal(err)
				}
			case "legacy_active", "pending":
				if err := os.WriteFile(filepath.Join(w.root, ".worker.lock"), nil, 0600); err != nil {
					t.Fatal(err)
				}
				id := strings.Repeat("b", 32)
				name := id
				if scenario == "pending" {
					name = "." + id + ".pending"
				}
				dir := filepath.Join(w.root, name)
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := saveDeployment(dir, deployment{ID: id, Status: "running"}); err != nil {
					t.Fatal(err)
				}
			case "invalid_id":
				d.ID = "../outside"
			}
			w.execute = func(context.Context, string, io.Writer, ...string) ([]byte, error) {
				t.Error("unsafe cleanup invoked Terraform")
				return nil, nil
			}
			rec := deleteDeploymentRequest(w, d.ID, "Bearer owner")
			want := 409
			if scenario == "invalid_id" {
				want = 404
			}
			if rec.Code != want {
				t.Fatalf("unsafe cleanup returned %d: %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestCleanupOfDeploymentThatNeverApplied(t *testing.T) {
	w, _ := testWorker(t)
	w.execute = func(context.Context, string, io.Writer, ...string) ([]byte, error) {
		return nil, errors.New("init failed")
	}
	if rec := requestDeployment(w, "POST", "Bearer owner", `{"name":"Site","region":"us-west-2","template":"blank"}`); rec.Code != 202 {
		t.Fatalf("create returned %d", rec.Code)
	}
	d := waitDeployment(t, w, "failed")
	w.execute = func(context.Context, string, io.Writer, ...string) ([]byte, error) {
		t.Error("never-applied deployment must not invoke Terraform")
		return nil, nil
	}
	if rec := deleteDeploymentRequest(w, d.ID, "Bearer owner"); rec.Code != 202 {
		t.Fatalf("cleanup returned %d", rec.Code)
	}
	waitCleanup(t, w, d.ID)
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
	body := `{"name":"Shop","region":"eu-west-1","template":"business","style":"warm","businessName":"Garage","listTitle":"Services","items":"Tire"}`
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
	if d.URL != "https://example.cloudfront.net" || d.Region != "eu-west-1" || d.Style != "warm" {
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
	if !strings.Contains(variables["page_html"], `<body class="generated-site site-style-warm">`) {
		t.Fatal("selected style was not written to the generated site")
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
	if got := waitDeployment(t, restarted, "ready"); got.ID != d.ID || got.Style != "warm" {
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
	if state, err := os.ReadFile(filepath.Join(w.root, d.ID, "terraform.tfstate")); err != nil || string(state) != `{"version":4,"resources":[{"mode":"managed"}]}` {
		t.Fatalf("Terraform process did not persist state: %q, %v", state, err)
	}
	if rec := deleteDeploymentRequest(w, d.ID, "Bearer owner"); rec.Code != 202 {
		t.Fatalf("cleanup returned %d: %s", rec.Code, rec.Body)
	}
	waitCleanup(t, w, d.ID)
}

func TestDeploymentCleanupFreesCappedSlot(t *testing.T) {
	t.Setenv("COOKIST_TEST_TERRAFORM_HELPER", "1")
	w, _ := testWorker(t)
	d := cleanupFixture(t, w, "ready")
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("%032x", i)
		dir := filepath.Join(w.root, id)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := saveDeployment(dir, deployment{ID: id, Status: "ready"}); err != nil {
			t.Fatal(err)
		}
	}
	body := `{"name":"Replacement","region":"us-west-2","template":"blank"}`
	if rec := requestDeployment(w, "POST", "Bearer owner", body); rec.Code != 429 {
		t.Fatalf("four reserved slots did not enforce the cap: %d", rec.Code)
	}
	if rec := deleteDeploymentRequest(w, d.ID, "Bearer owner"); rec.Code != 202 {
		t.Fatalf("cleanup returned %d: %s", rec.Code, rec.Body)
	}
	waitCleanup(t, w, d.ID)
	rec := requestDeployment(w, "POST", "Bearer owner", body)
	if rec.Code != 202 {
		t.Fatalf("cleanup did not free a slot: %d: %s", rec.Code, rec.Body)
	}
	var replacement deployment
	if err := json.Unmarshal(rec.Body.Bytes(), &replacement); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result, err := readDeployment(filepath.Join(w.root, replacement.ID), replacement.ID)
		_, operationErr := os.Stat(filepath.Join(w.root, ".operation.lock"))
		if err == nil && result.Status == "ready" && errors.Is(operationErr, os.ErrNotExist) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("replacement deployment never completed")
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
	for _, style := range []string{"", "classic", "modern", "warm", "bold"} {
		t.Run("style_"+style, func(t *testing.T) {
			config, err := terraformVariables(deploymentRequest{Name: "Site", Region: "us-west-2", Template: "business", Style: style,
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
		})
	}
}

func TestTerraformBlankTemplate(t *testing.T) {
	for _, style := range []string{"", "classic", "modern", "warm", "bold"} {
		t.Run("style_"+style, func(t *testing.T) {
			config, err := terraformVariables(deploymentRequest{Name: "<Site>", Region: "us-west-2", Template: "blank", Style: style,
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
		})
	}
}

func TestTerraformSiteStyles(t *testing.T) {
	req := deploymentRequest{Name: "Site", Region: "us-west-2", Template: "business", BusinessName: "Garage", ListTitle: "Services", Items: "Tire"}
	legacy, err := terraformVariables(req, "123456789012", strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	for _, style := range []string{"classic", "modern", "warm", "bold"} {
		t.Run(style, func(t *testing.T) {
			req.Style = style
			config, err := terraformVariables(req, "123456789012", strings.Repeat("a", 32))
			if err != nil {
				t.Fatal(err)
			}
			var parsed struct {
				Page string `json:"page_html"`
			}
			if err := json.Unmarshal(config, &parsed); err != nil {
				t.Fatal(err)
			}
			if style == "classic" {
				if string(config) != string(legacy) || !strings.Contains(parsed.Page, `body{font-family:system-ui,sans-serif;max-width:48rem;margin:4rem auto;padding:0 1.5rem;color:#262a28}li{margin:.75rem 0}`) {
					t.Fatal("Classic changed the legacy page appearance")
				}
			} else if !strings.Contains(parsed.Page, `<body class="generated-site site-style-`+style+`">`) ||
				!strings.Contains(parsed.Page, ".site-style-"+style+" {") {
				t.Fatal("generated page does not select an embedded style")
			}
		})
	}
}

func TestDeploymentRejectsUnknownStylesBeforeProvisioning(t *testing.T) {
	for _, style := range []string{"neon", "Modern", "modern ", `"></style><script>alert(1)</script>`} {
		t.Run(style, func(t *testing.T) {
			w, _ := testWorker(t)
			req := deploymentRequest{Name: "Site", Region: "us-west-2", Template: "blank", Style: style}
			body, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			rec := requestDeployment(w, http.MethodPost, "Bearer owner", string(body))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "supported site style") {
				t.Fatalf("invalid style returned %d: %s", rec.Code, rec.Body)
			}
			entries, err := os.ReadDir(w.root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("invalid style created deployment files: %v", err)
			}
		})
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
