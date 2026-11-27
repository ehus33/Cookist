package main

import (
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
)

type pageBlock struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Text       string `json:"text"`
	URL        string `json:"url"`
	Alt        string `json:"alt"`
	Align      string `json:"align"`
	Size       string `json:"size"`
	Beside     bool   `json:"beside,omitempty"`
	Collection string `json:"collection,omitempty"`
}

var pageBlockIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func safePageURL(value string) bool {
	if strings.ContainsAny(value, "\\ \t\r\n") {
		return false
	}
	for _, c := range value {
		if c < 32 || c == 127 {
			return false
		}
	}
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Opaque == ""
}

func validatePageBlocks(blocks []pageBlock) error {
	if len(blocks) < 1 || len(blocks) > 40 {
		return errors.New("Add between 1 and 40 elements to your page.")
	}
	ids := make(map[string]bool)
	catalogs := 0
	for i, block := range blocks {
		if block.Beside && (i == 0 || blocks[i-1].Beside) {
			return errors.New("A row can contain at most two elements.")
		}
		if !pageBlockIDPattern.MatchString(block.ID) || ids[block.ID] ||
			(block.Align != "left" && block.Align != "center" && block.Align != "right") ||
			(block.Size != "small" && block.Size != "medium" && block.Size != "large") ||
			len(block.Text) > 8000 || len(block.URL) > 2000 || len(block.Alt) > 640 {
			return fmt.Errorf("Element %d has invalid settings.", i+1)
		}
		ids[block.ID] = true
		switch block.Type {
		case "heading", "text", "button", "contact", "waitlist", "data-list", "data-form", "member-account":
			if strings.TrimSpace(block.Text) == "" || (block.Type != "text" && len(block.Text) > 320) {
				return fmt.Errorf("Element %d requires text within the allowed length.", i+1)
			}
		case "image":
			if strings.TrimSpace(block.Alt) == "" {
				return fmt.Errorf("Element %d requires image alternative text.", i+1)
			}
		case "divider", "spacer":
		case "products":
			catalogs++
		default:
			return fmt.Errorf("Element %d has an unsupported type.", i+1)
		}
		if (block.Type == "image" || block.Type == "button") && !safePageURL(block.URL) {
			return fmt.Errorf("Element %d requires a complete HTTPS URL without credentials.", i+1)
		}
	}
	if catalogs > 1 {
		return errors.New("Use only one product catalog per page.")
	}
	return nil
}

func hasProductCatalog(blocks []pageBlock) bool {
	for _, block := range blocks {
		if block.Type == "products" {
			return true
		}
	}
	return false
}

func renderPageBlocks(blocks []pageBlock, catalog string) string {
	return renderPageBlocksWithForms(blocks, catalog, "")
}

func renderPageBlocksWithForms(blocks []pageBlock, catalog, formBase string) string {
	var content strings.Builder
	content.WriteString("<main>")
	for i, block := range blocks {
		if !block.Beside {
			if i+1 < len(blocks) && blocks[i+1].Beside {
				content.WriteString(`<div class="page-row page-row-pair">`)
			} else {
				content.WriteString(`<div class="page-row">`)
			}
		}
		fmt.Fprintf(&content, `<div class="page-block page-block-%s page-align-%s page-size-%s">`, block.Type, block.Align, block.Size)
		text := html.EscapeString(strings.TrimSpace(block.Text))
		switch block.Type {
		case "heading":
			content.WriteString("<h2>" + text + "</h2>")
		case "text":
			content.WriteString("<p>" + text + "</p>")
		case "image":
			fmt.Fprintf(&content, `<img src="%s" alt="%s" loading="lazy" referrerpolicy="no-referrer">`,
				html.EscapeString(block.URL), html.EscapeString(strings.TrimSpace(block.Alt)))
		case "button":
			fmt.Fprintf(&content, `<a class="page-button" href="%s" rel="noreferrer">%s</a>`, html.EscapeString(block.URL), text)
		case "divider":
			content.WriteString("<hr>")
		case "spacer":
			content.WriteString(`<div class="page-spacer" aria-hidden="true"></div>`)
		case "products":
			content.WriteString(catalog)
		case "contact", "waitlist":
			fmt.Fprintf(&content, `<form class="lead-form" method="post" action="%s/%s"><h2>%s</h2><label>Name<input name="name" maxlength="160" autocomplete="name" required></label><label>Email<input type="email" name="email" maxlength="254" autocomplete="email" required></label>`,
				html.EscapeString(formBase), html.EscapeString(block.ID), text)
			if block.Type == "contact" {
				content.WriteString(`<label>Message<textarea name="message" maxlength="2000" rows="4" required></textarea></label>`)
			}
			content.WriteString(`<label class="lead-honeypot" aria-hidden="true">Leave empty<input name="website" tabindex="-1" autocomplete="off"></label><label class="lead-consent"><input type="checkbox" name="consent" value="yes" required>I agree this business may contact me about my request.</label><button class="page-button" type="submit">` + map[bool]string{true: "Join waitlist", false: "Send message"}[block.Type == "waitlist"] + `</button></form>`)
		case "data-list", "data-form", "member-account":
			fmt.Fprintf(&content, `<section class="dynamic-widget" data-dynamic="%s" data-collection="%s"><h2>%s</h2><div class="dynamic-body"></div><p class="dynamic-status" role="status" aria-live="polite">Loading app...</p></section>`,
				html.EscapeString(block.Type), html.EscapeString(block.Collection), text)
		}
		content.WriteString("</div>")
		if i+1 == len(blocks) || !blocks[i+1].Beside {
			content.WriteString("</div>")
		}
	}
	content.WriteString("</main>")
	return content.String()
}
