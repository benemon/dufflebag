package hcpauth

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/google/uuid"
)

type stubPrincipals struct {
	principal *identity.Principal
	calls     int
}

func (p *stubPrincipals) GetPrincipalByClientID(_ context.Context, id string) (*identity.Principal, error) {
	p.calls++
	if p.principal == nil || id != p.principal.ClientID {
		return nil, identity.ErrNotFound
	}
	return p.principal, nil
}

func (*stubPrincipals) TouchSecretLastUsed(context.Context, string, time.Time) error { return nil }

type stubIssuer struct{}

func (stubIssuer) Issue(*identity.Principal, string) (string, string, error) {
	return "token", "secret", nil
}
func (stubIssuer) TTL() time.Duration { return time.Minute }

func testHandler(t *testing.T, now func() time.Time) (*handler, *stubPrincipals) {
	t.Helper()
	principal, err := identity.RestorePrincipal(
		"p-1", "ci", "client-abc",
		identity.Scope{OrganizationID: uuid.MustParse("00000000-0000-4000-8000-000000000001")},
		identity.RoleBuilder, time.Now(), nil,
	)
	if err != nil {
		t.Fatalf("RestorePrincipal: %v", err)
	}
	principals := &stubPrincipals{principal: principal}
	return &handler{
		principals: principals,
		issuer:     stubIssuer{},
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		throttle:   newThrottle(now),
	}, principals
}

func tokenRequest(remoteAddr string) *http.Request {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {"client-abc"},
		"client_secret": {"whatever"},
	}
	r := httptest.NewRequest(http.MethodPost, TokenPath, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = remoteAddr
	return r
}

func TestCallerKey(t *testing.T) {
	trusted := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("2001:db8:ffff::/48"),
	}
	bareIPv4 := []netip.Prefix{
		netip.PrefixFrom(netip.MustParseAddr("192.0.2.10"), 32),
	}
	tests := []struct {
		name           string
		remoteAddr     string
		forwardedFor   []string
		trustedProxies []netip.Prefix
		want           string
	}{
		{
			name:         "unset configuration preserves the peer key and strips its port",
			remoteAddr:   "192.0.2.10:4321",
			forwardedFor: []string{"198.51.100.7"},
			want:         "192.0.2.10",
		},
		{
			name:           "trusted peer uses the rightmost untrusted chain entry",
			remoteAddr:     "10.0.0.3:4321",
			forwardedFor:   []string{"198.51.100.7, 10.0.0.2"},
			trustedProxies: trusted,
			want:           "198.51.100.7",
		},
		{
			name:           "first client through a shared trusted proxy",
			remoteAddr:     "10.0.0.3:4321",
			forwardedFor:   []string{"198.51.100.7"},
			trustedProxies: trusted,
			want:           "198.51.100.7",
		},
		{
			name:           "second client through a shared trusted proxy",
			remoteAddr:     "10.0.0.3:4321",
			forwardedFor:   []string{"203.0.113.8"},
			trustedProxies: trusted,
			want:           "203.0.113.8",
		},
		{
			name:           "untrusted peer cannot forge the forwarded chain",
			remoteAddr:     "203.0.113.9:4321",
			forwardedFor:   []string{"198.51.100.7"},
			trustedProxies: trusted,
			want:           "203.0.113.9",
		},
		{
			name:           "forged leftmost entry is never consulted",
			remoteAddr:     "10.0.0.3:4321",
			forwardedFor:   []string{"forged.example, 192.0.2.44"},
			trustedProxies: trusted,
			want:           "192.0.2.44",
		},
		{
			name:           "many forged leftmost entries are never consulted",
			remoteAddr:     "10.0.0.3:4321",
			forwardedFor:   []string{"forged-one, forged-two, 198.51.100.99, 192.0.2.44"},
			trustedProxies: trusted,
			want:           "192.0.2.44",
		},
		{
			name:           "trusted peer without a forwarded chain falls back to peer",
			remoteAddr:     "10.0.0.3:4321",
			trustedProxies: trusted,
			want:           "10.0.0.3",
		},
		{
			name:           "all trusted chain entries fall back to peer",
			remoteAddr:     "10.0.0.3:4321",
			forwardedFor:   []string{"10.0.0.1, 10.0.0.2"},
			trustedProxies: trusted,
			want:           "10.0.0.3",
		},
		{
			name:           "IPv6 peer and chain entries",
			remoteAddr:     "[2001:db8:ffff::3]:4321",
			forwardedFor:   []string{"2001:db8:1::7, 2001:db8:ffff::2"},
			trustedProxies: trusted,
			want:           "2001:db8:1::7",
		},
		{
			name:           "bare IPv4 configuration trusts exactly that address",
			remoteAddr:     "192.0.2.10:4321",
			forwardedFor:   []string{"198.51.100.7"},
			trustedProxies: bareIPv4,
			want:           "198.51.100.7",
		},
		{
			name:           "bare IPv4 configuration does not trust a neighbour",
			remoteAddr:     "192.0.2.11:4321",
			forwardedFor:   []string{"198.51.100.7"},
			trustedProxies: bareIPv4,
			want:           "192.0.2.11",
		},
		{
			name:           "chained trusted proxies are skipped across header values",
			remoteAddr:     "10.0.0.3:4321",
			forwardedFor:   []string{"203.0.113.7, 10.0.0.1", "10.0.0.2"},
			trustedProxies: trusted,
			want:           "203.0.113.7",
		},
		{
			name:           "malformed entry recorded by a trusted peer remains verbatim",
			remoteAddr:     "10.0.0.3:4321",
			forwardedFor:   []string{"198.51.100.7, recorded-by-proxy"},
			trustedProxies: trusted,
			want:           "recorded-by-proxy",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := callerKey(test.remoteAddr, test.forwardedFor, test.trustedProxies); got != test.want {
				t.Fatalf("caller key = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAdmitSeparatesForwardedCallersOnlyForTrustedPeers(t *testing.T) {
	trustedProxy := netip.MustParsePrefix("10.0.0.0/8")
	tests := []struct {
		name             string
		trustedProxies   []netip.Prefix
		secondClientWant int
	}{
		{
			name:             "trusted proxy gives callers independent buckets",
			trustedProxies:   []netip.Prefix{trustedProxy},
			secondClientWant: http.StatusOK,
		},
		{
			name:             "unset configuration keeps the shared peer bucket",
			secondClientWant: http.StatusTooManyRequests,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
			h, _ := testHandler(t, func() time.Time { return at })
			h.trustedProxies = test.trustedProxies
			admitted := h.Admit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			request := func(client string) *http.Request {
				r := tokenRequest("10.0.0.3:4321")
				r.Header.Set("X-Forwarded-For", client)
				return r
			}

			for i := range callerBurst {
				response := httptest.NewRecorder()
				admitted.ServeHTTP(response, request("198.51.100.7"))
				if response.Code != http.StatusOK {
					t.Fatalf("first client request %d within burst = %d", i+1, response.Code)
				}
			}
			response := httptest.NewRecorder()
			admitted.ServeHTTP(response, request("198.51.100.7"))
			if response.Code != http.StatusTooManyRequests {
				t.Fatalf("first client beyond burst = %d, want 429", response.Code)
			}

			response = httptest.NewRecorder()
			admitted.ServeHTTP(response, request("203.0.113.8"))
			if response.Code != test.secondClientWant {
				t.Fatalf("second client = %d, want %d", response.Code, test.secondClientWant)
			}
		})
	}
}

// The budget must actually bound something: permits sized from a memory budget
// mean nothing if the arithmetic yields a number so large it is never reached.
func TestVerificationPermitsAreBoundedByTheMemoryBudget(t *testing.T) {
	throttle := newThrottle(time.Now)
	permits := 0
	for throttle.acquire() {
		permits++
		if permits > 1000 {
			t.Fatal("permits are unbounded")
		}
	}
	if permits < 1 {
		t.Fatal("no verification may proceed at all")
	}
	if want := verificationMemoryBudget / identity.MaxVerificationMemoryBytes; permits != want {
		t.Fatalf("permits = %d, want %d", permits, want)
	}
	// Committed memory is returned, not leaked.
	throttle.release()
	if !throttle.acquire() {
		t.Fatal("a released permit was not reusable")
	}
}

// Saturation must be answered, not queued: the point is that the pod survives
// and says so, rather than being killed while requests wait.
func TestTokenEndpointRefusesWhenVerificationCapacityIsExhausted(t *testing.T) {
	h, principals := testHandler(t, time.Now)
	exhausted := false
	// Bounded: an acquire that never refuses must fail this test rather than
	// spin here forever.
	for range 1000 {
		if !h.throttle.acquire() {
			exhausted = true
			break
		}
	}
	if !exhausted {
		t.Fatal("the verification budget could not be exhausted")
	}

	response := httptest.NewRecorder()
	h.token(response, tokenRequest("192.0.2.1:1000"))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("saturated status = %d, want 503: %s", response.Code, response.Body)
	}
	if response.Header().Get("Retry-After") == "" {
		t.Fatal("saturated response carries no Retry-After")
	}
	if strings.Contains(response.Body.String(), "access_token") {
		t.Fatalf("saturated response issued a token: %s", response.Body)
	}
	// Refused BEFORE any argon2 work: the lookup is where the miss path spends
	// its equalising verification.
	if principals.calls != 0 {
		t.Fatalf("saturated request reached the principal lookup %d times", principals.calls)
	}

	h.throttle.release()
	response = httptest.NewRecorder()
	h.token(response, tokenRequest("192.0.2.1:1000"))
	if response.Code != http.StatusOK {
		t.Fatalf("status after capacity returned = %d, want 200: %s", response.Code, response.Body)
	}
}

func TestOneCallerCannotConsumeEveryoneElsesBudget(t *testing.T) {
	at := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	h, principals := testHandler(t, func() time.Time { return at })

	for i := range callerBurst {
		response := httptest.NewRecorder()
		h.token(response, tokenRequest("192.0.2.1:1000"))
		if response.Code != http.StatusOK {
			t.Fatalf("request %d within burst = %d: %s", i, response.Code, response.Body)
		}
	}
	before := principals.calls

	// The port differs, as it does for every real connection: the bucket is per
	// ADDRESS, not per connection, or a caller opening new sockets would evade it.
	response := httptest.NewRecorder()
	h.token(response, tokenRequest("192.0.2.1:2000"))
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("beyond burst = %d, want 429: %s", response.Code, response.Body)
	}
	if response.Header().Get("Retry-After") == "" {
		t.Fatal("throttled response carries no Retry-After")
	}
	if principals.calls != before {
		t.Fatal("a throttled request still spent a verification")
	}

	// Another address is unaffected — the limit isolates callers rather than
	// closing the endpoint.
	response = httptest.NewRecorder()
	h.token(response, tokenRequest("198.51.100.7:1000"))
	if response.Code != http.StatusOK {
		t.Fatalf("second caller = %d, want 200: %s", response.Code, response.Body)
	}
}

func TestTokenBurstCoversTwoMeasuredTerraformLanes(t *testing.T) {
	at := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	throttle := newThrottle(func() time.Time { return at })

	for i := range 64 {
		if !throttle.allow("192.0.2.1") {
			t.Fatalf("request %d within the supported CI burst was refused", i+1)
		}
	}
	if throttle.allow("192.0.2.1") {
		t.Fatal("request beyond the documented burst was admitted")
	}
}

// The caller table is unauthenticated state, so it must not become a second
// amplification. The first version of this bounded nothing — it dropped only
// entries idle past callerIdle and inserted regardless — and walked the whole
// table under the lock once full. Both are asserted here because neither was:
// the original test advanced the clock so the sweep always emptied everything,
// which is the one case where the broken code looked correct (duf-t0s).
func TestTheCallerTableIsBoundedUnderPressure(t *testing.T) {
	at := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	throttle := newThrottle(func() time.Time { return at })

	// The clock never advances, so nothing is idle: this is precisely the case
	// that grew without limit.
	const addresses = 50000
	started := time.Now()
	for i := range addresses {
		throttle.allow(distinctAddress(i))
		if tracked := throttle.tracked(); tracked > 2*maxTrackedCallers {
			t.Fatalf("tracked callers = %d after %d addresses, want at most %d",
				tracked, i+1, 2*maxTrackedCallers)
		}
	}
	elapsed := time.Since(started)

	// A guard against reintroducing a per-request scan, not a performance target.
	// Rotation makes this ~20ms, so the bound is roughly 25x headroom; the
	// version this replaced took 22.9 seconds for the same input. Deliberately
	// tight enough to catch a scan that is BOUNDED as well as one that is not —
	// walking a capped generation on every insert still costs ~1.3s here, which
	// a looser bound waved through when this was written.
	if elapsed > 500*time.Millisecond {
		t.Fatalf("%d addresses took %s, which means the table is being walked", addresses, elapsed)
	}
}

// Rotation must not be a way to earn a fresh burst: a caller being refused has
// to stay refused across one.
func TestARotationDoesNotResetACallersBucket(t *testing.T) {
	at := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	throttle := newThrottle(func() time.Time { return at })

	const address = "192.0.2.1"
	for range callerBurst {
		if !throttle.allow(address) {
			t.Fatal("refused within the burst")
		}
	}
	if throttle.allow(address) {
		t.Fatal("burst was not exhausted")
	}

	// Force a rotation with other addresses, then ask again.
	for i := range maxTrackedCallers {
		throttle.allow(distinctAddress(i))
	}
	if throttle.allow(address) {
		t.Fatal("a rotation handed the caller a fresh burst")
	}
}

// An address unseen for long enough is forgotten, so the table reflects current
// callers rather than every caller there has ever been.
func TestIdleCallersAreForgotten(t *testing.T) {
	at := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	throttle := newThrottle(func() time.Time { return at })

	for i := range 100 {
		throttle.allow(distinctAddress(i))
	}
	if tracked := throttle.tracked(); tracked != 100 {
		t.Fatalf("tracked callers = %d, want 100", tracked)
	}

	// Two generations, because an entry survives at most one rotation.
	for range 2 {
		at = at.Add(callerIdle + time.Minute)
		throttle.allow("203.0.113.1")
	}
	if tracked := throttle.tracked(); tracked != 1 {
		t.Fatalf("tracked callers after two rotations = %d, want 1", tracked)
	}
}

func distinctAddress(i int) string {
	return fmt.Sprintf("10.%d.%d.%d", i/65536%256, i/256%256, i%256)
}

// /sys/recovery shares the token endpoint's per-caller buckets (ADR-0024): one
// anonymous caller gets one budget across both surfaces, and the refusal
// happens outside the audit seam like the token endpoint's own.
func TestRecoveryPathSharesTheTokenCallerBudget(t *testing.T) {
	at := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	h, _ := testHandler(t, func() time.Time { return at })
	reached := 0
	admitted := h.Admit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusOK)
	}), "/sys/recovery")

	recoveryRequest := func(remoteAddr string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/sys/recovery", strings.NewReader(`{"shares":["x"]}`))
		r.RemoteAddr = remoteAddr
		return r
	}
	for i := range callerBurst {
		response := httptest.NewRecorder()
		admitted.ServeHTTP(response, recoveryRequest("192.0.2.1:1000"))
		if response.Code != http.StatusOK {
			t.Fatalf("recovery request %d within burst = %d: %s", i, response.Code, response.Body)
		}
	}

	response := httptest.NewRecorder()
	admitted.ServeHTTP(response, recoveryRequest("192.0.2.1:2000"))
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("recovery beyond burst = %d, want 429: %s", response.Code, response.Body)
	}
	if response.Header().Get("Retry-After") == "" {
		t.Fatal("throttled recovery carries no Retry-After")
	}
	// The platform plane's error shape, not OAuth's.
	if !strings.Contains(response.Body.String(), `"message"`) ||
		strings.Contains(response.Body.String(), "error_description") {
		t.Fatalf("throttled recovery body = %s, want platform error shape", response.Body)
	}
	before := reached

	// Shared budget: the same address is now also refused a token.
	response = httptest.NewRecorder()
	admitted.ServeHTTP(response, tokenRequest("192.0.2.1:3000"))
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("token after recovery burst = %d, want 429: %s", response.Code, response.Body)
	}
	if reached != before {
		t.Fatal("a throttled request still reached the audited handler")
	}

	// Another address is unaffected.
	response = httptest.NewRecorder()
	admitted.ServeHTTP(response, recoveryRequest("198.51.100.7:1000"))
	if response.Code != http.StatusOK {
		t.Fatalf("second caller = %d, want 200: %s", response.Code, response.Body)
	}

	// GET on the recovery path is not admitted through the buckets: only the
	// POST ceremony is anonymous work worth bounding, and everything else
	// still reaches the router to be refused there.
	response = httptest.NewRecorder()
	get := httptest.NewRequest(http.MethodGet, "/sys/recovery", nil)
	get.RemoteAddr = "192.0.2.1:4000"
	admitted.ServeHTTP(response, get)
	if response.Code != http.StatusOK {
		t.Fatalf("GET was refused by the admission layer = %d: %s", response.Code, response.Body)
	}
}
