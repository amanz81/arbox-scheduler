package main

// HTTP auth middleware for the LLM-facing API.
//
// Two tokens are supported:
//
//   * ARBOX_API_READ_TOKEN  — read-only, accepted on every endpoint.
//   * ARBOX_API_ADMIN_TOKEN — admin, required for all mutations AND on top
//                             of ?confirm=1 for the call to actually hit
//                             the upstream Arbox API.
//
// Tokens are compared with crypto/subtle.ConstantTimeCompare. On a missing
// or wrong token we return 401 with WWW-Authenticate: Bearer. A read token
// used against an admin-only route returns 403.
//
// Token kinds are surfaced to handlers via context (see tokenKindFromCtx)
// so the audit log can distinguish read vs admin actors even though the
// admin token can read everything.

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
)

type tokenKind string

const (
	tokenKindRead  tokenKind = "read"
	tokenKindAdmin tokenKind = "admin"
)

type ctxKey int

const (
	ctxKeyTokenKind ctxKey = iota
	ctxKeyToken
)

// authMiddleware holds the configured tokens. Tokens are provided once at
// boot from env vars; rotating the API tokens requires a daemon restart
// (update your host's secret store + redeploy).
type authMiddleware struct {
	readToken  string
	adminToken string
}

func newAuthMiddleware(read, admin string) *authMiddleware {
	return &authMiddleware{
		readToken:  strings.TrimSpace(read),
		adminToken: strings.TrimSpace(admin),
	}
}

// extractBearer parses "Authorization: Bearer <tok>" and returns the token
// (or "" if missing).
func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// classify returns the kind of token this request is using, or ""+false if
// the bearer doesn't match either configured token.
func (a *authMiddleware) classify(tok string) (tokenKind, bool) {
	if tok == "" {
		return "", false
	}
	if a.adminToken != "" && constantTimeEq(tok, a.adminToken) {
		return tokenKindAdmin, true
	}
	if a.readToken != "" && constantTimeEq(tok, a.readToken) {
		return tokenKindRead, true
	}
	return "", false
}

// requireRead allows either token kind (admin can do everything).
func (a *authMiddleware) requireRead(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := extractBearer(r)
		kind, ok := a.classify(tok)
		if !ok {
			writeUnauthorized(w)
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyTokenKind, kind)
		ctx = context.WithValue(ctx, ctxKeyToken, tok)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireAdmin requires the admin token. (?confirm=1 is enforced separately
// inside each mutating handler so the dry-run preview can still be returned
// to a valid admin caller without confirm.)
func (a *authMiddleware) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := extractBearer(r)
		kind, ok := a.classify(tok)
		if !ok {
			writeUnauthorized(w)
			return
		}
		if kind != tokenKindAdmin {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error": "admin token required for this endpoint",
				"code":  "forbidden",
			})
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyTokenKind, kind)
		ctx = context.WithValue(ctx, ctxKeyToken, tok)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// tokenKindFromCtx reads the kind set by requireRead/requireAdmin.
func tokenKindFromCtx(ctx context.Context) tokenKind {
	if v, ok := ctx.Value(ctxKeyTokenKind).(tokenKind); ok {
		return v
	}
	return ""
}

func tokenFromCtx(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyToken).(string); ok {
		return v
	}
	return ""
}

func constantTimeEq(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="arbox"`)
	writeJSON(w, http.StatusUnauthorized, map[string]any{
		"error": "missing or invalid bearer token",
		"code":  "unauthorized",
	})
}

// maskToken returns "abcd…" — first 4 chars + ellipsis. Used in logs so we
// never write a raw bearer to stdout.
func maskAPIToken(tok string) string {
	if len(tok) <= 4 {
		return "…"
	}
	return tok[:4] + "…"
}
