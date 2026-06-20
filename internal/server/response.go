package server

import (
	"encoding/json"
	"net/http"
)

// apiErrorEnvelope gives every API failure the same machine-readable shape.
type apiErrorEnvelope struct {
	Error apiErrorBody `json:"error"`
}

// apiErrorBody separates a stable programmatic code from its human explanation.
type apiErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// jsonReply applies API cache and content-type policy in one place. Encoding
// errors cannot be reported after headers are committed, so response values are
// intentionally limited to JSON-safe internal structs and maps.
func jsonReply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// apiError writes the common error envelope without exposing internal errors.
func apiError(w http.ResponseWriter, status int, code, message string) {
	jsonReply(w, status, apiErrorEnvelope{
		Error: apiErrorBody{Code: code, Message: message},
	})
}
