// Package hcpauth serves the OAuth2 token endpoint the HCP SDK authenticates
// against.
//
// In HCP this is a different host from the API (HCP_AUTH_URL rather than
// HCP_API_ADDRESS), so it is a separate package even though one process serves
// both. Keeping them apart means the auth surface can be moved to its own
// listener without untangling it from the registry first.
//
// # Deployment: one hostname, no path prefix
//
// A single hostname can serve both surfaces, because their path trees do not
// collide — /oauth2/token here, /packer/... on the registry. What is NOT
// possible is mounting either behind a path prefix.
//
// For client credentials the SDK builds the token URL by ASSIGNING the path,
// not appending it (config/tokensource.go):
//
//	tokenURL := c.authURL
//	tokenURL.Path = AuthEndpointTokenPath // "/oauth2/token"
//
// so HCP_AUTH_URL=https://host/auth requests https://host/oauth2/token and the
// prefix is discarded silently — a 404 with nothing to explain it. Note this
// disagrees with config/with.go, which concatenates for the browser login flow;
// the assigning path is the one service principals take.
//
// HCP_API_ADDRESS is a host rather than a URL: httptransport.New receives it
// with an empty basePath, so the registry paths come from the spec.
//
// The auth URL must also be https — config/hcp.go rejects any other scheme
// outright, including on a private network.
package hcpauth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/domain/identity"
)

// TokenPath is where the SDK posts its client-credentials grant.
const TokenPath = "/oauth2/token"

// Principals loads a principal by the client id presented at authentication.
type Principals interface {
	GetPrincipalByClientID(ctx context.Context, clientID string) (*identity.Principal, error)
	TouchSecretLastUsed(ctx context.Context, secretID string, at time.Time) error
}

// Issuer mints a token for a principal that proved its credential.
//
// TTL is needed because expires_in is part of the OAuth2 response, and reading
// it from the issuer keeps one source of truth rather than a second copy of the
// lifetime that could drift from the one baked into the token.
type Issuer interface {
	Issue(principal *identity.Principal, credential string) (token string, secretID string, err error)
	TTL() time.Duration
}

// maxTokenRequestBytes bounds the form body. A client-credentials grant is a
// few hundred bytes; without a limit the endpoint reads whatever is sent before
// deciding anything about it.
const maxTokenRequestBytes = 8 << 10

type handler struct {
	principals     Principals
	issuer         Issuer
	logger         *slog.Logger
	throttle       *throttle
	trustedProxies []netip.Prefix
	mux            *http.ServeMux
}

type admittedKey struct{}

// NewHandler serves the token endpoint.
func NewHandler(
	principals Principals, issuer Issuer, logger *slog.Logger, trustedProxies ...netip.Prefix,
) *handler {
	return newHandler(principals, issuer, logger, trustedProxies, time.Now)
}

func newHandler(
	principals Principals, issuer Issuer, logger *slog.Logger, trustedProxies []netip.Prefix,
	now func() time.Time,
) *handler {
	h := &handler{
		principals:     principals,
		issuer:         issuer,
		logger:         logger,
		throttle:       newThrottle(now),
		trustedProxies: trustedProxies,
		mux:            http.NewServeMux(),
	}
	h.mux.HandleFunc("POST "+TokenPath, h.token)
	return h
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

// Admit keeps anonymous request volume outside the audit seam. The marker
// prevents the token handler from charging the same request a second time.
//
// limited names further anonymous surfaces (POST paths, or explicit
// "METHOD /path") sharing this endpoint's per-caller buckets, so the
// audit-amplification bound the buckets exist for (ADR-0020) holds across the
// set rather than doubling per surface.
func (h *handler) Admit(next http.Handler, limited ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := callerKey(r.RemoteAddr, r.Header.Values("X-Forwarded-For"), h.trustedProxies)
		if r.Method == http.MethodPost && r.URL.Path == TokenPath {
			if !h.throttle.allow(key) {
				writeRetry(w, http.StatusTooManyRequests, "too many token requests")
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), admittedKey{}, true))
		}
		limitedRequest := r.Method == http.MethodPost && slices.Contains(limited, r.URL.Path)
		limitedRequest = limitedRequest || slices.Contains(limited, r.Method+" "+r.URL.Path)
		if limitedRequest {
			if !h.throttle.allow(key) {
				w.Header().Set("Retry-After", "1")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"message":"too many requests"}`))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func callerKey(remoteAddr string, forwardedFor []string, trustedProxies []netip.Prefix) string {
	peer, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		peer = remoteAddr
	}
	peerAddress, err := netip.ParseAddr(peer)
	if err != nil || !containsAddress(trustedProxies, peerAddress) {
		return peer
	}
	if len(forwardedFor) == 0 {
		return peer
	}

	entries := strings.Split(strings.Join(forwardedFor, ","), ",")
	for i := len(entries) - 1; i >= 0; i-- {
		entry := strings.TrimSpace(entries[i])
		address, err := netip.ParseAddr(entry)
		if err != nil || !containsAddress(trustedProxies, address) {
			// The rightmost untrusted entry was appended by the last trusted hop,
			// so it is the one address the client cannot forge. Anything to its
			// left is client-controlled and deliberately never consulted.
			return entry
		}
	}
	return peer
}

func containsAddress(prefixes []netip.Prefix, address netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

// tokenResponse is the OAuth2 success document. The SDK performs no validation
// of the contents — oauth2.Transport attaches access_token as a bearer token.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

func (h *handler) token(w http.ResponseWriter, r *http.Request) {
	event := audit.Enrichment{
		PrincipalID:  "anonymous",
		IdentityKind: identity.IdentityKindAnonymous,
		Scope:        identity.AuditScopePlatform,
		Outcome:      identity.AuditOutcomeRefused,
	}
	defer func() { audit.FromContext(r.Context()).Enrich(event) }()

	// Refused before the body is read, so a throttled caller costs a header
	// parse and nothing else.
	if admitted, _ := r.Context().Value(admittedKey{}).(bool); !admitted &&
		!h.throttle.allow(callerKey(r.RemoteAddr, r.Header.Values("X-Forwarded-For"), h.trustedProxies)) {
		event.Reason = "rate_limited"
		writeRetry(w, http.StatusTooManyRequests, "too many token requests")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTokenRequestBytes)

	if err := r.ParseForm(); err != nil {
		event.Reason = "invalid_request"
		writeError(w, http.StatusBadRequest, "invalid_request", "the request body could not be parsed")
		return
	}

	if grant := r.PostForm.Get("grant_type"); grant != "client_credentials" {
		event.Reason = "unsupported_grant_type"
		writeError(w, http.StatusBadRequest, "unsupported_grant_type",
			"only the client_credentials grant is supported")
		return
	}

	// An audience is accepted when absent because not every client sends one,
	// but a mismatched audience is refused rather than quietly issuing a token
	// for a service the caller did not ask for.
	if audience := r.PostForm.Get("audience"); audience != "" && audience != identity.TokenAudience {
		event.Reason = "unsupported_audience"
		writeError(w, http.StatusBadRequest, "invalid_request", "unsupported audience")
		return
	}

	clientID, clientSecret, ok := credentials(r)
	if !ok {
		event.Reason = "missing_credentials"
		writeError(w, http.StatusBadRequest, "invalid_request", "client credentials are required")
		return
	}
	audit.FromContext(r.Context()).ClientSecret(clientSecret)

	// Everything from here spends argon2id memory, including the lookup: a miss
	// verifies a dummy hash so a real client id cannot be identified by timing.
	// Saturation is answered rather than queued — a request that waits for
	// memory it may never get holds a connection while the pod dies anyway.
	if !h.throttle.acquire() {
		event.Outcome = identity.AuditOutcomeFailure
		event.Reason = "verification_capacity_exhausted"
		writeRetry(w, http.StatusServiceUnavailable, "authentication capacity exhausted")
		return
	}
	defer h.throttle.release()

	principal, err := h.principals.GetPrincipalByClientID(r.Context(), clientID)
	if err != nil {
		// A miss and a bad secret get the same answer for the same reason the
		// repository spends an argon2id verification on the miss path: telling
		// them apart enumerates valid client ids (ADR-0018).
		event.PrincipalID = "unknown"
		event.IdentityKind = identity.IdentityKindUnknown
		switch {
		case errors.Is(err, identity.ErrNotFound):
			event.Reason = "principal_not_found"
		case errors.Is(err, identity.ErrIntegrity):
			// A row failing keyring MAC verification (ADR-0024): the audit
			// trail names it while the wire stays an ordinary invalid_client.
			event.Reason = "identity_integrity_failed"
		default:
			event.Outcome = identity.AuditOutcomeFailure
			event.Reason = "principal_lookup_failed"
		}
		writeInvalidClient(w)
		return
	}
	event.PrincipalID = principal.ID
	event.PrincipalName = principal.Name
	event.IdentityKind = identity.IdentityKindServicePrincipal
	event.OrganizationID = principal.Scope.OrganizationID.String()
	event.Scope = identity.AuditScopeOrganization
	if !principal.Scope.OrganizationScoped() {
		event.ProjectID = principal.Scope.ProjectID.String()
		event.Scope = identity.AuditScopeProject
	}

	token, secretID, err := h.issuer.Issue(principal, clientSecret)
	if err != nil {
		if errors.Is(err, identity.ErrInvalid) {
			event.Reason = "invalid_credentials"
		} else {
			event.Outcome = identity.AuditOutcomeFailure
			event.Reason = "token_issue_failed"
		}
		writeInvalidClient(w)
		return
	}
	if err := h.principals.TouchSecretLastUsed(r.Context(), secretID, time.Now().UTC()); err != nil {
		// Usage metadata must not turn an already-minted token into an auth
		// failure. No credential identifiers or secret material enter the log.
		h.logger.Warn("record principal secret use", "error", err)
	}

	event.Outcome = identity.AuditOutcomeSuccess
	audit.FromContext(r.Context()).AccessToken(token)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(tokenResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		ExpiresIn:   int(h.issuer.TTL().Seconds()),
	})
}

// credentials reads the client credentials, preferring the Authorization header.
//
// The SDK uses oauth2 with AuthStyleInHeader, so the header is the path that
// matters; form fields are accepted because the OAuth2 spec permits them and
// costs nothing to support.
func credentials(r *http.Request) (clientID, clientSecret string, ok bool) {
	if id, secret, present := r.BasicAuth(); present {
		return id, secret, id != ""
	}
	id, secret := r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	return id, secret, id != ""
}

func writeInvalidClient(w http.ResponseWriter) {
	// RFC 6749 §5.2: a 401 for invalid_client carries a challenge.
	w.Header().Set("WWW-Authenticate", `Basic realm="dufflebag"`)
	writeError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
}

func writeError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":             code,
		"error_description": description,
	})
}
