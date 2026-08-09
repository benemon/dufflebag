package audit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/google/uuid"
)

type eventKind string

const (
	eventKindRequest  eventKind = "request"
	eventKindResponse eventKind = "response"
)

// Descriptor is the identity of the route selected before authentication.
// Semantic operation and target fields arrive with the plane tables that own them.
type Descriptor struct {
	RouteID            string
	Exempt             bool
	HandlerlessReason  string
	Operation          identity.AuditOperation
	TargetType         string
	TargetIDParam      string
	TargetID           string
	OperationID        string
	SystemWhenDisabled bool
}

// Resolver reports the route the production mux will select for a request.
type Resolver interface {
	Resolve(*http.Request) Descriptor
}

// PathValue extracts one single-segment wildcard from a ServeMux pattern.
func PathValue(pattern, path, name string) string {
	if name == "" {
		return ""
	}
	if _, route, ok := strings.Cut(pattern, " "); ok {
		pattern = route
	}
	patternParts := strings.Split(strings.Trim(pattern, "/"), "/")
	pathParts := strings.Split(strings.Trim(path, "/"), "/")
	if len(patternParts) != len(pathParts) {
		return ""
	}
	wildcard := "{" + name + "}"
	for i, part := range patternParts {
		if part == wildcard {
			return pathParts[i]
		}
	}
	return ""
}

// Writer is the broker operation the HTTP seam needs.
type Writer interface {
	Write([]byte) error
}

type disabledSystemWriter interface {
	Enabled() bool
	WriteSystem([]byte) error
}

// Enrichment is response information learned while a handler executes. It
// deliberately cannot carry an operation or target type: those come only from
// the pre-authentication route descriptor.
type Enrichment struct {
	PrincipalID    string
	PrincipalName  string
	IdentityKind   identity.IdentityKind
	Scope          identity.AuditScope
	OrganizationID string
	ProjectID      string
	TargetID       string
	Outcome        identity.AuditOutcome
	Reason         string
}

// Handle accumulates response facts on the request selected by the audit seam.
type Handle struct {
	correlationID   string
	hmacKeyVersion  string
	hmacKey         []byte
	enrichment      Enrichment
	clientSecret    string
	accessToken     string
	bootstrapSecret string
	recoveryShares  []string
	hooksMu         sync.Mutex
	hooks           []func()
	hooksOnce       sync.Once
}

// AfterResponse registers work that must run after the response audit write is
// attempted. It returns false for deliberately uncomposed callers, which have
// no seam capable of committing the hook.
func (h *Handle) AfterResponse(hook func()) bool {
	if h.correlationID == "" {
		return false
	}
	h.hooksMu.Lock()
	defer h.hooksMu.Unlock()
	h.hooks = append(h.hooks, hook)
	return true
}

func (h *Handle) runAfterResponse() {
	h.hooksOnce.Do(func() {
		h.hooksMu.Lock()
		hooks := append([]func(){}, h.hooks...)
		h.hooks = nil
		h.hooksMu.Unlock()
		for _, hook := range hooks {
			hook()
		}
	})
}

type handleKey struct{}

// CorrelationID returns the seam-owned request id. Direct handler tests and
// other deliberately uncomposed callers still get a usable, non-empty id.
func CorrelationID(ctx context.Context) string {
	if requestHandle, ok := ctx.Value(handleKey{}).(*Handle); ok {
		return requestHandle.correlationID
	}
	return uuid.NewString()
}

// FromContext returns the response handle installed by the seam. Calls made by
// deliberately uncomposed handler tests receive an inert handle.
func FromContext(ctx context.Context) *Handle {
	if requestHandle, ok := ctx.Value(handleKey{}).(*Handle); ok {
		return requestHandle
	}
	return &Handle{}
}

// Enrich refines fields learned during request execution. Empty values leave
// earlier facts untouched so a later branch cannot erase attribution.
func (h *Handle) Enrich(refinement Enrichment) {
	if h.correlationID == "" {
		return
	}
	if refinement.PrincipalID != "" {
		h.enrichment.PrincipalID = refinement.PrincipalID
	}
	if refinement.PrincipalName != "" {
		h.enrichment.PrincipalName = refinement.PrincipalName
	}
	if refinement.IdentityKind != "" {
		h.enrichment.IdentityKind = refinement.IdentityKind
	}
	if refinement.Scope != "" {
		h.enrichment.Scope = refinement.Scope
	}
	if refinement.OrganizationID != "" {
		h.enrichment.OrganizationID = refinement.OrganizationID
	}
	if refinement.ProjectID != "" {
		h.enrichment.ProjectID = refinement.ProjectID
	}
	if refinement.TargetID != "" {
		h.enrichment.TargetID = refinement.TargetID
	}
	if refinement.Outcome != "" {
		h.enrichment.Outcome = refinement.Outcome
	}
	if refinement.Reason != "" {
		h.enrichment.Reason = refinement.Reason
	}
}

func (h *Handle) ClientSecret(value string) {
	h.clientSecret = value
}

func (h *Handle) AccessToken(value string) {
	h.accessToken = value
}

func (h *Handle) BootstrapSecret(value string) {
	h.bootstrapSecret = value
}

// RecoveryShare records one recovery share crossing this response — minted by
// /sys/init or presented to /sys/recovery. HMACs rather than values, so the
// trail can say WHICH shares were used without holding a usable credential
// (ADR-0020), and recorded on refusals too: a failed ceremony naming the
// shares it was attempted with is exactly the entry an investigation wants.
func (h *Handle) RecoveryShare(value string) {
	if value != "" {
		h.recoveryShares = append(h.recoveryShares, value)
	}
}

func (h *Handle) digest(value string) string {
	if len(h.hmacKey) == 0 || value == "" {
		return ""
	}
	digest := hmac.New(sha256.New, h.hmacKey)
	_, _ = digest.Write([]byte(value))
	return hex.EncodeToString(digest.Sum(nil))
}

type requestRecord struct {
	SchemaVersion     int                     `json:"schema_version"`
	Kind              eventKind               `json:"kind"`
	CorrelationID     string                  `json:"correlation_id"`
	OccurredAt        time.Time               `json:"occurred_at"`
	HMACKeyVersion    string                  `json:"hmac_key_version,omitempty"`
	Method            string                  `json:"method"`
	Path              string                  `json:"path"`
	RemoteAddr        string                  `json:"remote_addr"`
	ForwardedFor      string                  `json:"forwarded_for,omitempty"`
	UserAgent         string                  `json:"user_agent,omitempty"`
	IdentityKind      identity.IdentityKind   `json:"identity_kind"`
	AuthorizationHMAC string                  `json:"authorization_hmac,omitempty"`
	Operation         identity.AuditOperation `json:"operation,omitempty"`
	TargetType        string                  `json:"target_type,omitempty"`
	TargetID          string                  `json:"target_id,omitempty"`
	PrincipalID       string                  `json:"principal_id,omitempty"`
	PrincipalName     string                  `json:"principal_name,omitempty"`
	Scope             identity.AuditScope     `json:"scope,omitempty"`
	OrganizationID    string                  `json:"organization_id,omitempty"`
	ProjectID         string                  `json:"project_id,omitempty"`
}

type responseRecord struct {
	SchemaVersion       int                     `json:"schema_version"`
	Kind                eventKind               `json:"kind"`
	CorrelationID       string                  `json:"correlation_id"`
	OccurredAt          time.Time               `json:"occurred_at"`
	HMACKeyVersion      string                  `json:"hmac_key_version,omitempty"`
	RouteID             string                  `json:"route_id"`
	Status              int                     `json:"status"`
	Bytes               int64                   `json:"bytes"`
	Outcome             identity.AuditOutcome   `json:"outcome"`
	Reason              string                  `json:"reason,omitempty"`
	Operation           identity.AuditOperation `json:"operation"`
	TargetType          string                  `json:"target_type"`
	TargetID            string                  `json:"target_id,omitempty"`
	PrincipalID         string                  `json:"principal_id,omitempty"`
	PrincipalName       string                  `json:"principal_name,omitempty"`
	IdentityKind        identity.IdentityKind   `json:"identity_kind,omitempty"`
	Scope               identity.AuditScope     `json:"scope,omitempty"`
	OrganizationID      string                  `json:"organization_id,omitempty"`
	ProjectID           string                  `json:"project_id,omitempty"`
	ClientSecretHMAC    string                  `json:"client_secret_hmac,omitempty"`
	AccessTokenHMAC     string                  `json:"access_token_hmac,omitempty"`
	BootstrapSecretHMAC string                  `json:"bootstrap_secret_hmac,omitempty"`
	RecoveryShareHMACs  []string                `json:"recovery_share_hmacs,omitempty"`
}

// HTTPHandler owns the request/response pair around the root mux.
type HTTPHandler struct {
	writer   Writer
	resolver Resolver
	next     http.Handler
	now      func() time.Time
	hmacKey  func() (string, []byte)
}

// StaticHMACKey returns a source for deployments whose audit key comes from
// environment configuration rather than the rotating keyring.
func StaticHMACKey(version string, key []byte) func() (string, []byte) {
	key = append([]byte(nil), key...)
	return func() (string, []byte) { return version, append([]byte(nil), key...) }
}

// NewHTTPHandler places the fail-closed request write and observed response
// write around the handler selected by resolver.
func NewHTTPHandler(
	writer Writer, resolver Resolver, next http.Handler, hmacKey func() (string, []byte),
) *HTTPHandler {
	return &HTTPHandler{
		writer: writer, resolver: resolver, next: next, now: time.Now,
		hmacKey: hmacKey,
	}
}

func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	descriptor := h.resolver.Resolve(r)
	if descriptor.Exempt {
		h.next.ServeHTTP(w, r)
		return
	}

	hmacKeyVersion, hmacKey := h.hmacKey()
	requestHandle := &Handle{
		correlationID: uuid.NewString(), hmacKeyVersion: hmacKeyVersion,
		hmacKey: append([]byte(nil), hmacKey...),
	}
	r = r.WithContext(context.WithValue(r.Context(), handleKey{}, requestHandle))
	observed := &responseObserver{ResponseWriter: w}

	request := requestRecord{
		SchemaVersion:     2,
		Kind:              eventKindRequest,
		CorrelationID:     requestHandle.correlationID,
		OccurredAt:        h.now().UTC(),
		HMACKeyVersion:    requestHandle.hmacKeyVersion,
		Method:            r.Method,
		Path:              r.URL.Path,
		RemoteAddr:        r.RemoteAddr,
		ForwardedFor:      r.Header.Get("X-Forwarded-For"),
		UserAgent:         r.UserAgent(),
		IdentityKind:      identity.IdentityKindAnonymous,
		AuthorizationHMAC: requestHandle.digest(r.Header.Get("Authorization")),
	}
	if err := h.write(request, descriptor.SystemWhenDisabled); err != nil {
		http.Error(observed, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		h.writeResponse(observed, r, descriptor, requestHandle, "audit_unavailable")
		return
	}

	defer func() {
		panicValue := recover()
		committedBeforePanic := observed.committed
		reason := ""
		if panicValue != nil {
			if committedBeforePanic {
				reason = "panic_after_write"
			} else {
				reason = "panic"
				http.Error(observed, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}
		}
		observed.ensureHeader()
		h.writeResponse(observed, r, descriptor, requestHandle, reason)
		if panicValue != nil && committedBeforePanic {
			// ErrAbortHandler makes net/http terminate the stream without adding a
			// second panic log. Flush first because net/http buffers small writes;
			// otherwise abort can discard bytes the handler already committed.
			observed.Flush()
			panic(http.ErrAbortHandler)
		}
	}()

	h.next.ServeHTTP(observed, r)
}

func (h *HTTPHandler) write(record any, systemWhenDisabled bool) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if writer, ok := h.writer.(disabledSystemWriter); ok && systemWhenDisabled && !writer.Enabled() {
		return writer.WriteSystem(encoded)
	}
	return h.writer.Write(encoded)
}

func (h *HTTPHandler) writeResponse(
	observed *responseObserver, request *http.Request, descriptor Descriptor,
	requestHandle *Handle, reason string,
) {
	defer requestHandle.runAfterResponse()
	hmacKeyVersion, hmacKey := h.hmacKey()
	requestHandle.hmacKeyVersion = hmacKeyVersion
	requestHandle.hmacKey = append(requestHandle.hmacKey[:0], hmacKey...)
	seamReason := reason != ""
	if reason == "" {
		reason = requestHandle.enrichment.Reason
	}
	if reason == "" {
		reason = descriptor.HandlerlessReason
	}
	if reason == "" {
		switch observed.status {
		case http.StatusMethodNotAllowed:
			reason = "method_not_allowed"
		}
	}
	outcome := requestHandle.enrichment.Outcome
	if outcome == "" || seamReason {
		outcome = outcomeForStatus(observed.status)
	}
	targetID := requestHandle.enrichment.TargetID
	if targetID == "" && descriptor.TargetIDParam != "" {
		targetID = request.PathValue(descriptor.TargetIDParam)
	}
	if targetID == "" {
		targetID = descriptor.TargetID
	}
	recoveryShareHMACs := make([]string, 0, len(requestHandle.recoveryShares))
	for _, share := range requestHandle.recoveryShares {
		if digest := requestHandle.digest(share); digest != "" {
			recoveryShareHMACs = append(recoveryShareHMACs, digest)
		}
	}
	_ = h.write(responseRecord{
		SchemaVersion:       2,
		Kind:                eventKindResponse,
		CorrelationID:       requestHandle.correlationID,
		OccurredAt:          h.now().UTC(),
		HMACKeyVersion:      requestHandle.hmacKeyVersion,
		RouteID:             descriptor.RouteID,
		Status:              observed.status,
		Bytes:               observed.bytes,
		Outcome:             outcome,
		Reason:              reason,
		Operation:           descriptor.Operation,
		TargetType:          descriptor.TargetType,
		TargetID:            targetID,
		PrincipalID:         requestHandle.enrichment.PrincipalID,
		PrincipalName:       requestHandle.enrichment.PrincipalName,
		IdentityKind:        requestHandle.enrichment.IdentityKind,
		Scope:               requestHandle.enrichment.Scope,
		OrganizationID:      requestHandle.enrichment.OrganizationID,
		ProjectID:           requestHandle.enrichment.ProjectID,
		ClientSecretHMAC:    requestHandle.digest(requestHandle.clientSecret),
		AccessTokenHMAC:     requestHandle.digest(requestHandle.accessToken),
		BootstrapSecretHMAC: requestHandle.digest(requestHandle.bootstrapSecret),
		RecoveryShareHMACs:  recoveryShareHMACs,
	}, descriptor.SystemWhenDisabled)
}

func outcomeForStatus(status int) identity.AuditOutcome {
	switch {
	case status >= 200 && status < 400:
		return identity.AuditOutcomeSuccess
	case status >= 400 && status < 500:
		return identity.AuditOutcomeRefused
	default:
		return identity.AuditOutcomeFailure
	}
}

type responseObserver struct {
	http.ResponseWriter
	status    int
	bytes     int64
	committed bool
}

func (w *responseObserver) WriteHeader(status int) {
	if w.committed {
		return
	}
	w.ResponseWriter.WriteHeader(status)
	w.status = status
	w.committed = true
}

func (w *responseObserver) Write(body []byte) (int, error) {
	w.ensureHeader()
	written, err := w.ResponseWriter.Write(body)
	w.bytes += int64(written)
	return written, err
}

func (w *responseObserver) Flush() {
	w.ensureHeader()
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *responseObserver) ReadFrom(reader io.Reader) (int64, error) {
	w.ensureHeader()
	if readerFrom, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		read, err := readerFrom.ReadFrom(reader)
		w.bytes += read
		return read, err
	}
	read, err := io.Copy(struct{ io.Writer }{w}, reader)
	return read, err
}

func (w *responseObserver) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *responseObserver) ensureHeader() {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
}
