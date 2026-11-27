package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"html"
	"strings"
)

//go:embed dynamic_runtime.js
var dynamicRuntimeSource string

// HTML normalizes CRLF before CSP checks; hash the same bytes the browser runs.
var dynamicRuntimeJS = strings.ReplaceAll(dynamicRuntimeSource, "\r\n", "\n")

func renderDynamicRuntime(origin, id string) string {
	return `<script data-app-base="` + html.EscapeString(origin+"/apps/"+id) + `">` + dynamicRuntimeJS + `</script>`
}

func dynamicContentPolicy(origin string) string {
	hash := sha256.Sum256([]byte(dynamicRuntimeJS))
	policy := "default-src 'none'; script-src 'sha256-" + base64.StdEncoding.EncodeToString(hash[:]) +
		"'; style-src 'unsafe-inline'; img-src https: data:; connect-src " + origin +
		"; form-action " + origin + "; base-uri 'none'; object-src 'none'"
	return `<meta http-equiv="Content-Security-Policy" content="` + html.EscapeString(policy) + `">`
}
