/*
This file tests the supported Markdown subset and its escaping boundary.
It verifies active content remains inert after rendering and publication.
The tests also document headings, lists, code fences, links, and tables.
It depends on shared HTTP publication helpers and standard test utilities,
including filesystem reads of the committed rendered index.
*/
package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMarkdownEscapesActiveContent proves that raw HTML, fenced-code HTML, and
// active URL schemes remain inert after rendering and publication.
func TestMarkdownEscapesActiveContent(t *testing.T) {
	server := testServer(t, nil)
	source := []byte("# Title\n\n<script>alert(1)</script> [safe](https://example.com) [bad](javascript:alert)\n\n| A | B |\n| --- | --- |\n| one | two |\n\n```\n<b>x</b>\n```")

	published, response := publish(t, server, "text/markdown", source, "")
	requireStatus(t, response, http.StatusCreated)

	data, err := os.ReadFile(filepath.Join(server.sites, published.ID, "index.html"))
	if err != nil {
		t.Fatalf("read rendered Markdown: %v", err)
	}
	page := string(data)

	// These fragments would execute or become active markup if escaping regressed.
	for _, unsafe := range []string{"<script>", "<b>x</b>", `href="javascript:`} {
		if strings.Contains(page, unsafe) {
			t.Errorf("rendered page contains unsafe fragment %q: %s", unsafe, page)
		}
	}
	// These fragments prove that safe formatting was retained while escaping input.
	for _, required := range []string{"&lt;script&gt;", `href="https://example.com"`, "<table>"} {
		if !strings.Contains(page, required) {
			t.Errorf("rendered page is missing %q: %s", required, page)
		}
	}
}

// TestMarkdownRendersSupportedBlocks documents the deliberately limited renderer
// contract without implying full CommonMark compatibility.
func TestMarkdownRendersSupportedBlocks(t *testing.T) {
	page := string(renderMarkdown([]byte("# Heading\n\n- one\n- two\n\nparagraph\n\n```\ncode\n```")))
	for _, required := range []string{
		"<h1>Heading</h1>",
		"<ul><li>one</li><li>two</li></ul>",
		"<p>paragraph</p>",
		"<pre><code>code\n</code></pre>",
	} {
		if !strings.Contains(page, required) {
			t.Errorf("rendered page is missing %q: %s", required, page)
		}
	}
}

// TestMarkdownSafeURLForms covers every intentionally activated link family and
// confirms an unsupported scheme is emitted only as escaped source text.
func TestMarkdownSafeURLForms(t *testing.T) {
	for _, url := range []string{
		"https://example.com",
		"http://example.com",
		"mailto:user@example.com",
		"/absolute/path",
		"./relative",
		"../parent",
		"#fragment",
	} {
		rendered := markdownInline("[link](" + url + ")")
		if !strings.Contains(rendered, `href="`+url+`"`) {
			t.Errorf("safe URL %q was not activated: %s", url, rendered)
		}
	}

	if rendered := markdownInline("[bad](data:text/html,x)"); strings.Contains(rendered, "href=") {
		t.Fatalf("unsafe URL was activated: %s", rendered)
	}
}
