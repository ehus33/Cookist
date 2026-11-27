package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const builderRequestLimit = 524288

func builderJSON(rw http.ResponseWriter, status int, value any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(value)
}

// Check types that encoding/json otherwise silently accepts, such as null for
// strings or booleans, and reject ambiguous duplicate object members.
func checkBuilderJSON(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return errors.New("Null values are not supported; omit optional fields instead.")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("Duplicate or invalid JSON field.")
			}
			seen[name] = true
			if err := checkBuilderJSON(dec); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := checkBuilderJSON(dec); err != nil {
				return err
			}
		}
	default:
		return errors.New("Invalid JSON structure.")
	}
	_, err = dec.Token()
	return err
}

func decodeBuilderRequest(data []byte) (deploymentRequest, error) {
	var req deploymentRequest
	if len(data) > builderRequestLimit {
		return req, errors.New("Builder request exceeds the 512 KiB size limit.")
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return req, errors.New("Builder request must be a JSON object.")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := checkBuilderJSON(dec); err != nil {
		return req, fmt.Errorf("Invalid builder JSON: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return req, errors.New("Builder request contains trailing JSON or data.")
	}
	dec = json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, fmt.Errorf("Invalid builder field or type: %w", err)
	}
	return req, nil
}

func readBuilderRequest(rw http.ResponseWriter, r *http.Request) (deploymentRequest, error) {
	data, err := io.ReadAll(http.MaxBytesReader(rw, r.Body, builderRequestLimit))
	if err != nil {
		return deploymentRequest{}, errors.New("Unable to read builder request; maximum size is 512 KiB.")
	}
	return decodeBuilderRequest(data)
}

func validateBuilderDraft(req deploymentRequest) error {
	if req.Template != "builder" {
		return errors.New("Saved drafts require the builder template.")
	}
	if req.DraftDeploymentID != "" && !idPattern.MatchString(req.DraftDeploymentID) {
		return errors.New("The draft edit target must be a valid deployment ID.")
	}
	if len(req.Name) > 320 || len(req.BusinessName) > 320 || len(req.ListTitle) > 320 ||
		len(req.Items) > 8000 || (req.Region != "" && !regionPattern.MatchString(req.Region)) {
		return errors.New("Draft settings exceed the allowed bounds or contain an invalid region.")
	}
	switch req.Style {
	case "", "classic", "modern", "warm", "bold":
	default:
		return errors.New("Select a supported site style.")
	}
	if len(req.Blocks) > 40 || len(req.Products) > 20 || len(req.DraftProducts) > 20 {
		return errors.New("Drafts allow at most 40 elements and 20 products.")
	}
	if err := validateDynamicConfig(req.Dynamic); err != nil {
		return err
	}
	ids := map[string]bool{}
	catalogs := 0
	for i, block := range req.Blocks {
		if block.Beside && (i == 0 || req.Blocks[i-1].Beside) {
			return errors.New("A row can contain at most two elements.")
		}
		if !pageBlockIDPattern.MatchString(block.ID) || ids[block.ID] ||
			(block.Align != "left" && block.Align != "center" && block.Align != "right") ||
			(block.Size != "small" && block.Size != "medium" && block.Size != "large") ||
			len(block.Text) > 8000 || len(block.URL) > 2000 || len(block.Alt) > 640 || len(block.Collection) > 32 {
			return fmt.Errorf("Element %d has invalid draft settings.", i+1)
		}
		ids[block.ID] = true
		switch block.Type {
		case "heading", "button", "contact", "waitlist", "data-list", "data-form", "member-account":
			if len(block.Text) > 320 {
				return fmt.Errorf("Element %d text exceeds the allowed length.", i+1)
			}
		case "text", "image", "divider", "spacer":
		case "products":
			catalogs++
		default:
			return fmt.Errorf("Element %d has an unsupported type.", i+1)
		}
	}
	if catalogs > 1 {
		return errors.New("Use only one product catalog per page.")
	}
	for i, product := range req.Products {
		if len(product.Name) > 320 || len(product.Description) > 2000 ||
			len(product.PaymentLink) > 200 || product.PriceCents < 0 || product.PriceCents > 99999999 {
			return fmt.Errorf("Product %d exceeds the allowed draft bounds.", i+1)
		}
	}
	for i, product := range req.DraftProducts {
		if len(product.Name) > 320 || len(product.Description) > 2000 ||
			len(product.Price) > 64 || len(product.PaymentLink) > 2000 {
			return fmt.Errorf("Product %d exceeds the allowed draft editor bounds.", i+1)
		}
	}
	return nil
}

func atomicBuilderFile(filename string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(filename), "."+filepath.Base(filename)+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, filename)
}

func saveBuilderDesign(dir string, req deploymentRequest) error {
	if req.Template != "builder" {
		return nil
	}
	if err := validateDeployment(req); err != nil {
		return err
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	return atomicBuilderFile(filepath.Join(dir, "design.json"), data)
}

func readBuilderDesign(dir string) (deploymentRequest, error) {
	var req deploymentRequest
	f, err := os.Open(filepath.Join(dir, "design.json"))
	if err != nil {
		return req, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, builderRequestLimit+1))
	if err == nil {
		req, err = decodeBuilderRequest(data)
	}
	if err == nil {
		err = validateDeployment(req)
	}
	if err == nil && req.Template != "builder" {
		err = errors.New("saved design is not a builder site")
	}
	return req, err
}

func (w *deploymentWorker) builderDesignRoute(rw http.ResponseWriter, r *http.Request, access deploymentAccess) bool {
	if r.URL.Path == "/api/builder-draft" {
		w.builderDraft(rw, r, access)
		return true
	}
	const prefix = "/api/deployments/"
	if !strings.HasPrefix(r.URL.Path, prefix) || !strings.HasSuffix(r.URL.Path, "/design") {
		return false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), "/design")
	if !idPattern.MatchString(id) {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPatch {
		rw.Header().Set("Allow", "GET, PATCH")
		jsonError(rw, 405, "method_not_allowed", "Use GET or PATCH.")
		return true
	}
	w.builderDeploymentDesign(rw, r, access, id)
	return true
}

func (w *deploymentWorker) builderDraft(rw http.ResponseWriter, r *http.Request, access deploymentAccess) {
	if access.ActorID == "" {
		jsonError(rw, 401, "unauthorized", "Sign in to save builder drafts.")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPut && r.Method != http.MethodDelete {
		rw.Header().Set("Allow", "GET, PUT, DELETE")
		jsonError(rw, 405, "method_not_allowed", "Use GET, PUT or DELETE.")
		return
	}
	identity, _ := json.Marshal([]string{access.TenantID, access.ActorID})
	digest := sha256.Sum256(identity)
	dir := filepath.Join(w.root, ".drafts")
	filename := filepath.Join(dir, fmt.Sprintf("%x.json", digest))
	w.mu.Lock()
	defer w.mu.Unlock()
	switch r.Method {
	case http.MethodGet:
		f, err := os.Open(filename)
		if errors.Is(err, os.ErrNotExist) {
			builderJSON(rw, 200, map[string]any{"draft": nil})
			return
		}
		if err == nil {
			defer f.Close()
			data, readErr := io.ReadAll(io.LimitReader(f, builderRequestLimit+1))
			err = readErr
			var req deploymentRequest
			if err == nil {
				req, err = decodeBuilderRequest(data)
			}
			if err == nil {
				err = validateBuilderDraft(req)
			}
			if err == nil {
				if req.Blocks == nil {
					req.Blocks = []pageBlock{}
				}
				builderJSON(rw, 200, map[string]any{"draft": req})
				return
			}
		}
		jsonError(rw, 500, "storage_error", "Unable to read the saved builder draft.")
	case http.MethodPut:
		req, err := readBuilderRequest(rw, r)
		if err == nil {
			err = validateBuilderDraft(req)
		}
		if err != nil {
			jsonError(rw, 400, "invalid_draft", err.Error())
			return
		}
		if req.Blocks == nil {
			req.Blocks = []pageBlock{}
		}
		if req.DraftDeploymentID != "" {
			d, err := readDeployment(filepath.Join(w.root, req.DraftDeploymentID), req.DraftDeploymentID)
			if err == nil {
				d, err = w.ownedDeployment(d)
			}
			if errors.Is(err, os.ErrNotExist) {
				jsonError(rw, 404, "not_found", "The draft edit target was not found.")
				return
			}
			if err == nil {
				err = access.check("list", &d)
			}
			if err == nil {
				err = access.check("create", &d)
			}
			if err != nil {
				builderAccessError(rw, err)
				return
			}
		}
		data, err := json.Marshal(req)
		if err == nil {
			err = os.MkdirAll(dir, 0700)
		}
		if err == nil {
			err = atomicBuilderFile(filename, data)
		}
		if err != nil {
			jsonError(rw, 500, "storage_error", "Unable to save the builder draft.")
			return
		}
		builderJSON(rw, 200, map[string]any{"draft": req})
	case http.MethodDelete:
		if err := os.Remove(filename); err != nil && !errors.Is(err, os.ErrNotExist) {
			jsonError(rw, 500, "storage_error", "Unable to delete the builder draft.")
			return
		}
		builderJSON(rw, 200, map[string]any{"draft": nil})
	}
}

func builderAccessError(rw http.ResponseWriter, err error) {
	var apiErr *deploymentError
	if errors.As(err, &apiErr) {
		jsonError(rw, apiErr.status, apiErr.code, apiErr.message)
	} else {
		jsonError(rw, 500, "storage_error", "Unable to verify deployment ownership.")
	}
}

func (w *deploymentWorker) builderDeploymentDesign(rw http.ResponseWriter, r *http.Request, access deploymentAccess, id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := access.check("list", nil); err != nil {
		builderAccessError(rw, err)
		return
	}
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
		builderAccessError(rw, err)
		return
	}
	if err := access.check("list", &d); err != nil {
		builderAccessError(rw, err)
		return
	}
	saved, err := readBuilderDesign(dir)
	if errors.Is(err, os.ErrNotExist) {
		jsonError(rw, 409, "design_unavailable", "This legacy site has no saved builder design and cannot be edited.")
		return
	}
	if err != nil || saved.Region != d.Region {
		jsonError(rw, 500, "storage_error", "The saved builder design is invalid; operator recovery is required.")
		return
	}
	if r.Method == http.MethodGet {
		builderJSON(rw, 200, saved)
		return
	}
	if err := access.check("create", &d); err != nil {
		builderAccessError(rw, err)
		return
	}
	if d.Status != "ready" {
		jsonError(rw, 409, "worker_busy", "Only ready builder sites can be edited.")
		return
	}
	req, err := readBuilderRequest(rw, r)
	if err == nil {
		err = validateDeployment(req)
	}
	if err != nil {
		jsonError(rw, 400, "invalid_request", err.Error())
		return
	}
	if req.Template != "builder" || req.Region != d.Region {
		jsonError(rw, 400, "immutable_settings", "The builder template and deployment region cannot be changed.")
		return
	}
	req.FormOrigin = w.publicOrigin
	if err := validateDynamicUpdate(saved.Dynamic, req.Dynamic); err != nil {
		jsonError(rw, 409, "immutable_collections", err.Error())
		return
	}
	if req.Dynamic != nil && (w.dynamic == nil || !validFormOrigin(w.publicOrigin)) {
		jsonError(rw, 503, "dynamic_unavailable", "Dynamic apps require configured database and visitor authentication.")
		return
	}
	if req.Dynamic != nil && !w.requireDatabase(rw, access.TenantID) {
		return
	}
	variables, err := terraformVariables(req, w.account, id)
	if err != nil {
		jsonError(rw, 400, "invalid_request", err.Error())
		return
	}
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
	lock, err := os.OpenFile(filepath.Join(w.root, ".worker.lock"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		w.operationError(rw, err)
		return
	}
	_, err = lock.WriteString(id)
	closeErr := lock.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		jsonError(rw, 500, "storage_error", "Unable to save the update lock; operator recovery is required.")
		return
	}
	running := d
	running.Status, running.LastUpdateError, running.CanEdit = "running", "", false
	if err := saveBuilderUpdateMetadata(dir, running); err != nil {
		jsonError(rw, 500, "storage_error", "Unable to save update status; operator recovery is required.")
		return
	}
	started = true
	go w.runBuilderUpdate(dir, d, req, variables)
	builderJSON(rw, http.StatusAccepted, running)
}

// Updates must replace result.json even while running: readDeployment prefers
// it over deployment.json, which contains the original creation metadata.
func saveBuilderUpdateMetadata(dir string, d deployment) error {
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return atomicBuilderFile(filepath.Join(dir, "result.json"), data)
}

func validateBuilderPlan(data []byte) error {
	var plan struct {
		FormatVersion string            `json:"format_version"`
		Complete      *bool             `json:"complete"`
		Errored       bool              `json:"errored"`
		Deferred      []json.RawMessage `json:"deferred_changes"`
		Changes       []struct {
			Address string `json:"address"`
			Mode    string `json:"mode"`
			Type    string `json:"type"`
			Name    string `json:"name"`
			Change  struct {
				Actions      []string       `json:"actions"`
				Before       map[string]any `json:"before"`
				After        map[string]any `json:"after"`
				AfterUnknown map[string]any `json:"after_unknown"`
			} `json:"change"`
		} `json:"resource_changes"`
		Drift []struct {
			Change struct {
				Actions []string `json:"actions"`
			} `json:"change"`
		} `json:"resource_drift"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&plan); err != nil {
		return errors.New("Terraform returned an invalid update plan.")
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF ||
		!strings.HasPrefix(plan.FormatVersion, "1.") || plan.Changes == nil ||
		plan.Errored || (plan.Complete != nil && !*plan.Complete) || len(plan.Deferred) != 0 {
		return errors.New("Terraform returned an incomplete or unsupported update plan.")
	}
	for _, drift := range plan.Drift {
		if len(drift.Change.Actions) != 1 || drift.Change.Actions[0] != "no-op" {
			return errors.New("Infrastructure drift requires operator recovery before editing.")
		}
	}
	for _, resource := range plan.Changes {
		change := resource.Change
		if len(change.Actions) == 1 && change.Actions[0] == "no-op" {
			continue
		}
		if len(change.Actions) != 1 || change.Actions[0] != "update" ||
			resource.Address != "aws_s3_object.index" || resource.Mode != "managed" ||
			resource.Type != "aws_s3_object" || resource.Name != "index" {
			return errors.New("Update rejected: only an in-place update to the existing page object is allowed.")
		}
		for _, field := range []string{"bucket", "key"} {
			before, beforeOK := change.Before[field].(string)
			after, afterOK := change.After[field].(string)
			if !beforeOK || !afterOK || before == "" || before != after ||
				(change.AfterUnknown[field] != nil && change.AfterUnknown[field] != false) {
				return errors.New("Update rejected: the page bucket and key must remain identical and known.")
			}
		}
	}
	return nil
}

func (w *deploymentWorker) runBuilderUpdate(dir string, original deployment, req deploymentRequest, variables []byte) {
	defer w.releaseOperation()
	uncertain := false
	stage := "prepare"
	err := func() error {
		candidate, err := os.CreateTemp(dir, ".builder-candidate-*.json")
		if err != nil {
			return err
		}
		candidateName := candidate.Name()
		planName := candidateName + ".tfplan"
		defer os.Remove(candidateName)
		defer os.Remove(planName)
		err = candidate.Chmod(0600)
		if err == nil {
			_, err = candidate.Write(variables)
		}
		closeErr := candidate.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		logFile, err := os.OpenFile(filepath.Join(dir, "builder-update.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer logFile.Close()
		if err := logFile.Chmod(0600); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
		defer cancel()
		stage = "plan"
		if _, err := w.execute(ctx, dir, logFile, "plan", "-input=false", "-no-color",
			"-var-file="+candidateName, "-out="+planName); err != nil {
			return err
		}
		if err := os.Chmod(planName, 0600); err != nil {
			return err
		}
		stage = "inspect"
		plan, err := w.execute(ctx, dir, logFile, "show", "-json", planName)
		if err != nil {
			return err
		}
		stage = "validate"
		if err := validateBuilderPlan(plan); err != nil {
			return err
		}
		stage = "database"
		if err := w.prepareUsageDatabase(ctx, original.TenantID, req.Dynamic); err != nil {
			return err
		}
		stage, uncertain = "apply", true
		if _, err := w.execute(ctx, dir, logFile, "apply", "-input=false", "-no-color", planName); err != nil {
			return err
		}
		stage = "state"
		empty, err := readTerraformState(dir)
		if err != nil {
			return err
		}
		if empty {
			return errors.New("Terraform state no longer contains the published site.")
		}
		stage = "persist"
		if err := atomicBuilderFile(filepath.Join(dir, "terraform.tfvars.json"), variables); err != nil {
			return err
		}
		return saveBuilderDesign(dir, req)
	}()
	w.mu.Lock()
	defer w.mu.Unlock()
	d := original
	if err == nil {
		d.Name, d.Style, d.Status, d.LastUpdateError = strings.TrimSpace(req.Name), req.Style, "ready", ""
	} else {
		log.Printf("builder update %s failed at %s: %v", d.ID, stage, err)
		d.LastUpdateError = "Builder update failed during " + stage + "; the published design was not replaced."
		if stage == "validate" {
			d.LastUpdateError = err.Error()
		}
		if uncertain {
			d.Status = "failed"
			d.LastUpdateError = "Builder update failed during " + stage + "; operator recovery is required before further operations."
		}
	}
	d.CanEdit = d.Status == "ready"
	if err := saveBuilderUpdateMetadata(dir, d); err != nil {
		d.Status, d.CanEdit = "failed", false
		d.LastUpdateError = "Unable to persist builder update status; operator recovery is required."
		_ = saveBuilderUpdateMetadata(dir, d)
		log.Printf("builder update %s metadata persistence failed: %v; lock retained", d.ID, err)
		return
	}
	if !uncertain || err == nil {
		w.removeLock()
	}
}
