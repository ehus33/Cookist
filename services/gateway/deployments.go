package main

import (
	"bytes"
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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type deploymentRequest struct {
	Name              string                `json:"name"`
	Region            string                `json:"region"`
	Template          string                `json:"template"`
	Style             string                `json:"style"`
	BusinessName      string                `json:"businessName"`
	ListTitle         string                `json:"listTitle"`
	Items             string                `json:"items"`
	Products          []deploymentProduct   `json:"products,omitempty"`
	Blocks            []pageBlock           `json:"blocks"`
	FormOrigin        string                `json:"-"`
	DraftProducts     []builderProductDraft `json:"draftProducts,omitempty"`
	DraftDeploymentID string                `json:"draftDeploymentId,omitempty"`
	Dynamic           *dynamicConfig        `json:"dynamic,omitempty"`
}

type builderProductDraft struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Price       string `json:"price"`
	PaymentLink string `json:"paymentLink"`
}
type deploymentProduct struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	PriceCents  int64  `json:"priceCents"`
	PaymentLink string `json:"paymentLink"`
}

type deployment struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Region          string `json:"region"`
	Style           string `json:"style,omitempty"`
	Status          string `json:"status"`
	URL             string `json:"url,omitempty"`
	CreatedAt       string `json:"createdAt"`
	TenantID        string `json:"tenantId,omitempty"`
	CreatedBy       string `json:"createdBy,omitempty"`
	CanDelete       bool   `json:"canDelete"`
	CanEdit         bool   `json:"canEdit,omitempty"`
	CanData         bool   `json:"canData,omitempty"`
	LastUpdateError string `json:"lastUpdateError,omitempty"`
}

const maxTestDeployments = 4

type deploymentWorker struct {
	coreURL, owner, root, terraform, account string
	accessFile                               string
	localAccess                              bool
	selfServiceBilling                       bool
	billing                                  *stripeBilling
	publicOrigin                             string
	formRates                                map[string]submissionRate
	dynamic                                  *dynamicService
	client                                   *http.Client
	mu                                       sync.Mutex
	execute                                  func(context.Context, string, io.Writer, ...string) ([]byte, error)
}

var regionPattern = regexp.MustCompile(`^[a-z]{2}(?:-[a-z]+)+-[0-9]+$`)
var accountPattern = regexp.MustCompile(`^[0-9]{12}$`)
var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var websiteURLPattern = regexp.MustCompile(`^https://[a-z0-9-]+\.cloudfront\.net$`)
var paymentLinkPattern = regexp.MustCompile(`^https://buy\.stripe\.com/(?:test_)?[A-Za-z0-9]+$`)

//go:embed terraform/site/*.tf terraform/site/.terraform.lock.hcl
var siteTerraform embed.FS

//go:embed site_styles.css
var siteStylesCSS string

var siteFaviconURL = "data:image/svg+xml," + url.PathEscape(
	`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><rect width="32" height="32" rx="8" fill="#262a28"/><path d="M9 10h14M9 16h14M9 22h9" fill="none" stroke="#fff" stroke-width="3" stroke-linecap="round"/></svg>`)

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
		if args[0] == "output" || args[0] == "show" {
			return cmd.Output()
		}
		cmd.Stdout = logFile
		return nil, cmd.Run()
	}
	return w, nil
}

func (w *deploymentWorker) authorize(r *http.Request) (deploymentAccess, error) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || len(auth) > 16384 {
		return deploymentAccess{}, &deploymentError{http.StatusUnauthorized, "unauthorized", "Sign in to continue."}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(w.coreURL, "/")+"/api/me", nil)
	if err != nil {
		return deploymentAccess{}, err
	}
	req.Header.Set("Authorization", auth)
	res, err := w.client.Do(req)
	if err != nil {
		return deploymentAccess{}, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		return deploymentAccess{}, &deploymentError{http.StatusUnauthorized, "unauthorized", "Sign in to continue."}
	}
	if res.StatusCode != http.StatusOK {
		return deploymentAccess{}, fmt.Errorf("identity service returned %d", res.StatusCode)
	}
	var identity struct {
		ActorID string `json:"actorId"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&identity); err != nil {
		return deploymentAccess{}, err
	}
	if identity.ActorID == "" {
		return deploymentAccess{}, errors.New("identity service returned an empty actor ID")
	}
	return w.accessFor(identity.ActorID)
}

type deploymentError struct {
	status        int
	code, message string
}

func (e *deploymentError) Error() string { return e.message }

func (w *deploymentWorker) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	access, err := w.authorize(r)
	if err != nil {
		var apiErr *deploymentError
		if errors.As(err, &apiErr) {
			jsonError(rw, apiErr.status, apiErr.code, apiErr.message)
		} else {
			log.Printf("deployment authorization unavailable: %v", err)
			jsonError(rw, 503, "auth_unavailable", "Deployment authorization is unavailable.")
		}
		return
	}
	if r.URL.Path == "/api/deployment-access" {
		if r.Method != http.MethodGet {
			rw.Header().Set("Allow", "GET")
			jsonError(rw, 405, "method_not_allowed", "Use GET.")
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		rw.Header().Set("Cache-Control", "no-store")
		access.DynamicEnabled = w.dynamic != nil
		access.DatabaseBillingAvailable = w.billing != nil && (w.billing.databasePrice != "" || w.billing.usage != nil)
		_ = json.NewEncoder(rw).Encode(access)
		return
	}
	if w.dynamicAdminRoute(rw, r, access) || w.builderDesignRoute(rw, r, access) || w.submissionsRoute(rw, r, access) {
		return
	}
	action := "list"
	if r.Method == http.MethodPost {
		action = "create"
	} else if r.Method == http.MethodDelete {
		action = "delete"
	}
	if err := access.check(action, nil); err != nil {
		var apiErr *deploymentError
		if errors.As(err, &apiErr) {
			jsonError(rw, apiErr.status, apiErr.code, apiErr.message)
		}
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/deployments/") {
		id := strings.TrimPrefix(r.URL.Path, "/api/deployments/")
		if !idPattern.MatchString(id) {
			jsonError(rw, 404, "not_found", "Deployment not found.")
		} else if r.Method != http.MethodDelete {
			rw.Header().Set("Allow", "DELETE")
			jsonError(rw, 405, "method_not_allowed", "Use DELETE.")
		} else {
			w.delete(rw, id, access)
		}
		return
	}
	if r.URL.Path != "/api/deployments" {
		jsonError(rw, 404, "not_found", "Route not found.")
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.list(rw, access)
	case http.MethodPost:
		w.create(rw, r, access)
	default:
		rw.Header().Set("Allow", "GET, POST")
		jsonError(rw, 405, "method_not_allowed", "Use GET or POST.")
	}
}

func validateDeployment(req deploymentRequest) error {
	if len(req.DraftProducts) != 0 || req.DraftDeploymentID != "" {
		return errors.New("Draft-only product fields cannot be published.")
	}
	switch req.Style {
	case "", "classic", "modern", "warm", "bold":
	default:
		return errors.New("Select a supported site style.")
	}
	if strings.TrimSpace(req.Name) == "" || len(req.Name) > 320 || !regionPattern.MatchString(req.Region) ||
		(req.Template != "blank" && req.Template != "business" && req.Template != "storefront" && req.Template != "builder") || len(req.BusinessName) > 320 ||
		len(req.ListTitle) > 320 || len(req.Items) > 8000 ||
		(req.Template == "business" && (strings.TrimSpace(req.BusinessName) == "" || strings.TrimSpace(req.ListTitle) == "")) {
		return errors.New("Complete the required fields and select a valid template and region.")
	}
	if req.Template == "builder" {
		if err := validatePageBlocks(req.Blocks); err != nil {
			return err
		}
		if err := validateDynamicConfig(req.Dynamic); err != nil {
			return err
		}
		if err := validateDynamicBlocks(req); err != nil {
			return err
		}
	} else if req.Dynamic != nil {
		return errors.New("Dynamic collections require the visual builder template.")
	} else if len(req.Blocks) > 0 {
		return errors.New("Page elements require the visual builder template.")
	}
	usesProducts := req.Template == "storefront" || (req.Template == "builder" && hasProductCatalog(req.Blocks))
	if !usesProducts && len(req.Products) != 0 {
		return errors.New("Products require the storefront template.")
	}
	if usesProducts {
		if strings.TrimSpace(req.BusinessName) == "" || len(req.Products) < 1 || len(req.Products) > 20 {
			return errors.New("Enter a store name and between 1 and 20 products.")
		}
		for i, product := range req.Products {
			if strings.TrimSpace(product.Name) == "" || len(product.Name) > 320 ||
				len(product.Description) > 2000 || product.PriceCents < 1 || product.PriceCents > 99999999 {
				return fmt.Errorf("Product %d requires a name and a USD price between $0.01 and $999999.99.", i+1)
			}
			if len(product.PaymentLink) > 200 || !paymentLinkPattern.MatchString(product.PaymentLink) {
				return fmt.Errorf("Product %d requires a plain https://buy.stripe.com/ Stripe Payment Link without query parameters.", i+1)
			}
		}
	}
	return nil
}

func (w *deploymentWorker) create(rw http.ResponseWriter, r *http.Request, access deploymentAccess) {
	data, err := io.ReadAll(http.MaxBytesReader(rw, r.Body, builderRequestLimit))
	if err != nil {
		jsonError(rw, 400, "invalid_request", "Invalid deployment request.")
		return
	}
	var request deploymentRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		jsonError(rw, 400, "invalid_request", "Invalid deployment request.")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		jsonError(rw, 400, "invalid_request", "Invalid deployment request.")
		return
	}
	if request.Dynamic != nil {
		request, err = decodeBuilderRequest(data)
		if err != nil {
			jsonError(rw, 400, "invalid_request", err.Error())
			return
		}
	}
	if err := validateDeployment(request); err != nil {
		jsonError(rw, 400, "invalid_request", err.Error())
		return
	}
	request.FormOrigin = w.publicOrigin
	if request.Dynamic != nil && (w.dynamic == nil || !validFormOrigin(w.publicOrigin)) {
		jsonError(rw, 503, "dynamic_unavailable", "Dynamic apps require a configured database, visitor identity pool and HTTPS public origin.")
		return
	}
	if request.Dynamic != nil && !w.requireDatabase(rw, access.TenantID) {
		return
	}
	if hasLeadForm(request.Blocks) && !validFormOrigin(request.FormOrigin) {
		jsonError(rw, 503, "forms_unavailable", "Lead forms require a configured HTTPS public origin.")
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.acquireOperation(); err != nil {
		w.operationError(rw, err)
		return
	}
	started := false
	defer func() {
		if !started {
			w.releaseOperation()
		}
	}()
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
	count, tenantCount := 0, 0
	for _, entry := range entries {
		name := entry.Name()
		pendingID := strings.TrimSuffix(strings.TrimPrefix(name, "."), ".pending")
		if idPattern.MatchString(name) || (name == "."+pendingID+".pending" && idPattern.MatchString(pendingID)) {
			count++
			if access.Role != "platform_owner" {
				d, readErr := readDeployment(filepath.Join(w.root, name), name)
				if readErr == nil {
					d, readErr = w.ownedDeployment(d)
				}
				if readErr != nil {
					log.Printf("tenant quota metadata check failed: %v", readErr)
					w.removeLock()
					jsonError(rw, 500, "storage_error", "Unable to verify deployment ownership; operator recovery is required.")
					return
				}
				if d.TenantID == access.TenantID {
					tenantCount++
				}
			}
		}
		if access.Role != "platform_owner" && tenantCount >= access.Limit {
			w.removeLock()
			jsonError(rw, 429, "tenant_deployment_limit", "Your organization has reached its deployment limit. Clean up an existing deployment before creating another.")
			return
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
		Region: request.Region, Style: request.Style, Status: "running", CreatedAt: time.Now().UTC().Format(time.RFC3339),
		TenantID: access.TenantID, CreatedBy: access.ActorID}
	dir := filepath.Join(w.root, d.ID)
	staging := filepath.Join(w.root, "."+d.ID+".pending")
	if err = os.Mkdir(staging, 0700); err == nil {
		err = writeTerraformConfig(staging, request, w.account, d.ID)
		if err == nil && request.Template == "builder" {
			err = saveBuilderDesign(staging, request)
		}
		if err == nil {
			err = saveDeployment(staging, d)
		}
		if err == nil {
			err = os.WriteFile(filepath.Join(staging, ".never-applied"), nil, 0600)
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
	if err = os.WriteFile(lock, []byte(d.ID), 0600); err != nil {
		log.Printf("deployment lock owner could not be saved: %v", err)
		jsonError(rw, 500, "storage_error", "Unable to start the deployment; operator recovery is required.")
		return
	}
	started = true
	go w.run(dir, d)
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	rw.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(rw).Encode(d)
}

// This separate lock serializes cleanup with creation, including recovery of a
// completed failed apply whose worker lock is intentionally still present.
func (w *deploymentWorker) acquireOperation() error {
	f, err := os.OpenFile(filepath.Join(w.root, ".operation.lock"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	return f.Close()
}

func (w *deploymentWorker) releaseOperation() {
	if err := os.Remove(filepath.Join(w.root, ".operation.lock")); err != nil {
		log.Printf("deployment operation lock removal failed: %v", err)
	}
}

func (w *deploymentWorker) operationError(rw http.ResponseWriter, err error) {
	if errors.Is(err, os.ErrExist) {
		jsonError(rw, 409, "worker_busy", "A deployment operation is running or needs operator recovery.")
		return
	}
	log.Printf("deployment operation lock failed: %v", err)
	jsonError(rw, 500, "storage_error", "Deployment storage is unavailable; operator recovery is required.")
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
	defer w.releaseOperation()
	logFile, err := os.OpenFile(filepath.Join(dir, "terraform.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err == nil {
		defer logFile.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
		defer cancel()
		if w.billing != nil && w.billing.usage != nil {
			design, readErr := readBuilderDesign(dir)
			if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				err = readErr
			}
			if err == nil {
				err = w.prepareUsageDatabase(ctx, d.TenantID, design.Dynamic)
			}
		}
		for _, args := range [][]string{{"init", "-input=false", "-no-color"}, {"apply", "-input=false", "-auto-approve", "-no-color"}} {
			if err != nil {
				break
			}
			if args[0] == "apply" {
				if err = os.Remove(filepath.Join(dir, ".never-applied")); err != nil {
					break
				}
			}
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
	w.mu.Lock()
	defer w.mu.Unlock()
	if err = saveDeployment(dir, d); err != nil {
		log.Printf("deployment %s status could not be saved: %v; lock retained", d.ID, err)
		return
	}
	if d.Status == "ready" {
		w.removeLock()
	}
}

func readDeployment(dir, id string) (deployment, error) {
	var d deployment
	info, err := os.Lstat(dir)
	if err != nil {
		return d, err
	}
	if !info.IsDir() {
		return d, errors.New("deployment directory is invalid")
	}
	data, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if errors.Is(err, os.ErrNotExist) {
		data, err = os.ReadFile(filepath.Join(dir, "deployment.json"))
	}
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return d, err
	}
	if d.ID != id || (d.Status != "running" && d.Status != "ready" && d.Status != "failed" &&
		d.Status != "deleting" && d.Status != "cleanup_failed") {
		return d, errors.New("deployment metadata is invalid")
	}
	return d, nil
}

func (w *deploymentWorker) delete(rw http.ResponseWriter, id string, access deploymentAccess) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.acquireOperation(); err != nil {
		w.operationError(rw, err)
		return
	}
	started := false
	defer func() {
		if !started {
			w.releaseOperation()
		}
	}()
	dir := filepath.Join(w.root, id)
	d, err := readDeployment(dir, id)
	if err == nil {
		d, err = w.ownedDeployment(d)
	}
	if errors.Is(err, os.ErrNotExist) {
		jsonError(rw, 404, "not_found", "Deployment not found.")
		return
	}
	if err != nil {
		log.Printf("deployment cleanup metadata failed: %v", err)
		jsonError(rw, 500, "storage_error", "Unable to read deployment state; operator recovery is required.")
		return
	}
	if err := access.check("delete", &d); err != nil {
		var apiErr *deploymentError
		if errors.As(err, &apiErr) {
			jsonError(rw, apiErr.status, apiErr.code, apiErr.message)
		}
		return
	}
	if d.Status != "ready" && d.Status != "failed" && d.Status != "cleanup_failed" {
		jsonError(rw, 409, "worker_busy", "Only completed deployments can be deleted. Interrupted jobs require operator recovery.")
		return
	}
	if w.dynamic == nil {
		design, designErr := readBuilderDesign(dir)
		if designErr != nil && !errors.Is(designErr, os.ErrNotExist) {
			log.Printf("cleanup design lookup failed: %v", designErr)
			jsonError(rw, 500, "storage_error", "Unable to inspect app data before cleanup; operator recovery is required.")
			return
		}
		if designErr == nil && design.Dynamic != nil {
			jsonError(rw, 503, "dynamic_unavailable", "Configure the app database before cleanup so managed records are not orphaned.")
			return
		}
	}
	lock := filepath.Join(w.root, ".worker.lock")
	owner, err := os.ReadFile(lock)
	if err == nil {
		if !w.canRecoverLock(id, string(owner)) {
			jsonError(rw, 409, "worker_busy", "Another deployment is running or needs operator recovery.")
			return
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		log.Printf("deployment cleanup lock read failed: %v", err)
		jsonError(rw, 500, "storage_error", "Unable to read the worker lock.")
		return
	} else {
		if !w.canRecoverLock(id, id) {
			jsonError(rw, 409, "worker_busy", "Another deployment is running or needs operator recovery.")
			return
		}
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			w.operationError(rw, err)
			return
		}
		_, writeErr := f.WriteString(id)
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			log.Printf("deployment cleanup lock write failed: %v, %v", writeErr, closeErr)
			jsonError(rw, 500, "storage_error", "Unable to save the worker lock; operator recovery is required.")
			return
		}
	}
	d.Status = "deleting"
	d.URL = ""
	if err = saveDeployment(dir, d); err != nil {
		log.Printf("deployment cleanup status failed: %v", err)
		jsonError(rw, 500, "storage_error", "Unable to save cleanup status; operator recovery is required.")
		return
	}
	started = true
	go w.destroy(dir, d)
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	rw.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(rw).Encode(d)
}

func (w *deploymentWorker) canRecoverLock(id, owner string) bool {
	if owner != "" && owner != id {
		return false
	}
	entries, err := os.ReadDir(w.root)
	if err != nil {
		log.Printf("deployment recovery scan failed: %v", err)
		return false
	}
	failed := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".pending") {
			return false
		}
		if !idPattern.MatchString(entry.Name()) {
			continue
		}
		d, err := readDeployment(filepath.Join(w.root, entry.Name()), entry.Name())
		if err != nil || d.Status == "running" || d.Status == "deleting" {
			return false
		}
		if d.Status == "failed" || d.Status == "cleanup_failed" {
			failed++
			if owner == "" && d.ID != id {
				return false
			}
		}
	}
	// Older workers wrote empty locks; recover only the sole failed job.
	return owner == id || failed == 1
}

func readTerraformState(dir string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, "terraform.tfstate"))
	if err != nil {
		return false, err
	}
	var state struct {
		Version   int `json:"version"`
		Resources []struct {
			Mode string `json:"mode"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return false, err
	}
	if state.Version != 4 || state.Resources == nil {
		return false, errors.New("Terraform state is invalid")
	}
	for _, resource := range state.Resources {
		if resource.Mode != "data" {
			return false, nil
		}
	}
	return true, nil
}

func (w *deploymentWorker) destroy(dir string, d deployment) {
	defer w.releaseOperation()
	err := w.destroyResources(dir)
	w.mu.Lock()
	defer w.mu.Unlock()
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		err = w.dynamicDeleteApp(ctx, d.ID)
		cancel()
	}
	if err == nil {
		err = os.RemoveAll(dir)
	}
	if err != nil {
		log.Printf("deployment %s cleanup failed: %v; state and worker lock retained", d.ID, err)
		d.Status = "cleanup_failed"
		if saveErr := saveDeployment(dir, d); saveErr != nil {
			log.Printf("deployment %s cleanup status could not be saved: %v", d.ID, saveErr)
		}
		return
	}
	w.removeLock()
}

func (w *deploymentWorker) destroyResources(dir string) error {
	_, err := readTerraformState(dir)
	if errors.Is(err, os.ErrNotExist) {
		_, markerErr := os.Stat(filepath.Join(dir, ".never-applied"))
		_, backupErr := os.Stat(filepath.Join(dir, "terraform.tfstate.backup"))
		if markerErr == nil && errors.Is(backupErr, os.ErrNotExist) {
			return nil
		}
		return errors.New("Terraform state is missing; operator recovery is required")
	}
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(dir, "terraform.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Minute)
	defer cancel()
	for _, args := range [][]string{{"init", "-input=false", "-lockfile=readonly", "-no-color"},
		{"destroy", "-input=false", "-auto-approve", "-no-color"}} {
		if _, err := w.execute(ctx, dir, logFile, args...); err != nil {
			return err
		}
	}
	empty, err := readTerraformState(dir)
	if err != nil {
		return err
	}
	if !empty {
		return errors.New("Terraform destroy left managed resources in state")
	}
	return nil
}

func (w *deploymentWorker) list(rw http.ResponseWriter, access deploymentAccess) {
	w.mu.Lock()
	defer w.mu.Unlock()
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
		d, err := readDeployment(dir, entry.Name())
		if err == nil {
			d, err = w.ownedDeployment(d)
		}
		if err != nil {
			log.Printf("deployment metadata read failed: %v", err)
			jsonError(rw, 500, "storage_error", "Unable to load deployments.")
			return
		}
		if access.Role == "platform_owner" || d.TenantID == access.TenantID {
			d.CanDelete = access.CanDelete
			_, designErr := os.Stat(filepath.Join(w.root, d.ID, "design.json"))
			if designErr != nil && !errors.Is(designErr, os.ErrNotExist) {
				log.Printf("saved design lookup failed: %v", designErr)
				jsonError(rw, 500, "storage_error", "Unable to inspect saved site designs.")
				return
			}
			d.CanEdit = access.CanCreate && d.Status == "ready" && designErr == nil
			d.CanData = false
			if w.dynamic != nil && access.CanDelete && d.Status == "ready" && designErr == nil {
				design, err := readBuilderDesign(dir)
				if err != nil {
					log.Printf("dynamic design lookup failed: %v", err)
					jsonError(rw, 500, "storage_error", "Unable to inspect dynamic app configuration.")
					return
				}
				d.CanData = design.Dynamic != nil
			}
			if access.PaymentRequired {
				d.URL = ""
			}
			result = append(result, d)
		}
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
	catalog := ""
	if req.Template == "storefront" || (req.Template == "builder" && hasProductCatalog(req.Blocks)) {
		var products strings.Builder
		for _, product := range req.Products {
			fmt.Fprintf(&products, `<li><h3>%s</h3><p class="store-description">%s</p><p class="store-price">$%d.%02d USD</p>`,
				escape(product.Name), escape(product.Description), product.PriceCents/100, product.PriceCents%100)
			if strings.HasPrefix(product.PaymentLink, "https://buy.stripe.com/test_") {
				products.WriteString(`<p class="store-test-notice">Test checkout - no real charge</p>`)
			}
			fmt.Fprintf(&products, `<a class="store-buy" href="%s" rel="noreferrer" aria-label="Buy %s">Buy now</a></li>`,
				escape(product.PaymentLink), escape(product.Name))
		}
		catalog = `<p class="store-checkout-note">Secure checkout by Stripe. Final price, availability, shipping and taxes are confirmed at checkout.</p><ul class="store-products">` +
			products.String() + "</ul>"
	}
	if req.Template == "storefront" {
		title = escape(req.BusinessName)
		content = "<main><h1>" + title + "</h1>" + catalog + "</main>"
	}
	if req.Template == "builder" {
		if hasLeadForm(req.Blocks) && !validFormOrigin(req.FormOrigin) {
			return nil, errors.New("Lead forms require a configured HTTPS public origin.")
		}
		content = renderPageBlocksWithForms(req.Blocks, catalog, strings.TrimRight(req.FormOrigin, "/")+"/forms/"+id)
		if req.Dynamic != nil {
			if !validFormOrigin(req.FormOrigin) {
				return nil, errors.New("Dynamic apps require a configured HTTPS public origin.")
			}
			content += renderDynamicRuntime(req.FormOrigin, id)
		}
	}
	css := `body{font-family:system-ui,sans-serif;max-width:48rem;margin:4rem auto;padding:0 1.5rem;color:#262a28}li{margin:.75rem 0}`
	body := "<body>"
	if (req.Style != "" && req.Style != "classic") || req.Template == "storefront" || req.Template == "builder" {
		css = siteStylesCSS
		style := req.Style
		if style == "" {
			style = "classic"
		}
		body = `<body class="generated-site site-style-` + style + `">`
	}
	policy := ""
	if req.Dynamic != nil {
		policy = dynamicContentPolicy(req.FormOrigin)
	}
	page := `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">` + policy + `<link rel="icon" type="image/svg+xml" href="` + siteFaviconURL + `"><title>` + title +
		`</title><style>` + css + `</style></head>` + body + content + `</body></html>`
	return json.MarshalIndent(map[string]string{
		"account_id": account, "deployment_id": id, "region": req.Region, "page_html": page,
	}, "", "  ")
}
