/*
This file adds Tabucom's browser copy affordance for published HTML pages.
The stored deployment remains immutable; raw=1 is the byte-exact source path.
Decoration is limited to successful HTML GET responses after expiry and password checks.
It depends on standard bytes, HTML escaping, HTTP request, MIME, path, and string APIs,
while site.go and s3.go provide the already-authorized file or object content.
*/
package server

import (
	"bytes"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
)

const copyHTMLMarker = "data-tabucom-copy-html"
const maxDecoratedHTMLBytes = 5 << 20

// rawMode reports whether a request wants the stored deployment bytes without
// Tabucom's browser-only copy control. Query strings are visible, easy to cache,
// and simple for humans or agents to reproduce with curl.
func rawMode(r *http.Request) bool {
	return r.URL.Query().Get("raw") == "1"
}

// shouldDecorateHTML keeps the copy control out of methods and request shapes
// where body rewriting would surprise clients or break byte-range semantics.
func shouldDecorateHTML(r *http.Request, file string, size int64) bool {
	if r.Method != http.MethodGet || rawMode(r) || r.Header.Get("Range") != "" {
		return false
	}
	if size < 0 || size > maxDecoratedHTMLBytes {
		return false
	}
	contentType := mime.TypeByExtension(filepath.Ext(file))
	return strings.HasPrefix(strings.ToLower(contentType), "text/html")
}

// decorateHTML inserts a compact, self-contained control near the end of an HTML
// document. If a page omits </body>, appending keeps the original content intact.
func decorateHTML(body []byte) []byte {
	snippet := []byte(copyHTMLSnippet())
	lower := bytes.ToLower(body)
	index := bytes.LastIndex(lower, []byte("</body>"))
	if index == -1 {
		out := make([]byte, 0, len(body)+len(snippet))
		out = append(out, body...)
		out = append(out, snippet...)
		return out
	}

	out := make([]byte, 0, len(body)+len(snippet))
	out = append(out, body[:index]...)
	out = append(out, snippet...)
	out = append(out, body[index:]...)
	return out
}

func copyHTMLSnippet() string {
	label := "Copy Page"
	return `<script ` + copyHTMLMarker + `>
(() => {
  if (window.__tabucomCopyHTML || !document.body) return;
  window.__tabucomCopyHTML = true;
  const rawURL = new URL(window.location.href);
  rawURL.searchParams.set("raw", "1");
  const host = document.createElement("div");
  host.setAttribute("` + copyHTMLMarker + `", "");
  host.style.cssText = "all:initial;position:fixed;right:16px;bottom:16px;z-index:2147483647";
  document.body.appendChild(host);
  const root = host.attachShadow ? host.attachShadow({mode:"closed"}) : host;
  root.innerHTML = ` + "`" + `<style>
button{all:initial;box-sizing:border-box;display:inline-flex;align-items:center;justify-content:center;min-width:92px;height:36px;padding:0 12px;border:1px solid rgba(255,255,255,.28);border-radius:6px;background:#111;color:#fff;box-shadow:0 8px 24px rgba(0,0,0,.22);font:700 13px/1 ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;cursor:pointer}
button:focus{outline:2px solid #0c7569;outline-offset:3px}
button[disabled]{opacity:.72;cursor:default}
</style><button type="button" aria-label="Copy source HTML">` + label + `</button>` + "`" + `;
  const button = root.querySelector("button");
  function legacyCopy(text) {
    const input = document.createElement("textarea");
    input.value = text;
    input.setAttribute("readonly", "");
    input.style.cssText = "position:fixed;top:0;left:-9999px";
    document.body.appendChild(input);
    input.select();
    input.setSelectionRange(0, input.value.length);
    const copied = document.execCommand("copy");
    document.body.removeChild(input);
    return copied;
  }
  button.addEventListener("click", async () => {
    const original = button.textContent;
    button.disabled = true;
    button.textContent = "Copying...";
    try {
      const response = await fetch(rawURL.href, {cache:"no-store", credentials:"same-origin"});
      if (!response.ok) throw new Error("raw request failed");
      const text = await response.text();
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(text);
      } else if (!legacyCopy(text)) {
        throw new Error("copy failed");
      }
      button.textContent = "Copied";
    } catch {
      button.textContent = "Copy failed";
    }
    setTimeout(() => {
      button.disabled = false;
      button.textContent = original;
    }, 1500);
  });
})();
</script>`
}
