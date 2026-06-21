/*
This file implements Tabucom's deliberately small Markdown renderer.
It converts trusted syntax subsets while escaping all uploaded active HTML.
Publication writes the resulting standalone document as the deployment index.
It depends only on standard formatting, HTML escaping, and string packages,
avoiding a parser dependency for the limited supported feature set.
*/
package server

import (
	"fmt"
	"html"
	"strings"
)

const markdownDocumentStart = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Preview</title><style>body{font:16px/1.6 system-ui;max-width:52rem;margin:3rem auto;padding:0 1rem}pre{overflow:auto;padding:1rem;background:#f4f4f5}code{background:#f4f4f5;padding:.15em .3em}</style></head><body>`

// markdownRenderer tracks only the two block states supported by Tabucom's small
// renderer. It is not a general CommonMark parser; the narrow feature set keeps
// untrusted Markdown transformation dependency-free and auditable.
type markdownRenderer struct {
	output strings.Builder
	inCode bool
	inList bool
}

// renderMarkdown implements headings, paragraphs, unordered lists, fenced code,
// tables, and a restricted link syntax. All source text passes through HTML
// escaping, so embedded HTML and scripts are rendered as text rather than run.
func renderMarkdown(source []byte) []byte {
	lines := strings.Split(strings.ReplaceAll(string(source), "\r\n", "\n"), "\n")
	renderer := markdownRenderer{}
	renderer.output.WriteString(markdownDocumentStart)

	for index := 0; index < len(lines); index++ {
		line := lines[index]
		if strings.HasPrefix(line, "```") {
			renderer.toggleCodeBlock()
			continue
		}
		if renderer.inCode {
			renderer.output.WriteString(html.EscapeString(line))
			renderer.output.WriteByte('\n')
			continue
		}

		trimmed := strings.TrimSpace(line)
		if startsMarkdownTable(lines, index) {
			index = renderer.writeTable(lines, index)
			continue
		}
		if strings.HasPrefix(trimmed, "- ") {
			renderer.writeListItem(strings.TrimSpace(trimmed[2:]))
			continue
		}

		renderer.closeList()
		if trimmed == "" {
			continue
		}
		renderer.writeTextBlock(trimmed)
	}

	// Unclosed source constructs are closed in generated HTML. The source remains
	// visible without allowing malformed markup to escape the document body.
	if renderer.inCode {
		renderer.output.WriteString("</code></pre>")
	}
	renderer.closeList()
	renderer.output.WriteString("</body></html>")
	return []byte(renderer.output.String())
}

// toggleCodeBlock closes any active list before switching fenced-code state.
func (r *markdownRenderer) toggleCodeBlock() {
	r.closeList()
	if r.inCode {
		r.output.WriteString("</code></pre>")
	} else {
		r.output.WriteString("<pre><code>")
	}
	r.inCode = !r.inCode
}

// writeListItem lazily opens a list and escapes inline content.
func (r *markdownRenderer) writeListItem(value string) {
	if !r.inList {
		r.output.WriteString("<ul>")
		r.inList = true
	}
	r.output.WriteString("<li>" + markdownInline(value) + "</li>")
}

// closeList emits a closing tag only when a list is active.
func (r *markdownRenderer) closeList() {
	if r.inList {
		r.output.WriteString("</ul>")
		r.inList = false
	}
}

// writeTextBlock recognizes ATX headings up to level six; all other non-empty
// lines become standalone paragraphs.
func (r *markdownRenderer) writeTextBlock(value string) {
	headingLevel := 0
	for headingLevel < len(value) && headingLevel < 6 && value[headingLevel] == '#' {
		headingLevel++
	}
	if headingLevel > 0 && len(value) > headingLevel && value[headingLevel] == ' ' {
		fmt.Fprintf(&r.output, "<h%d>%s</h%d>", headingLevel, markdownInline(strings.TrimSpace(value[headingLevel:])), headingLevel)
		return
	}
	r.output.WriteString("<p>" + markdownInline(value) + "</p>")
}

// startsMarkdownTable requires a pipe-bearing header followed by a valid separator.
func startsMarkdownTable(lines []string, index int) bool {
	return strings.Contains(strings.TrimSpace(lines[index]), "|") &&
		index+1 < len(lines) && markdownTableSeparator(lines[index+1])
}

// writeTable consumes a header, its separator, and contiguous non-empty row lines.
// It returns the last consumed index so the caller's loop resumes correctly.
func (r *markdownRenderer) writeTable(lines []string, index int) int {
	r.closeList()
	r.output.WriteString("<table><thead><tr>")
	for _, cell := range markdownCells(strings.TrimSpace(lines[index])) {
		r.output.WriteString("<th>" + markdownInline(cell) + "</th>")
	}
	r.output.WriteString("</tr></thead><tbody>")

	index += 2
	for ; index < len(lines) && strings.Contains(lines[index], "|") && strings.TrimSpace(lines[index]) != ""; index++ {
		r.output.WriteString("<tr>")
		for _, cell := range markdownCells(lines[index]) {
			r.output.WriteString("<td>" + markdownInline(cell) + "</td>")
		}
		r.output.WriteString("</tr>")
	}
	r.output.WriteString("</tbody></table>")
	return index - 1
}

// markdownInline activates links only for a small allowlist of schemes and
// relative-reference forms. Unsupported or malformed syntax is escaped as text.
func markdownInline(value string) string {
	var output strings.Builder
	for len(value) > 0 {
		open := strings.IndexByte(value, '[')
		if open < 0 {
			output.WriteString(html.EscapeString(value))
			break
		}

		closeLabel := strings.Index(value[open+1:], "](")
		if closeLabel < 0 {
			output.WriteString(html.EscapeString(value))
			break
		}
		closeLabel += open + 1

		closeURL := strings.IndexByte(value[closeLabel+2:], ')')
		if closeURL < 0 {
			output.WriteString(html.EscapeString(value))
			break
		}
		closeURL += closeLabel + 2

		output.WriteString(html.EscapeString(value[:open]))
		label := value[open+1 : closeLabel]
		urlText := value[closeLabel+2 : closeURL]
		if safeMarkdownURL(urlText) {
			output.WriteString(`<a rel="nofollow noreferrer" href="` + html.EscapeString(urlText) + `">` + html.EscapeString(label) + `</a>`)
		} else {
			output.WriteString(html.EscapeString(value[open : closeURL+1]))
		}
		value = value[closeURL+1:]
	}
	return output.String()
}

// safeMarkdownURL blocks whitespace, quote injection, and active schemes such as
// javascript: while retaining ordinary web, mail, fragment, and relative links.
func safeMarkdownURL(value string) bool {
	if strings.ContainsAny(value, " \t\r\n\"") {
		return false
	}
	lower := strings.ToLower(value)
	return strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "mailto:") ||
		strings.HasPrefix(value, "/") ||
		strings.HasPrefix(value, "./") ||
		strings.HasPrefix(value, "../") ||
		strings.HasPrefix(value, "#")
}

// markdownCells performs the intentionally simple pipe-delimited table split.
func markdownCells(value string) []string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "|")
	value = strings.TrimSuffix(value, "|")
	cells := strings.Split(value, "|")
	for index := range cells {
		cells[index] = strings.TrimSpace(cells[index])
	}
	return cells
}

// markdownTableSeparator accepts cells containing at least three dashes with
// optional spaces and alignment colons.
func markdownTableSeparator(value string) bool {
	cells := markdownCells(value)
	if len(cells) == 0 {
		return false
	}
	for _, cell := range cells {
		cell = strings.Trim(cell, " :")
		if len(cell) < 3 || strings.Trim(cell, "-") != "" {
			return false
		}
	}
	return true
}
