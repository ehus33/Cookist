package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type deploymentRequest struct {
	Name         string `json:"name"`
	Region       string `json:"region"`
	Template     string `json:"template"`
	BusinessName string `json:"businessName"`
	ListTitle    string `json:"listTitle"`
	Items        string `json:"items"`
}

type deployment struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Region    string `json:"region"`
	Status    string `json:"status"`
	URL       string `json:"url,omitempty"`
	CreatedAt string `json:"createdAt"`
}

const maxTestDeployments = 4

type deploymentWorker struct {
	coreURL, owner, root, terraform, account string
	client                                   *http.Client
	mu                                       sync.Mutex
	execute                                  func(context.Context, string, io.Writer, ...string) ([]byte, error)
}

var regionPattern = regexp.MustCompile(`^[a-z]{2}(?:-[a-z]+)+-[0-9]+$`)
var accountPattern = regexp.MustCompile(`^[0-9]{12}$`)
var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var websiteURLPattern = regexp.MustCompile(`^https://[a-z0-9-]+\.cloudfront\.net$`)

//go:embed terraform/site/*.tf terraform/site/.terraform.lock.hcl
var siteTerraform embed.FS

func newDeploymentWorker(coreURL, owner, root, terraform, account string) (*deploymentWorker, error) {
	if owner == "" || root == "" || terraform == "" || !accountPattern.MatchString(account) {
		return nil, errors.New("deployment owner, state directory, Terraform executable and 12-digit AWS account ID are required")
	}
	if !filepath.IsAbs(root) || !filepath.IsAbs(terraform) {
		return nil, errors.New("deployment state directory and Terraform executable must be absolute paths")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("create deployment state directory: %w", err)
	}
	info, err := os.Stat(terraform)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("Terraform executable is unavailable")
	}
	w := &deploymentWorker{coreURL: coreURL, owner: owner, root: root, terraform: terraform, account: account,
		client: &http.Client{Timeout: 6 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	w.execute = func(ctx context.Context, dir string, logFile io.Writer, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, w.terraform, args...)
		cmd.Dir, cmd.Stderr = dir, logFile
		cmd.Env = append(os.Environ(), "TF_IN_AUTOMATION=1", "TF_INPUT=0")
		if args[0] == "output" {
			return cmd.Output()
		}
		cmd.Stdout = logFile
		return nil, cmd.Run()
	}
	return w, nil
}

func (w *deploymentWorker) authorize(r *http.Request) error {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || len(auth) > 16384 {
		return &deploymentError{http.StatusUnauthorized, "unauthorized", "Sign in to continue."}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(w.coreURL, "/")+"/api/me", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	res, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		return &deploymentError{http.StatusUnauthorized, "unauthorized", "Sign in to continue."}
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("identity service returned %d", res.StatusCode)
	}
	var identity struct {
		ActorID string `json:"actorId"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&identity); err != nil {
		return err
	}
	if identity.ActorID == "" || identity.ActorID != w.owner {
		return &deploymentError{http.StatusForbidden, "forbidden", "Only the configured operator can manage deployments."}
	}
	return nil
}

type deploymentError struct {
	status        int
	code, message string
}

func (e *deploymentError) Error() string { return e.message }

func (w *deploymentWorker) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if err := w.authorize(r); err != nil {
		var apiErr *deploymentError
		if errors.As(err, &apiErr) {
			jsonError(rw, apiErr.status, apiErr.code, apiErr.message)
		} else {
			log.Printf("deployment authorization unavailable: %v", err)
			jsonError(rw, 503, "auth_unavailable", "Deployment authorization is unavailable.")
		}
		return
	}
	if r.URL.Path != "/api/deployments" {
		jsonError(rw, 404, "not_found", "Route not found.")
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.list(rw)
	case http.MethodPost:
		w.create(rw, r)
	default:
		rw.Header().Set("Allow", "GET, POST")
		jsonError(rw, 405, "method_not_allowed", "Use GET or POST.")
	}
}

func validateDeployment(req deploymentRequest) error {
	if strings.TrimSpace(req.Name) == "" || len(req.Name) > 320 || !regionPattern.MatchString(req.Region) ||
		(req.Template != "blank" && req.Template != "business") || len(req.BusinessName) > 320 ||
		len(req.ListTitle) > 320 || len(req.Items) > 8000 ||
		(req.Template == "business" && (strings.TrimSpace(req.BusinessName) == "" || strings.TrimSpace(req.ListTitle) == "")) {
		return errors.New("Complete the required fields and select a valid template and region.")
	}
	return nil
}

func (w *deploymentWorker) create(rw http.ResponseWriter, r *http.Request) {
	var request deploymentRequest
	decoder := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 16384))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		jsonError(rw, 400, "invalid_request", "Invalid deployment request.")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		jsonError(rw, 400, "invalid_request", "Invalid deployment request.")
		return
	}
	if err := validateDeployment(request); err != nil {
		jsonError(rw, 400, "invalid_request", err.Error())
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	lock := filepath.Join(w.root, ".worker.lock")
	f, err := os.OpenFile(lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		jsonError(rw, 409, "worker_busy", "A deployment is running or needs operator recovery.")
		return
	}
	if err != nil {
		log.Printf("deployment lock failed: %v", err)
		jsonError(rw, 500, "storage_error", "Deployment storage is unavailable.")
		return
	}
	if err = f.Close(); err != nil {
		log.Printf("deployment lock close failed: %v", err)
		jsonError(rw, 500, "storage_error", "Deployment storage is unavailable; operator recovery is required.")
		return
	}
	entries, err := os.ReadDir(w.root)
	if err != nil {
		log.Printf("deployment limit check failed: %v", err)
		jsonError(rw, 500, "storage_error", "Unable to check the deployment limit; operator recovery is required.")
		return
	}
	count := 0
	for _, entry := range entries {
		name := entry.Name()
		pendingID := strings.TrimSuffix(strings.TrimPrefix(name, "."), ".pending")
		if idPattern.MatchString(name) || (name == "."+pendingID+".pending" && idPattern.MatchString(pendingID)) {
			count++
		}
	}
	if count >= maxTestDeployments {
		w.removeLock()
		jsonError(rw, 429, "deployment_limit", "The test phase allows at most four site deployments across all users. Running and failed deployments also count.")
		return
	}
	var id [16]byte
	if _, err = rand.Read(id[:]); err != nil {
		log.Printf("deployment ID creation failed: %v", err)
		w.removeLock()
		jsonError(rw, 500, "storage_error", "Unable to start the deployment.")
		return
	}
	d := deployment{ID: hex.EncodeToString(id[:]), Name: strings.TrimSpace(request.Name),
		Region: request.Region, Status: "running", CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	dir := filepath.Join(w.root, d.ID)
	staging := filepath.Join(w.root, "."+d.ID+".pending")
	if err = os.Mkdir(staging, 0700); err == nil {
		err = writeTerraformConfig(staging, request, w.account, d.ID)
		if err == nil {
			err = saveDeployment(staging, d)
		}
		if err == nil {
			err = os.Rename(staging, dir)
		}
	}
	if err != nil {
		log.Printf("deployment preparation failed: %v", err)
		jsonError(rw, 500, "storage_error", "Unable to save the deployment; operator recovery is required.")
		return
	}
	go w.run(dir, d)
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	rw.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(rw).Encode(d)
}

func (w *deploymentWorker) removeLock() {
	if err := os.Remove(filepath.Join(w.root, ".worker.lock")); err != nil {
		log.Printf("deployment lock removal failed: %v", err)
	}
}

func saveDeployment(dir string, d deployment) error {
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}
	name := "deployment.json"
	if d.Status != "running" {
		name = "result.json"
	}
	tmp := filepath.Join(dir, name+".tmp")
	if err = os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, name))
}

func (w *deploymentWorker) run(dir string, d deployment) {
	logFile, err := os.OpenFile(filepath.Join(dir, "terraform.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err == nil {
		defer logFile.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
		defer cancel()
		for _, args := range [][]string{{"init", "-input=false", "-no-color"}, {"apply", "-input=false", "-auto-approve", "-no-color"}} {
			if _, err = w.execute(ctx, dir, logFile, args...); err != nil {
				break
			}
		}
		if err == nil {
			var output []byte
			output, err = w.execute(ctx, dir, logFile, "output", "-raw", "website_url")
			if err == nil {
				d.URL = strings.TrimSpace(string(output))
				if !websiteURLPattern.MatchString(d.URL) {
					err = errors.New("Terraform returned an invalid website URL")
					d.URL = ""
				}
			}
		}
	}
	if err != nil {
		log.Printf("deployment %s failed: %v", d.ID, err)
		d.Status = "failed"
	} else {
		d.Status = "ready"
	}
	if err = saveDeployment(dir, d); err != nil {
		log.Printf("deployment %s status could not be saved: %v; lock retained", d.ID, err)
		return
	}
	if d.Status == "ready" {
		w.removeLock()
	}
}

func (w *deploymentWorker) list(rw http.ResponseWriter) {
	dirs, err := os.ReadDir(w.root)
	if err != nil {
		log.Printf("deployment listing failed: %v", err)
		jsonError(rw, 500, "storage_error", "Unable to load deployments.")
		return
	}
	result := make([]deployment, 0)
	for _, entry := range dirs {
		if !entry.IsDir() || !idPattern.MatchString(entry.Name()) {
			continue
		}
		dir := filepath.Join(w.root, entry.Name())
		data, err := os.ReadFile(filepath.Join(dir, "result.json"))
		if errors.Is(err, os.ErrNotExist) {
			data, err = os.ReadFile(filepath.Join(dir, "deployment.json"))
		}
		if err != nil {
			log.Printf("deployment metadata read failed: %v", err)
			jsonError(rw, 500, "storage_error", "Unable to load deployments.")
			return
		}
		var d deployment
		if err := json.Unmarshal(data, &d); err != nil || d.ID != entry.Name() {
			log.Printf("deployment metadata is invalid for %s", entry.Name())
			jsonError(rw, 500, "storage_error", "Unable to load deployments.")
			return
		}
		result = append(result, d)
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(rw).Encode(result)
}

func writeTerraformConfig(dir string, req deploymentRequest, account, id string) error {
	variables, err := terraformVariables(req, account, id)
	if err != nil {
		return err
	}
	files, err := siteTerraform.ReadDir("terraform/site")
	if err != nil {
		return fmt.Errorf("read embedded Terraform templates: %w", err)
	}
	for _, file := range files {
		data, err := siteTerraform.ReadFile("terraform/site/" + file.Name())
		if err != nil {
			return fmt.Errorf("read Terraform template %s: %w", file.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(dir, file.Name()), data, 0600); err != nil {
			return fmt.Errorf("write Terraform template %s: %w", file.Name(), err)
		}
	}
	return os.WriteFile(filepath.Join(dir, "terraform.tfvars.json"), variables, 0600)
}

func terraformVariables(req deploymentRequest, account, id string) ([]byte, error) {
	if !accountPattern.MatchString(account) || !idPattern.MatchString(id) {
		return nil, errors.New("invalid deployment account or ID")
	}
	if err := validateDeployment(req); err != nil {
		return nil, err
	}
	escape := func(s string) string {
		return html.EscapeString(strings.TrimSpace(s))
	}
	title := escape(req.Name)
	content := "<main></main>"
	if req.Template == "business" {
		title = escape(req.BusinessName)
		var items strings.Builder
		for _, line := range strings.Split(req.Items, "\n") {
			if strings.TrimSpace(line) != "" {
				fmt.Fprintf(&items, "<li>%s</li>", escape(line))
			}
		}
		content = "<main><h1>" + title + "</h1><h2>" + escape(req.ListTitle) + "</h2><ul>" + items.String() + "</ul></main>"
	}
	page := `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>` + title +
		`</title><style>body{font-family:system-ui,sans-serif;max-width:48rem;margin:4rem auto;padding:0 1.5rem;color:#262a28}li{margin:.75rem 0}</style></head><body>` + content + `</body></html>`
	return json.MarshalIndent(map[string]string{
		"account_id": account, "deployment_id": id, "region": req.Region, "page_html": page,
	}, "", "  ")
}
