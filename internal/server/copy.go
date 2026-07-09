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
const copyPrimaryMarker = "data-tabucom-copy-primary"
const copyArrowMarker = "data-tabucom-copy-arrow"
const copyMenuMarker = "data-tabucom-copy-menu"
const copyDownloadMarker = "data-tabucom-download-html"
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
button{all:initial;box-sizing:border-box;display:inline-flex;align-items:center;justify-content:center;height:36px;border:1px solid rgba(255,255,255,.28);background:#111;color:#fff;font:700 13px/1 ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;cursor:pointer}
button:focus{outline:2px solid #0c7569;outline-offset:3px}
button[disabled]{opacity:.72;cursor:default}
.wrap{all:initial;position:relative;display:inline-flex;filter:drop-shadow(0 8px 24px rgba(0,0,0,.22))}
.primary{min-width:92px;padding:0 12px;border-radius:6px 0 0 6px}
.arrow{width:34px;border-left:0;border-radius:0 6px 6px 0;font-size:14px}
.menu{all:initial;position:absolute;right:0;bottom:42px;display:grid;min-width:142px;padding:5px;border:1px solid rgba(17,17,17,.14);border-radius:7px;background:#fff;box-shadow:0 12px 32px rgba(0,0,0,.22)}
.menu[hidden]{display:none}
.menu button{width:100%;height:32px;justify-content:flex-start;border:0;border-radius:4px;background:#fff;color:#111;padding:0 9px;box-shadow:none}
.menu button:focus,.menu button:hover{background:#eef6f4;color:#111}
</style><div class="wrap"><button class="primary" type="button" ` + copyPrimaryMarker + ` aria-label="Copy source HTML">` + label + `</button><button class="arrow" type="button" ` + copyArrowMarker + ` aria-label="More page actions" aria-haspopup="menu" aria-expanded="false">▾</button><div class="menu" ` + copyMenuMarker + ` role="menu" hidden><button type="button" ` + copyDownloadMarker + ` role="menuitem">Download HTML</button></div></div>` + "`" + `;
  const primary = root.querySelector("[` + copyPrimaryMarker + `]");
  const arrow = root.querySelector("[` + copyArrowMarker + `]");
  const menu = root.querySelector("[` + copyMenuMarker + `]");
  const download = root.querySelector("[` + copyDownloadMarker + `]");
  let menuOpen = false;

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

  function setMenu(open, focusTarget = false) {
    menuOpen = open;
    menu.hidden = !open;
    arrow.setAttribute("aria-expanded", open ? "true" : "false");
    if (open && focusTarget) download.focus();
    if (!open && focusTarget) arrow.focus();
  }

  function filename() {
    const match = window.location.pathname.match(/^\/p\/([a-z0-9-]+)(?:\/|$)/);
    if (match) return ` + "`" + `tabucom-${match[1]}.html` + "`" + `;
    const labels = window.location.hostname.split(".");
    if (labels.length > 2 && /^[a-z0-9-]+$/.test(labels[0])) {
      return ` + "`" + `tabucom-${labels[0]}.html` + "`" + `;
    }
    return "tabucom-page.html";
  }

  async function rawText() {
    const response = await fetch(rawURL.href, {cache:"no-store", credentials:"same-origin"});
    if (!response.ok) throw new Error("raw request failed");
    return response.text();
  }

  primary.addEventListener("click", async () => {
    const original = primary.textContent;
    primary.disabled = true;
    primary.textContent = "Copying...";
    try {
      const text = await rawText();
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(text);
      } else if (!legacyCopy(text)) {
        throw new Error("copy failed");
      }
      primary.textContent = "Copied";
    } catch {
      primary.textContent = "Copy failed";
    }
    setTimeout(() => {
      primary.disabled = false;
      primary.textContent = original;
    }, 1500);
  });

  arrow.addEventListener("click", () => setMenu(!menuOpen, menu.hidden));
  arrow.addEventListener("keydown", (event) => {
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      setMenu(!menuOpen, true);
    } else if (event.key === "Escape" && menuOpen) {
      event.preventDefault();
      setMenu(false, true);
    }
  });
  download.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      event.preventDefault();
      setMenu(false, true);
    }
  });
  download.addEventListener("click", async () => {
    const original = download.textContent;
    download.disabled = true;
    download.textContent = "Downloading...";
    try {
      const text = await rawText();
      const url = URL.createObjectURL(new Blob([text], {type:"text/html;charset=utf-8"}));
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = filename();
      document.body.appendChild(anchor);
      anchor.click();
      document.body.removeChild(anchor);
      setTimeout(() => URL.revokeObjectURL(url), 0);
      download.textContent = "Downloaded";
      setMenu(false, true);
    } catch {
      download.textContent = "Download failed";
    }
    setTimeout(() => {
      download.disabled = false;
      download.textContent = original;
    }, 1500);
  });
  document.addEventListener("click", (event) => {
    if (menuOpen && event.target !== host) setMenu(false);
  });
})();
</script>`
}
