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
	"strings"
	"time"
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

const tokenUI = `<div class="token-panel" data-token-panel>
            <div class="token-row">
              <button class="token-button" type="button" data-generate-token>Generate Publish Token</button>
              <p class="token-ttl">Token TTL: {{TOKEN_TTL}}. Shown once after generation.</p>
            </div>
            <p class="token-help">This token lets your LLM publish a document to Tabucom. Generate one, copy it, and give it to the LLM when you ask it to publish.</p>
            <div data-token-result hidden>
              <textarea class="token-output" data-token-output readonly aria-label="Generated publish token"></textarea>
              <div class="token-row">
                <button class="token-copy" type="button" data-copy-token>Copy token</button>
              </div>
            </div>
            <p class="token-status" data-token-status role="status"></p>
          </div>`

const llmsTokenInstructions = `
## Publish tokens

This Tabucom instance requires a stateless publish token.

1. First generate a publish token from the Tabucom UI.
2. Pass it to the LLM or harness as ` + "`TABUCOM_PUBLISH_TOKEN`" + `.
3. Publish with ` + "`Authorization: Bearer $TABUCOM_PUBLISH_TOKEN`" + `.
`

const llmsAPIKeyInstructions = `
## Publish API key

This Tabucom instance requires an operator-provided publish API key.

1. Pass it to the LLM or harness as ` + "`TABUCOM_PUBLISH_API_KEY`" + `.
2. Publish with ` + "`X-API-Key: $TABUCOM_PUBLISH_API_KEY`" + `.
`

const agentTokenInstructions = ` First generate a publish token from the Tabucom UI, pass it as TABUCOM_PUBLISH_TOKEN, and publish with Authorization: Bearer $TABUCOM_PUBLISH_TOKEN.`
const agentAPIKeyInstructions = ` Pass the operator-provided key as TABUCOM_PUBLISH_API_KEY and publish with X-API-Key: $TABUCOM_PUBLISH_API_KEY.`
const openPublishSummary = `Publishing requires no authentication; individual deployments may use one visitor password.`
const tokenPublishSummary = `Publishing requires a token generated from the Tabucom UI; individual deployments may also use one visitor password.`
const apiKeyPublishSummary = `Publishing requires an operator-provided API key; individual deployments may use one visitor password.`
const apiKeyAndTokenPublishSummary = `Publishing requires both an operator-provided API key and a token generated from the Tabucom UI; individual deployments may use one visitor password.`

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
	data = s.applyFeaturePlaceholders(data)
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
	data = s.applyFeaturePlaceholders(data)

	if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method == http.MethodGet {
		_, _ = w.Write(data)
	}
}

func (s *Server) applyFeaturePlaceholders(data []byte) []byte {
	tokenHTML := ""
	tokenInstructions := ""
	apiKeyInstructions := ""
	tokenAgentInstructions := ""
	apiKeyAgentInstructions := ""
	publishingAuthentication := "none"
	publishSummary := openPublishSummary
	if len(s.cfg.PublishAPIKeys) > 0 {
		apiKeyInstructions = llmsAPIKeyInstructions
		apiKeyAgentInstructions = agentAPIKeyInstructions
		publishingAuthentication = "api_key"
		publishSummary = apiKeyPublishSummary
	}
	if s.cfg.StatelessPublishTokensEnabled {
		tokenHTML = strings.ReplaceAll(tokenUI, "{{TOKEN_TTL}}", publishTokenTTLLabel(s.cfg.StatelessPublishTokenTTL))
		tokenInstructions = llmsTokenInstructions
		tokenAgentInstructions = agentTokenInstructions
		if len(s.cfg.PublishAPIKeys) > 0 {
			publishingAuthentication = "api_key_and_stateless_bearer_token"
			publishSummary = apiKeyAndTokenPublishSummary
		} else {
			publishingAuthentication = "stateless_bearer_token"
			publishSummary = tokenPublishSummary
		}
	}
	data = bytes.ReplaceAll(data, []byte("{{TOKEN_UI}}"), []byte(tokenHTML))
	data = bytes.ReplaceAll(data, []byte("\n{{PUBLISH_TOKEN_INSTRUCTIONS}}"), []byte(tokenInstructions))
	data = bytes.ReplaceAll(data, []byte("\n{{PUBLISH_API_KEY_INSTRUCTIONS}}"), []byte(apiKeyInstructions))
	data = bytes.ReplaceAll(data, []byte("{{AGENT_API_KEY_INSTRUCTIONS}}"), []byte(apiKeyAgentInstructions))
	data = bytes.ReplaceAll(data, []byte("{{AGENT_TOKEN_INSTRUCTIONS}}"), []byte(tokenAgentInstructions))
	data = bytes.ReplaceAll(data, []byte("{{PUBLISHING_AUTHENTICATION}}"), []byte(publishingAuthentication))
	data = bytes.ReplaceAll(data, []byte("{{PUBLISH_AUTH_SUMMARY}}"), []byte(publishSummary))
	return data
}

// publishTokenTTLLabel preserves the landing page's familiar default wording
// while showing custom durations exactly as operators configure them.
func publishTokenTTLLabel(ttl time.Duration) string {
	if ttl == time.Hour {
		return "1 hour"
	}
	return ttl.String()
}
