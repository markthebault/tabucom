/*
This file implements optional stateless publish tokens for Tabucom.
Tokens are signed bearer credentials for the publish endpoint only.
They carry scope, issued-at, and expiry claims and are never persisted.
The implementation uses HMAC-SHA256 and base64url from the standard library.
It depends on standard crypto, JSON, HTTP, string, and time packages only.
*/
package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

var errInvalidPublishToken = errors.New("invalid publish token")

type tokenHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

type publishTokenClaims struct {
	Scope string `json:"scope"`
	Iat   int64  `json:"iat"`
	Exp   int64  `json:"exp"`
}

type tokenResponse struct {
	Token      string    `json:"token"`
	ExpiresAt  time.Time `json:"expiresAt"`
	TTLSeconds int64     `json:"ttlSeconds"`
}

// publishToken issues a signed bearer token for agents and harnesses. The token
// is stateless; validation recomputes the signature and checks the claims.
func (s *Server) publishToken(w http.ResponseWriter, r *http.Request) {
	token, expiresAt, err := s.newPublishToken(s.cfg.Now().UTC(), s.cfg.StatelessPublishTokenTTL)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "internal_error", "could not generate token")
		return
	}
	jsonReply(w, http.StatusCreated, tokenResponse{
		Token: token, ExpiresAt: expiresAt, TTLSeconds: int64(s.cfg.StatelessPublishTokenTTL.Seconds()),
	})
}

func (s *Server) requirePublishToken(r *http.Request) error {
	if !s.cfg.StatelessPublishTokensEnabled {
		return nil
	}
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return errInvalidPublishToken
	}
	return s.validatePublishToken(strings.TrimSpace(strings.TrimPrefix(header, "Bearer ")), s.cfg.Now().UTC())
}

func (s *Server) newPublishToken(now time.Time, ttl time.Duration) (string, time.Time, error) {
	expiresAt := now.Add(ttl)
	header, err := json.Marshal(tokenHeader{Alg: "HS256", Typ: "JWT"})
	if err != nil {
		return "", time.Time{}, err
	}
	claims, err := json.Marshal(publishTokenClaims{
		Scope: "publish",
		Iat:   now.Unix(),
		Exp:   expiresAt.Unix(),
	})
	if err != nil {
		return "", time.Time{}, err
	}

	unsigned := encodeTokenPart(header) + "." + encodeTokenPart(claims)
	return unsigned + "." + s.signToken(unsigned), expiresAt, nil
}

func (s *Server) validatePublishToken(token string, now time.Time) error {
	first, rest, ok := strings.Cut(token, ".")
	if !ok {
		return errInvalidPublishToken
	}
	second, signature, ok := strings.Cut(rest, ".")
	if !ok || strings.Contains(signature, ".") {
		return errInvalidPublishToken
	}

	unsigned := first + "." + second
	if !hmac.Equal([]byte(signature), []byte(s.signToken(unsigned))) {
		return errInvalidPublishToken
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(first)
	if err != nil {
		return errInvalidPublishToken
	}
	var header tokenHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil || header.Alg != "HS256" || header.Typ != "JWT" {
		return errInvalidPublishToken
	}

	claimBytes, err := base64.RawURLEncoding.DecodeString(second)
	if err != nil {
		return errInvalidPublishToken
	}
	var claims publishTokenClaims
	if err := json.Unmarshal(claimBytes, &claims); err != nil {
		return errInvalidPublishToken
	}
	if claims.Scope != "publish" || claims.Exp <= now.Unix() {
		return errInvalidPublishToken
	}
	return nil
}

func (s *Server) signToken(unsigned string) string {
	mac := hmac.New(sha256.New, []byte(s.cfg.StatelessTokenSigningSecret))
	_, _ = mac.Write([]byte(unsigned))
	return encodeTokenPart(mac.Sum(nil))
}

func encodeTokenPart(value []byte) string {
	return base64.RawURLEncoding.EncodeToString(value)
}
