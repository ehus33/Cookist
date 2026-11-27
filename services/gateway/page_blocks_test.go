package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func builderRequest() deploymentRequest {
	return deploymentRequest{
		Name: "My page", Region: "us-west-2", Template: "builder", Style: "modern",
		Blocks: []pageBlock{{ID: "heading-1", Type: "heading", Text: "Welcome", Align: "center", Size: "large"}},
	}
}

func TestBuilderRendersAnyTwoElementTypesInOneRow(t *testing.T) {
	kinds := []string{"heading", "text", "image", "button", "divider", "spacer", "products"}
	for _, left := range kinds {
		for _, right := range kinds {
			// A page still permits only one product catalog, independent of layout.
			if left == "products" && right == "products" {
				continue
			}
			t.Run(left+"_"+right, func(t *testing.T) {
				req := builderRequest()
				req.Blocks = []pageBlock{
					{ID: "left", Type: left, Text: "Left", URL: "https://example.com/image.png", Alt: "Left photo", Align: "left", Size: "medium"},
					{ID: "right", Type: right, Text: "Right", URL: "https://example.com/contact", Alt: "Right photo", Align: "center", Size: "large", Beside: true},
				}
				if hasProductCatalog(req.Blocks) {
					req.BusinessName = "Shop"
					req.Products = []deploymentProduct{{Name: "Coffee", PriceCents: 1999, PaymentLink: "https://buy.stripe.com/test_abc123"}}
				}
				data, err := terraformVariables(req, "123456789012", strings.Repeat("a", 32))
				if err != nil {
					t.Fatal(err)
				}
				var variables struct {
					Page string `json:"page_html"`
				}
				if err := json.Unmarshal(data, &variables); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(variables.Page, `<main><div class="page-row page-row-pair">`) ||
					!strings.Contains(variables.Page, `</div></div></main>`) ||
					strings.Count(variables.Page, `<div class="page-row`) != 1 ||
					strings.Count(variables.Page, `<div class="page-block`) != 2 {
					t.Fatal("paired elements were not rendered in exactly one two-element row")
				}
			})
		}
	}
}

func TestBuilderRejectsInvalidRowsBeforeProvisioning(t *testing.T) {
	for _, flags := range [][]bool{{true}, {false, true, true}, {false, false, true, true}} {
		w, _ := testWorker(t)
		req := builderRequest()
		req.Blocks = nil
		for i, beside := range flags {
			req.Blocks = append(req.Blocks, pageBlock{ID: fmt.Sprintf("element-%d", i), Type: "text", Text: "Content", Align: "left", Size: "medium", Beside: beside})
		}
		body, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		rec := requestDeployment(w, http.MethodPost, "Bearer owner", string(body))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "at most two") {
			t.Fatalf("invalid row returned %d: %s", rec.Code, rec.Body)
		}
		entries, err := os.ReadDir(w.root)
		if err != nil || len(entries) != 0 {
			t.Fatalf("invalid rows created deployment files: %v", err)
		}
	}
}

func TestBuilderRendersOrderedEscapedElementsAndSellerCatalog(t *testing.T) {
	for _, style := range []string{"", "classic", "modern", "warm", "bold"} {
		t.Run("style_"+style, func(t *testing.T) {
			req := builderRequest()
			req.Style = style
			req.Blocks = append(req.Blocks,
				pageBlock{ID: "body", Type: "text", Text: "<script>unsafe</script>\nNew line ${literal}", Align: "left", Size: "medium"},
				pageBlock{ID: "photo", Type: "image", URL: "https://example.com/image.png?a=1&b=2", Alt: `A "safe" photo`, Align: "right", Size: "small"},
				pageBlock{ID: "link", Type: "button", Text: "Contact <us>", URL: "https://example.com/contact", Align: "center", Size: "medium"},
				pageBlock{ID: "line", Type: "divider", Align: "left", Size: "small"},
				pageBlock{ID: "space", Type: "spacer", Align: "left", Size: "large"},
				pageBlock{ID: "catalog", Type: "products", Align: "left", Size: "medium"},
			)
			req.BusinessName = "Store"
			req.Products = []deploymentProduct{{Name: "Coffee", PriceCents: 1999, PaymentLink: "https://buy.stripe.com/test_abc123"}}
			data, err := terraformVariables(req, "123456789012", strings.Repeat("a", 32))
			if err != nil {
				t.Fatal(err)
			}
			var variables struct {
				Page string `json:"page_html"`
			}
			if err := json.Unmarshal(data, &variables); err != nil {
				t.Fatal(err)
			}
			page := variables.Page
			for _, want := range []string{
				"<title>My page</title>", `<h2>Welcome</h2>`, "&lt;script&gt;unsafe&lt;/script&gt;\nNew line ${literal}",
				`src="https://example.com/image.png?a=1&amp;b=2"`, `alt="A &#34;safe&#34; photo"`, `referrerpolicy="no-referrer"`,
				`href="https://example.com/contact" rel="noreferrer">Contact &lt;us&gt;</a>`, `<hr>`,
				`class="page-spacer" aria-hidden="true"`, `href="https://buy.stripe.com/test_abc123"`,
				"$19.99 USD", "Test checkout - no real charge", `class="generated-site site-style-`,
			} {
				if !strings.Contains(page, want) {
					t.Errorf("missing builder output %q", want)
				}
			}
			previous := -1
			for _, kind := range []string{"heading", "text", "image", "button", "divider", "spacer", "products"} {
				index := strings.Index(page, `<div class="page-block page-block-`+kind)
				if index <= previous {
					t.Fatalf("element %s was reordered or omitted", kind)
				}
				previous = index
			}
			if strings.Contains(page, "<script>") || strings.Contains(page, "builder-element-toolbar") {
				t.Fatal("builder emitted executable input or editor controls")
			}
		})
	}
}

func TestBuilderRejectsInvalidElementsBeforeProvisioning(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*deploymentRequest)
	}{
		{"empty", func(r *deploymentRequest) { r.Blocks = nil }},
		{"too_many", func(r *deploymentRequest) { r.Blocks = make([]pageBlock, 41) }},
		{"duplicate_id", func(r *deploymentRequest) { r.Blocks = append(r.Blocks, r.Blocks[0]) }},
		{"invalid_id", func(r *deploymentRequest) { r.Blocks[0].ID = "../page" }},
		{"raw_html", func(r *deploymentRequest) { r.Blocks[0].Type = "html" }},
		{"invalid_alignment", func(r *deploymentRequest) { r.Blocks[0].Align = `left" onclick="bad` }},
		{"invalid_size", func(r *deploymentRequest) { r.Blocks[0].Size = "9999px" }},
		{"empty_text", func(r *deploymentRequest) { r.Blocks[0].Text = " " }},
		{"long_heading", func(r *deploymentRequest) { r.Blocks[0].Text = strings.Repeat("a", 321) }},
		{"long_text", func(r *deploymentRequest) { r.Blocks[0].Type = "text"; r.Blocks[0].Text = strings.Repeat("a", 8001) }},
		{"missing_alt", func(r *deploymentRequest) {
			r.Blocks[0].Type = "image"
			r.Blocks[0].URL = "https://example.com/image.png"
		}},
		{"products_without_catalog", func(r *deploymentRequest) { r.Products = []deploymentProduct{{Name: "Ignored"}} }},
		{"catalog_without_products", func(r *deploymentRequest) { r.Blocks[0].Type = "products" }},
		{"wrong_template", func(r *deploymentRequest) { r.Template = "blank" }},
		{"two_catalogs", func(r *deploymentRequest) {
			r.Blocks[0].Type = "products"
			r.Blocks = append(r.Blocks, pageBlock{ID: "second", Type: "products", Align: "left", Size: "medium"})
		}},
	}
	for _, url := range []string{"javascript:alert(1)", "data:text/html,test", "http://example.com", "//example.com",
		"https://user:pass@example.com", "https:///missing-host", "https://example.com\n", `https://example.com\@other.com`} {
		tests = append(tests, struct {
			name   string
			mutate func(*deploymentRequest)
		}{url, func(r *deploymentRequest) { r.Blocks[0].Type = "button"; r.Blocks[0].URL = url }})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w, _ := testWorker(t)
			req := builderRequest()
			test.mutate(&req)
			body, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			rec := requestDeployment(w, http.MethodPost, "Bearer owner", string(body))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("invalid builder returned %d: %s", rec.Code, rec.Body)
			}
			entries, err := os.ReadDir(w.root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("invalid builder created files: %v", err)
			}
		})
	}
}

func TestBuilderPersistsGeneratedPageWithoutRealAWS(t *testing.T) {
	t.Setenv("COOKIST_TEST_TERRAFORM_HELPER", "1")
	w, _ := testWorker(t)
	req := builderRequest()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	rec := requestDeployment(w, http.MethodPost, "Bearer owner", string(body))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("builder returned %d: %s", rec.Code, rec.Body)
	}
	d := waitDeployment(t, w, "ready")
	content, err := os.ReadFile(filepath.Join(w.root, d.ID, "terraform.tfvars.json"))
	if err != nil {
		t.Fatal(err)
	}
	var variables struct {
		Page string `json:"page_html"`
	}
	if err := json.Unmarshal(content, &variables); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(variables.Page, "<h2>Welcome</h2>") {
		t.Fatal("deployed page lost the user-designed content")
	}
}
