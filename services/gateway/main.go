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

func newGateway(coreURL, searchURL, webDist string, origins []string) (http.Handler, error) {
	core, coreTarget, err := proxyFor(coreURL)
	if err != nil {
		return nil, err
	}
	search, searchTarget, err := proxyFor(searchURL)
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
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Add("Vary", "Origin")
			if !allowed[origin] {
				jsonError(w, 403, "origin_not_allowed", "This origin is not allowed.")
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/health" {
			if r.Method != http.MethodGet {
				jsonError(w, 405, "method_not_allowed", "Use GET.")
				return
			}
			for _, target := range []*url.URL{coreTarget, searchTarget} {
				ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
				req, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, target.String()+"/health", nil)
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
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{\"status\":\"ok\"}\n"))
			return
		}
		if r.URL.Path == "/api/search" {
			if r.Method != http.MethodGet {
				jsonError(w, 405, "method_not_allowed", "Search only supports GET.")
				return
			}
			search.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api" {
			if r.ContentLength > 64*1024 {
				jsonError(w, 413, "body_too_large", "Request body exceeds 64 KiB.")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
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

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func main() {
	handler, err := newGateway(
		env("CORE_URL", "http://127.0.0.1:8081"), env("SEARCH_URL", "http://127.0.0.1:8082"),
		env("WEB_DIST", "apps/web/dist"),
		strings.Split(env("ALLOWED_ORIGINS", "http://localhost:5173,http://127.0.0.1:5173,http://localhost:8080,http://127.0.0.1:8080"), ","),
	)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{
		Addr: env("HOST", "127.0.0.1") + ":" + env("PORT", "8080"), Handler: handler,
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
