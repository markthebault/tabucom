/*
This file embeds and serves Tabucom's landing and discovery documents.
An explicit route allowlist keeps private templates from becoming public assets.
Public origins are substituted safely for human and machine-facing examples.
It depends on standard embed, filesystem, HTML escaping, MIME, path, and HTTP APIs,
and supplies the embedded password template parsed by password.go.
*/
package server

import (
	"bytes"
	"embed"
	"html"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
)

// embeddedWeb keeps discovery and documentation assets inside the executable so
// they are always versioned with the routes they describe.
//
//go:embed web web/.well-known/agent.json
var embeddedWeb embed.FS

// webPaths is an allowlist rather than a direct filesystem mapping. New embedded
// files do not become public until routing policy explicitly exposes them.
var webPaths = map[string]string{
	"/agents":                              "web/agents.html",
	"/openapi.json":                        "web/openapi.json",
	"/llms.txt":                            "web/llms.txt",
	"/.well-known/agent.json":              "web/.well-known/agent.json",
	"/agenticons/agenticon-claudecode.svg": "web/agenticons/agenticon-claudecode.svg",
	"/agenticons/agenticon-codex.svg":      "web/agenticons/agenticon-codex.svg",
	"/agenticons/agenticon-cursor.svg":     "web/agenticons/agenticon-cursor.svg",
	"/agenticons/agenticon-hermes.svg":     "web/agenticons/agenticon-hermes.svg",
	"/agenticons/agenticon-openclaw.svg":   "web/agenticons/agenticon-openclaw.svg",
}

// isWebPath reports whether a request addresses public discovery content.
func isWebPath(requestPath string) bool {
	_, ok := webPaths[requestPath]
	return ok
}

// landing serves the human-facing entry point with the request's public origin.
// HTML escaping prevents a malicious Host header from becoming page markup.
func (s *Server) landing(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(embeddedWeb, "web/index.html")
	if err != nil {
		http.Error(w, "documentation unavailable", http.StatusInternalServerError)
		return
	}

	origin := html.EscapeString(s.requestBase(r))
	data = bytes.ReplaceAll(data, []byte("{{ORIGIN}}"), []byte(origin))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// serveWebFile serves one allowlisted embedded asset and substitutes its public
// origin. HEAD receives identical metadata without a response body.
func (s *Server) serveWebFile(w http.ResponseWriter, r *http.Request) {
	name, ok := webPaths[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(embeddedWeb, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	origin := s.requestBase(r)
	if path.Ext(name) == ".html" {
		origin = html.EscapeString(origin)
	}
	data = bytes.ReplaceAll(data, []byte("{{ORIGIN}}"), []byte(origin))

	if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method == http.MethodGet {
		_, _ = w.Write(data)
	}
}
