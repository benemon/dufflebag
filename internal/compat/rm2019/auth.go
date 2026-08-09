package rm2019

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/domain/identity"
)

type verifiedKey struct{}

// authenticate rejects any request without a valid bearer token.
//
// These endpoints need no per-route authorization step, unlike the packer
// plane: there is no tenant in the path to check, because discovering the
// caller's tenants is the whole purpose of the call.
func authenticate(auth Authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := trimBearer(r.Header.Get("Authorization"))
		if !ok {
			audit.FromContext(r.Context()).Enrich(audit.Enrichment{
				PrincipalID: "anonymous", IdentityKind: identity.IdentityKindAnonymous,
				Scope: identity.AuditScopePlatform, Reason: "missing_token",
			})
			unauthenticated(w)
			return
		}
		verified, err := auth.Verify(token)
		if err != nil {
			audit.FromContext(r.Context()).Enrich(audit.Enrichment{
				PrincipalID:  "unknown",
				IdentityKind: identity.IdentityKindUnknown,
				Scope:        identity.AuditScopePlatform,
				Reason:       "invalid_token",
			})
			unauthenticated(w)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), verifiedKey{}, verified)))
	})
}

func unauthenticated(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="dufflebag"`)
	writeError(w, http.StatusUnauthorized, 16, "a bearer token is required")
}

// writeInternal records the failure and returns only a correlation id, so an
// internal error is not chattier than a deliberate refusal (ADR-0017).
func writeInternal(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	correlation := audit.CorrelationID(r.Context())
	logger.Error("resource-manager request failed", "error", err, "correlation_id", correlation)
	writeError(w, http.StatusInternalServerError, 13, "internal error; correlation id "+correlation)
}

// writeError emits the google.rpc.Status shape the SDK expects, matching the
// packer plane so a client sees one error vocabulary across both.
func writeError(w http.ResponseWriter, status int, code int32, message string) {
	writeJSON(w, status, map[string]any{
		"code":    code,
		"message": message,
		"details": []any{},
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
