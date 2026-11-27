package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func jsonError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func proxyFor(raw string) (*httputil.ReverseProxy, *url.URL, error) {
	target, err := url.Parse(raw)
	if err != nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") || target.User != nil {
		return nil, nil, fmt.Errorf("invalid upstream URL")
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ResponseHeaderTimeout: 10 * time.Second,
		IdleConnTimeout:       60 * time.Second,
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("upstream request failed: %v", err)
		jsonError(w, 502, "upstream_unavailable", "A Cookist service is unavailable. Please try again.")
	}
	return proxy, target, nil
}

func newGateway(coreURL, webDist string, origins []string, deployments *deploymentWorker) (http.Handler, error) {
	core, coreTarget, err := proxyFor(coreURL)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, origin := range origins {
		allowed[strings.TrimSpace(origin)] = true
	}
	client := &http.Client{Timeout: 6 * time.Second}
	files := http.FileServer(http.Dir(webDist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		if strings.HasPrefix(r.URL.Path, "/apps/") {
			if deployments == nil {
				jsonError(w, 503, "dynamic_unavailable", "Dynamic apps are not configured.")
			} else {
				deployments.dynamicPublicRoute(w, r)
			}
			return
		}
		if strings.HasPrefix(r.URL.Path, "/forms/") {
			if deployments == nil {
				jsonError(w, 503, "forms_unavailable", "Lead forms are not configured.")
			} else {
				deployments.receiveSubmission(w, r)
			}
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Add("Vary", "Origin")
			if !allowed[origin] {
				jsonError(w, 403, "origin_not_allowed", "This origin is not allowed.")
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/api/billing/webhook" {
			if deployments == nil {
				jsonError(w, 503, "billing_unavailable", "Billing is not configured.")
			} else {
				deployments.billingRoute(w, r)
			}
			return
		}
		if r.URL.Path == "/health" {
			if r.Method != http.MethodGet {
				jsonError(w, 405, "method_not_allowed", "Use GET.")
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
			req, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, coreTarget.String()+"/health", nil)
			if requestErr != nil {
				cancel()
				jsonError(w, 503, "not_ready", "Invalid service health configuration.")
				return
			}
			res, requestErr := client.Do(req)
			if requestErr != nil {
				cancel()
				jsonError(w, 503, "not_ready", "Cookist services are starting or unavailable.")
				return
			}
			res.Body.Close()
			cancel()
			if res.StatusCode != 200 {
				jsonError(w, 503, "not_ready", "A Cookist service is not ready.")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{\"status\":\"ok\"}\n"))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api" {
			limit := int64(64 * 1024)
			if r.URL.Path == "/api/builder-draft" || r.URL.Path == "/api/deployments" || strings.HasSuffix(r.URL.Path, "/design") {
				limit = 524288
			}
			if r.ContentLength > limit {
				jsonError(w, 413, "body_too_large", "Request body exceeds the route's size limit.")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			if strings.HasPrefix(r.URL.Path, "/api/billing/") {
				if deployments == nil {
					jsonError(w, 503, "billing_unavailable", "Billing is not configured.")
				} else {
					deployments.billingRoute(w, r)
				}
				return
			}
			if r.URL.Path == "/api/builder-draft" || r.URL.Path == "/api/deployment-access" || r.URL.Path == "/api/deployments" || strings.HasPrefix(r.URL.Path, "/api/deployments/") {
				if deployments == nil {
					jsonError(w, 503, "deployments_disabled", "Deployments are not configured on this server.")
				} else {
					deployments.ServeHTTP(w, r)
				}
				return
			}
			core.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			jsonError(w, 405, "method_not_allowed", "Use GET to load the app.")
			return
		}
		clean := path.Clean("/" + r.URL.Path)
		file := filepath.Join(webDist, filepath.FromSlash(strings.TrimPrefix(clean, "/")))
		info, statErr := os.Stat(file)
		if statErr == nil && !info.IsDir() {
			if strings.HasPrefix(clean, "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			files.ServeHTTP(w, r)
			return
		}
		if statErr != nil && !os.IsNotExist(statErr) {
			log.Printf("static file lookup failed: %v", statErr)
			jsonError(w, 500, "static_file_error", "Unable to load the app.")
			return
		}
		if filepath.Ext(clean) != "" {
			jsonError(w, 404, "not_found", "File not found.")
			return
		}
		index := filepath.Join(webDist, "index.html")
		if _, err := os.Stat(index); err != nil {
			jsonError(w, 503, "web_not_built", "Build the React app first, or open the Vite development server.")
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, index)
	}), nil
}

func requireCognito(next http.Handler, coreURL string) (http.Handler, error) {
	target, err := url.Parse(coreURL)
	if err != nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") || target.User != nil {
		return nil, fmt.Errorf("invalid core URL")
	}
	target.Path = strings.TrimRight(target.Path, "/") + "/api/me"
	target.RawQuery = ""
	client := &http.Client{Timeout: 6 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := path.Clean("/" + r.URL.Path)
		if !(clean == "/api" || strings.HasPrefix(clean, "/api/")) || r.Method == http.MethodOptions ||
			(r.URL.Path == "/api/billing/webhook" && r.Method == http.MethodPost) {
			next.ServeHTTP(w, r)
			return
		}
		authorization := r.Header.Get("Authorization")
		if !strings.HasPrefix(authorization, "Bearer ") || len(authorization) > 16384 {
			jsonError(w, 401, "unauthorized", "Sign in to continue.")
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target.String(), nil)
		if err != nil {
			jsonError(w, 503, "auth_unavailable", "Authentication is unavailable.")
			return
		}
		req.Header.Set("Authorization", authorization)
		response, err := client.Do(req)
		if err != nil {
			log.Printf("authentication service unavailable: %v", err)
			jsonError(w, 503, "auth_unavailable", "Authentication is unavailable.")
			return
		}
		response.Body.Close()
		switch response.StatusCode {
		case http.StatusOK:
			w.Header().Set("Cache-Control", "private, no-store")
			next.ServeHTTP(w, r)
		case http.StatusUnauthorized:
			jsonError(w, 401, "unauthorized", "Sign in to continue.")
		case http.StatusForbidden:
			jsonError(w, 403, "forbidden", "This account cannot access Cookist.")
		default:
			log.Printf("authentication service returned unexpected status %d", response.StatusCode)
			jsonError(w, 503, "auth_unavailable", "Authentication is unavailable.")
		}
	}), nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func main() {
	coreURL := env("CORE_URL", "http://127.0.0.1:8081")
	mode := env("COOKIST_AUTH_MODE", "demo")
	host := env("HOST", "127.0.0.1")
	localAccess, err := localSubscriptionBypass(env("COOKIST_DEPLOYMENT_ACCESS_MODE", "hosted"), host)
	if err != nil {
		log.Fatal(err)
	}
	var deployments *deploymentWorker
	switch env("COOKIST_DEPLOYMENTS_ENABLED", "false") {
	case "false":
	case "true":
		if mode != "cognito" {
			log.Fatal("deployments require Cognito authentication")
		}
		var err error
		deployments, err = newDeploymentWorker(coreURL, os.Getenv("COOKIST_DEPLOYMENT_OWNER"),
			os.Getenv("COOKIST_DEPLOYMENT_ROOT"), os.Getenv("COOKIST_TERRAFORM_PATH"), os.Getenv("COOKIST_AWS_ACCOUNT_ID"))
		if err != nil {
			log.Fatal(err)
		}
		deployments.accessFile = os.Getenv("COOKIST_DEPLOYMENT_ACCESS_FILE")
		deployments.localAccess = localAccess
		deployments.publicOrigin = os.Getenv("COOKIST_PUBLIC_ORIGIN")
		if deployments.publicOrigin != "" && !validFormOrigin(deployments.publicOrigin) {
			log.Fatal("COOKIST_PUBLIC_ORIGIN must be an HTTPS origin without a path for lead forms")
		}
		dynamicRegion := os.Getenv("COOKIST_DYNAMIC_REGION")
		dynamicTable := os.Getenv("COOKIST_DYNAMIC_TABLE")
		visitorPool := os.Getenv("COOKIST_VISITOR_POOL_ID")
		visitorClient := os.Getenv("COOKIST_VISITOR_CLIENT_ID")
		if dynamicRegion != "" || dynamicTable != "" || visitorPool != "" || visitorClient != "" {
			if dynamicRegion == "" || dynamicTable == "" || visitorPool == "" || visitorClient == "" {
				log.Fatal("Dynamic apps require region, table, visitor pool and visitor client together")
			}
			if !validFormOrigin(deployments.publicOrigin) {
				log.Fatal("Dynamic apps require COOKIST_PUBLIC_ORIGIN to be a valid HTTPS origin")
			}
			if visitorClient == os.Getenv("VITE_COGNITO_CLIENT_ID") ||
				strings.Contains(os.Getenv("COOKIST_COGNITO_CLIENT_IDS"), visitorClient) ||
				strings.HasSuffix(os.Getenv("COOKIST_COGNITO_ISSUER"), "/"+visitorPool) {
				log.Fatal("Dynamic visitor authentication must use a separate Cognito pool/client from Cookist owners")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			deployments.dynamic, err = newDynamicService(ctx, dynamicRegion, dynamicTable, visitorPool, visitorClient)
			cancel()
			if err != nil {
				log.Fatal(err)
			}
		}
		if _, err := loadAccessPolicy(deployments.accessFile); err != nil {
			log.Fatalf("deployment access configuration is invalid: %v", err)
		}
	default:
		log.Fatal("COOKIST_DEPLOYMENTS_ENABLED must be true or false")
	}
	switch env("COOKIST_BILLING_ENABLED", "false") {
	case "false":
	case "true":
		if deployments == nil {
			log.Fatal("Stripe billing requires the deployment worker to be enabled")
		}
		deployments.billing, err = newStripeBilling(
			filepath.Join(deployments.root, ".billing"), os.Getenv("COOKIST_PUBLIC_ORIGIN"),
			os.Getenv("COOKIST_STRIPE_PRICE_ID"), os.Getenv("COOKIST_STRIPE_SECRET_KEY"),
			os.Getenv("COOKIST_STRIPE_WEBHOOK_SECRET"), env("COOKIST_STRIPE_MODE", "test"))
		if err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatal("COOKIST_BILLING_ENABLED must be true or false")
	}
	switch env("COOKIST_SELF_SERVICE_BILLING", "false") {
	case "false":
	case "true":
		if deployments == nil || deployments.billing == nil || localAccess {
			log.Fatal("self-service billing requires configured hosted Stripe billing")
		}
		deployments.selfServiceBilling = true
	default:
		log.Fatal("COOKIST_SELF_SERVICE_BILLING must be true or false")
	}
	if price := os.Getenv("COOKIST_DATABASE_PRICE_ID"); price != "" {
		log.Fatal("Fixed-price database billing is no longer enabled in this server; remove COOKIST_DATABASE_PRICE_ID and configure COOKIST_DATABASE_COST_BILLING")
	}
	switch env("COOKIST_DATABASE_COST_BILLING", "false") {
	case "false":
	case "true":
		if deployments == nil {
			log.Fatal("Actual database cost billing requires a deployment worker")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := deployments.configureDatabaseUsage(ctx)
		cancel()
		if err != nil {
			log.Fatal(err)
		}
		go deployments.runDatabaseUsageBilling()
	default:
		log.Fatal("COOKIST_DATABASE_COST_BILLING must be true or false")
	}
	handler, err := newGateway(
		coreURL, env("WEB_DIST", "apps/web/dist"),
		strings.Split(env("ALLOWED_ORIGINS", "http://localhost:5173,http://127.0.0.1:5173,http://localhost:8080,http://127.0.0.1:8080"), ","),
		deployments,
	)
	if err != nil {
		log.Fatal(err)
	}
	switch mode {
	case "demo":
	case "cognito":
		handler, err = requireCognito(handler, coreURL)
		if err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatal("COOKIST_AUTH_MODE must be demo or cognito")
	}
	server := &http.Server{
		Addr: net.JoinHostPort(host, env("PORT", "8080")), Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("shutdown failed: %v", err)
		}
	}()
	log.Printf("Cookist gateway listening on %s", server.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
