package identity

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/argon2"
)

var (
	epoch = time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	orgA  = uuid.MustParse("00000000-0000-4000-8000-000000000001")
	orgB  = uuid.MustParse("00000000-0000-4000-8000-000000000002")
	projA = uuid.MustParse("00000000-0000-4000-8000-000000000101")
	projB = uuid.MustParse("00000000-0000-4000-8000-000000000102")
)

func newTestPrincipal(t *testing.T, scope Scope) (*Principal, string) {
	t.Helper()
	principal, err := NewPrincipal("p-1", "ci", "client-1", scope, RoleBuilder, epoch)
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	// Creation no longer mints a credential (duf-4ac), so the tests that need
	// one issue it the same way every caller does.
	secret, err := principal.IssueSecret("s-1", nil, epoch)
	if err != nil {
		t.Fatalf("IssueSecret: %v", err)
	}
	return principal, secret
}

// A principal exists before it can authenticate, and that state is ordinary
// rather than transitional: the console creates one, then issues separately.
func TestNewPrincipalHoldsNoSecrets(t *testing.T) {
	principal, err := NewPrincipal("p-1", "ci", "client-1", Scope{OrganizationID: orgA}, RoleBuilder, epoch)
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	if got := len(principal.Secrets()); got != 0 {
		t.Fatalf("a new principal holds %d secrets, want none", got)
	}
	if _, ok := principal.Authenticate("", epoch); ok {
		t.Fatal("a secretless principal authenticated")
	}
}

func TestNewPrincipalRejectsBadInput(t *testing.T) {
	full := Scope{OrganizationID: orgA, ProjectID: projA}
	for _, c := range []struct {
		name                string
		id, pname, clientID string
		scope               Scope
	}{
		{"empty id", "", "ci", "client-1", full},
		{"empty name", "p-1", "", "client-1", full},
		{"empty client id", "p-1", "ci", "", full},
		{"no organization", "p-1", "ci", "client-1", Scope{ProjectID: projA}},
		{"bucket without project", "p-1", "ci", "client-1", Scope{OrganizationID: orgA, BucketID: "01ARZ3NDEKTSV4RRFFQ69G5FAV"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewPrincipal(c.id, c.pname, c.clientID, c.scope, RoleBuilder, epoch); !errors.Is(err, ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
		})
	}
}

func TestAuthenticate(t *testing.T) {
	principal, secret := newTestPrincipal(t, Scope{OrganizationID: orgA, ProjectID: projA})

	if _, ok := principal.Authenticate(secret, epoch); !ok {
		t.Fatal("issued secret does not authenticate")
	}
	for _, wrong := range []string{"", secret + "x", strings.ToUpper(secret), "not-the-secret"} {
		if _, ok := principal.Authenticate(wrong, epoch); ok {
			t.Fatalf("authenticated with %q", wrong)
		}
	}
}

func TestAuthenticateAcrossUsableSecretCounts(t *testing.T) {
	scope := Scope{OrganizationID: orgA, ProjectID: projA}

	zero, err := NewPrincipal("p-zero", "zero", "client-zero", scope, RoleBuilder, epoch)
	if err != nil {
		t.Fatalf("zero-secret principal: %v", err)
	}
	if id, ok := zero.Authenticate("anything", epoch); ok || id != "" {
		t.Fatalf("zero usable secrets authenticated as %q", id)
	}

	one, err := NewPrincipal("p-one", "one", "client-one", scope, RoleBuilder, epoch)
	if err != nil {
		t.Fatalf("one-secret principal: %v", err)
	}
	onePlaintext, err := one.IssueSecret("one", nil, epoch)
	if err != nil {
		t.Fatalf("issue one secret: %v", err)
	}
	if id, ok := one.Authenticate(onePlaintext, epoch); !ok || id != "one" {
		t.Fatalf("one usable secret authenticated as %q,%v", id, ok)
	}

	two, err := NewPrincipal("p-two", "two", "client-two", scope, RoleBuilder, epoch)
	if err != nil {
		t.Fatalf("two-secret principal: %v", err)
	}
	first, err := two.IssueSecret("first", nil, epoch)
	if err != nil {
		t.Fatalf("issue first secret: %v", err)
	}
	second, err := two.IssueSecret("second", nil, epoch)
	if err != nil {
		t.Fatalf("issue second secret: %v", err)
	}
	for id, plaintext := range map[string]string{"first": first, "second": second} {
		if matched, ok := two.Authenticate(plaintext, epoch); !ok || matched != id {
			t.Fatalf("two usable secrets: %s authenticated as %q,%v", id, matched, ok)
		}
	}

	withExpired, err := NewPrincipal("p-expired", "expired", "client-expired", scope, RoleBuilder, epoch)
	if err != nil {
		t.Fatalf("expired-secret principal: %v", err)
	}
	expiresAt := epoch.Add(time.Hour)
	expiredPlaintext, err := withExpired.IssueSecret("expired", &expiresAt, epoch)
	if err != nil {
		t.Fatalf("issue expiring secret: %v", err)
	}
	afterExpiry := epoch.Add(2 * time.Hour)
	activePlaintext, err := withExpired.IssueSecret("active", nil, afterExpiry)
	if err != nil {
		t.Fatalf("issue active secret: %v", err)
	}
	if id, ok := withExpired.Authenticate(expiredPlaintext, afterExpiry); ok || id != "" {
		t.Fatalf("expired secret authenticated as %q", id)
	}
	if id, ok := withExpired.Authenticate(activePlaintext, afterExpiry); !ok || id != "active" {
		t.Fatalf("active secret beside expired one authenticated as %q,%v", id, ok)
	}
}

func TestSecretIsNotRecoverable(t *testing.T) {
	principal, secret := newTestPrincipal(t, Scope{OrganizationID: orgA, ProjectID: projA})

	for _, stored := range principal.Secrets() {
		if strings.Contains(stored.Encoded(), secret) {
			t.Fatal("the stored hash contains the plaintext secret")
		}
		if !strings.HasPrefix(stored.Encoded(), "$argon2id$") {
			t.Fatalf("stored hash is not argon2id: %s", stored.Encoded())
		}
	}
}

func TestRotationHasNoAuthenticationGap(t *testing.T) {
	principal, first := newTestPrincipal(t, Scope{OrganizationID: orgA, ProjectID: projA})

	second, err := principal.IssueSecret("s-2", nil, epoch.Add(time.Hour))
	if err != nil {
		t.Fatalf("IssueSecret: %v", err)
	}
	// The whole point: both work at once, so a redeploy can happen between.
	firstOK, secondOK := false, false
	if _, ok := principal.Authenticate(first, epoch); ok {
		firstOK = true
	}
	if _, ok := principal.Authenticate(second, epoch); ok {
		secondOK = true
	}
	if !firstOK || !secondOK {
		t.Fatal("both secrets must authenticate during rotation")
	}

	if err := principal.RevokeSecret("s-1", epoch); err != nil {
		t.Fatalf("RevokeSecret: %v", err)
	}
	if _, ok := principal.Authenticate(first, epoch); ok {
		t.Fatal("revoked secret still authenticates")
	}
	if _, ok := principal.Authenticate(second, epoch); !ok {
		t.Fatal("surviving secret stopped authenticating")
	}
}

func TestSecretCountIsCapped(t *testing.T) {
	principal, _ := newTestPrincipal(t, Scope{OrganizationID: orgA, ProjectID: projA})
	if _, err := principal.IssueSecret("s-2", nil, epoch); err != nil {
		t.Fatalf("second secret: %v", err)
	}
	if _, err := principal.IssueSecret("s-3", nil, epoch); !errors.Is(err, ErrConflict) {
		t.Fatalf("third secret should be refused, got %v", err)
	}
	if got := len(principal.Secrets()); got != 2 {
		t.Fatalf("holds %d secrets, want 2", got)
	}
}

// Root has nothing above it to re-issue on its behalf, so a secretless root is
// recoverable only by direct database access. It keeps the rule.
func TestRevokingRootsOnlySecretIsRefused(t *testing.T) {
	principal, err := NewPrincipal("p-root", "admin", "client-root", Scope{}, RoleRoot, epoch)
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	secret, err := principal.IssueSecret("s-1", nil, epoch)
	if err != nil {
		t.Fatalf("IssueSecret: %v", err)
	}

	if err := principal.RevokeSecret("s-1", epoch); !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	if _, ok := principal.Authenticate(secret, epoch); !ok {
		t.Fatal("a refused revocation must leave the secret working")
	}
	// A nonexistent secret answers not-found rather than conflict: the guard
	// asks what would SURVIVE the revocation, and revoking nothing leaves the
	// permanent secret standing. Secret existence is not concealed here — a
	// caller who may revoke can already list.
	if err := principal.RevokeSecret("nonexistent", epoch); !errors.Is(err, ErrNotFound) {
		t.Fatalf("nonexistent secret should be not-found, got %v", err)
	}
}

// Expiry extends the root rule (duf-2rw): a root holding only expiring secrets
// is the same lockout on a timer.
func TestRootMustKeepANeverExpiringSecret(t *testing.T) {
	principal, err := NewPrincipal("p-root", "admin", "client-root", Scope{}, RoleRoot, epoch)
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	if _, err := principal.IssueSecret("s-permanent", nil, epoch); err != nil {
		t.Fatalf("IssueSecret: %v", err)
	}
	expiry := epoch.Add(30 * 24 * time.Hour)
	if _, err := principal.IssueSecret("s-expiring", &expiry, epoch); err != nil {
		t.Fatalf("IssueSecret expiring: %v", err)
	}

	// Revoking the permanent one would leave root on a timer.
	if err := principal.RevokeSecret("s-permanent", epoch); !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict revoking the permanent secret, got %v", err)
	}
	// Revoking the expiring one is ordinary rotation.
	if err := principal.RevokeSecret("s-expiring", epoch); err != nil {
		t.Fatalf("revoking the expiring secret must be allowed: %v", err)
	}
}

// The cap counts USABLE secrets: an expired one grants nothing, so it must not
// force a revoke-first step in the middle of an outage (duf-2rw).
func TestSecretCapIgnoresExpiredSecrets(t *testing.T) {
	principal, _ := newTestPrincipal(t, Scope{OrganizationID: orgA, ProjectID: projA})
	soon := epoch.Add(time.Hour)
	if _, err := principal.IssueSecret("s-expiring", &soon, epoch); err != nil {
		t.Fatalf("IssueSecret: %v", err)
	}
	// At the cap while both are usable.
	if _, err := principal.IssueSecret("s-3", nil, epoch); !errors.Is(err, ErrConflict) {
		t.Fatalf("third usable secret should be refused, got %v", err)
	}
	// Once one expires, a replacement issues without revoking anything.
	later := epoch.Add(2 * time.Hour)
	if _, err := principal.IssueSecret("s-3", nil, later); err != nil {
		t.Fatalf("replacement after expiry should be allowed: %v", err)
	}
	if got := len(principal.Secrets()); got != 3 {
		t.Fatalf("holds %d secrets, want 3 stored (one expired)", got)
	}
}

// An expired secret fails authentication by the same path garbage does, and a
// token minted from it dies with it.
func TestExpiredSecretAuthenticatesNothing(t *testing.T) {
	principal, err := NewPrincipal("p-1", "svc", "client-1", Scope{OrganizationID: orgA, ProjectID: projA}, RoleBuilder, epoch)
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	expiry := epoch.Add(time.Hour)
	plaintext, err := principal.IssueSecret("s-exp", &expiry, epoch)
	if err != nil {
		t.Fatalf("IssueSecret: %v", err)
	}

	if _, ok := principal.Authenticate(plaintext, epoch); !ok {
		t.Fatal("an unexpired secret must authenticate")
	}
	if !principal.HasActiveSecret("s-exp", epoch) {
		t.Fatal("an unexpired secret is active")
	}
	afterExpiry := epoch.Add(2 * time.Hour)
	if _, ok := principal.Authenticate(plaintext, afterExpiry); ok {
		t.Fatal("an expired secret must not authenticate")
	}
	if principal.HasActiveSecret("s-exp", afterExpiry) {
		t.Fatal("a token minted from an expired secret must stop working")
	}

	// A secret cannot be born expired.
	past := epoch.Add(-time.Hour)
	if _, err := principal.IssueSecret("s-past", &past, epoch); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a born-expired secret should be refused, got %v", err)
	}
}

// Every other principal may be left secretless: a maintainer issues a
// replacement for it, so revoking a leaked credential need not wait for its
// successor to exist (Ben, 2026-08-02; ADR-0004 amended).
func TestRevokingTheOnlySecretIsAllowedBelowRoot(t *testing.T) {
	principal, secret := newTestPrincipal(t, Scope{OrganizationID: orgA, ProjectID: projA})

	if err := principal.RevokeSecret("s-1", epoch); err != nil {
		t.Fatalf("a builder's only secret must be revocable: %v", err)
	}
	if len(principal.Secrets()) != 0 {
		t.Fatalf("holds %d secrets, want none", len(principal.Secrets()))
	}
	if _, ok := principal.Authenticate(secret, epoch); ok {
		t.Fatal("the revoked secret still authenticates")
	}
	// Recoverable, which is the whole argument for allowing it: a fresh secret
	// makes the principal usable again.
	if _, err := principal.IssueSecret("s-2", nil, epoch); err != nil {
		t.Fatalf("reissue after revoking the last secret: %v", err)
	}
}

func TestScopePermitsIsDenyByDefault(t *testing.T) {
	for _, c := range []struct {
		name         string
		scope        Scope
		org, project uuid.UUID
		want         bool
	}{
		{"project-scoped, own tenancy", Scope{OrganizationID: orgA, ProjectID: projA}, orgA, projA, true},
		{"project-scoped, sibling project", Scope{OrganizationID: orgA, ProjectID: projA}, orgA, projB, false},
		{"project-scoped, other organization", Scope{OrganizationID: orgA, ProjectID: projA}, orgB, projA, false},
		{"bucket-scoped, own project", Scope{OrganizationID: orgA, ProjectID: projA, BucketID: "01ARZ3NDEKTSV4RRFFQ69G5FAV"}, orgA, projA, true},
		{"bucket-scoped, sibling project", Scope{OrganizationID: orgA, ProjectID: projA, BucketID: "01ARZ3NDEKTSV4RRFFQ69G5FAV"}, orgA, projB, false},
		{"org-scoped, any project in its org", Scope{OrganizationID: orgA}, orgA, projB, true},
		{"org-scoped, other organization", Scope{OrganizationID: orgA}, orgB, projA, false},
		{"zero scope permits nothing", Scope{}, orgA, projA, false},
		{"zero organization requested", Scope{OrganizationID: orgA, ProjectID: projA}, uuid.Nil, projA, false},
		{"zero project requested", Scope{OrganizationID: orgA, ProjectID: projA}, orgA, uuid.Nil, false},
		{"org-scoped with zero project requested", Scope{OrganizationID: orgA}, orgA, uuid.Nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.scope.Permits(c.org, c.project); got != c.want {
				t.Fatalf("Permits(%v, %v) = %v, want %v", c.org, c.project, got, c.want)
			}
		})
	}
}

func TestRestorePrincipalRejectsInconsistentState(t *testing.T) {
	if _, err := RestorePrincipal("p-1", "ci", "client-1", Scope{}, RoleBuilder, epoch, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a stored principal without an organization must be rejected: %v", err)
	}

	three := []Secret{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	if _, err := RestorePrincipal("p-1", "ci", "client-1", Scope{OrganizationID: orgA}, RoleBuilder, epoch, three); !errors.Is(err, ErrInvalid) {
		t.Fatalf("more secrets than the cap must be rejected: %v", err)
	}
}

func TestRestoreSecretRejectsNonArgon2id(t *testing.T) {
	for _, c := range []struct{ name, id, encoded string }{
		{"empty id", "", "$argon2id$v=19$m=1,t=1,p=1$c2FsdA$aGFzaA"},
		{"bcrypt", "s-1", "$2a$10$abcdefghijklmnopqrstuv"},
		{"plaintext", "s-1", "hunter2"},
		{"empty", "s-1", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := RestoreSecret(c.id, c.encoded, epoch, nil, nil); !errors.Is(err, ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
		})
	}
}

// A hash written with DIFFERENT cost parameters must still verify, or tuning
// them locks every existing principal out.
//
// The previous version of this test hashed with the current parameters and
// verified with the current parameters — it would have passed whether or not
// verification read anything back from the hash, and it did pass while
// verification ignored them entirely (review finding 11). This one writes a hash
// at deliberately different settings.
func TestVerificationUsesTheParametersTheHashCarries(t *testing.T) {
	const plaintext = "the-secret"

	// Deliberately unlike the package constants: less memory, more iterations.
	weaker := encodeArgon2id(t, plaintext, 8*1024, 3, 1)
	if strings.HasPrefix(weaker, fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$", argon2.Version, argonMemory, argonTime, argonThreads)) {
		t.Fatal("fixture assumption broken: the test hash uses the package parameters")
	}

	secret, err := RestoreSecret("s-1", weaker, epoch, nil, nil)
	if err != nil {
		t.Fatalf("RestoreSecret: %v", err)
	}
	if !secret.matches(plaintext) {
		t.Fatal("a hash written with different parameters does not verify — tuning them would lock everyone out")
	}
	if secret.matches("not-the-secret") {
		t.Fatal("the wrong plaintext verified")
	}
}

// A hash claiming zero cost would make argon2 panic, so it is refused as
// malformed rather than treated as cheap.
func TestRestoreSecretRefusesDegenerateParameters(t *testing.T) {
	for _, encoded := range []string{
		"$argon2id$v=19$m=0,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=8,t=0,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=8,t=1,p=0$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=8,t=1,p=1$$aGFzaA",
	} {
		secret, err := RestoreSecret("s-1", encoded, epoch, nil, nil)
		if err != nil {
			continue // refused on restore, which is also correct
		}
		if secret.matches("anything") {
			t.Fatalf("a degenerate hash verified: %s", encoded)
		}
	}
}

// encodeArgon2id writes a PHC string at the given cost, so a test can produce a
// hash that does not match the package constants.
func encodeArgon2id(t *testing.T, plaintext string, memory, iterations uint32, threads uint8) string {
	t.Helper()
	salt := []byte("0123456789abcdef")
	key := argon2.IDKey([]byte(plaintext), salt, iterations, memory, threads, argonKeyLen)
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memory, iterations, threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	)
}

// Platform scope is reachable only paired with root.
//
// duf-ls6: Scope{} is platform-scoped, and the store answers a platform scope
// with every organisation and every principal on the instance — so the ZERO
// VALUE is the most privileged one, which inverts deny by default. Nothing in
// the type system prevents a future caller constructing one by forgetting to
// populate a scope, the same shape as review finding 10 a layer down.
//
// What makes that safe is this coupling and nothing else: a scope only becomes
// privileged by arriving on a principal, and a principal cannot hold platform
// scope without root. This pins it, so the safety argument stops being a
// comment someone can invalidate.
func TestPlatformScopeIsReachableOnlyWithRoot(t *testing.T) {
	platform := Scope{}
	if !platform.PlatformScoped() {
		t.Fatal("the zero scope is no longer platform-scoped; the hazard this pins has changed shape")
	}

	for _, role := range []Role{RoleReader, RoleBuilder, RolePublisher, RoleMaintainer} {
		t.Run("constructing "+string(role)+" at platform scope", func(t *testing.T) {
			if _, err := NewPrincipal(
				"p-1", "name", "client", platform, role, time.Unix(0, 0).UTC(),
			); !errors.Is(err, ErrInvalid) {
				t.Fatalf("NewPrincipal(%s, platform) = %v, want ErrInvalid", role, err)
			}
			if _, err := RestorePrincipal(
				"p-1", "name", "client", platform, role, time.Unix(0, 0).UTC(), nil,
			); !errors.Is(err, ErrInvalid) {
				t.Fatalf("RestorePrincipal(%s, platform) = %v, want ErrInvalid", role, err)
			}
		})
	}

	// And root is refused ANY tenancy, so the pairing holds in both directions —
	// a non-root principal can never carry a privileged scope, and root can
	// never carry a narrow one that would let it act as a tenant.
	tenancy := Scope{OrganizationID: uuid.New()}
	if _, err := NewPrincipal(
		"p-1", "name", "client", tenancy, RoleRoot, time.Unix(0, 0).UTC(),
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("NewPrincipal(root, tenancy) = %v, want ErrInvalid", err)
	}

	// Root at platform scope is the one permitted combination.
	if _, err := NewPrincipal(
		"p-1", "name", "client", platform, RoleRoot, time.Unix(0, 0).UTC(),
	); err != nil {
		t.Fatalf("root at platform scope must be constructible: %v", err)
	}
}

// The side door the review found (duf-2rw): a root's FIRST secret arriving
// with an expiry creates a root nothing permanent stands behind — delete the
// other root and the instance locks out on a timer. Issuance refuses it.
func TestRootsFirstSecretCannotExpire(t *testing.T) {
	principal, err := NewPrincipal("p-root", "admin", "client-root", Scope{}, RoleRoot, epoch)
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	expiry := epoch.Add(time.Hour)
	if _, err := principal.IssueSecret("s-exp", &expiry, epoch); !errors.Is(err, ErrRootPermanence) {
		t.Fatalf("an expiring first root secret must be refused with ErrRootPermanence, got %v", err)
	}
	if len(principal.Secrets()) != 0 {
		t.Fatal("the refused secret must not have been issued")
	}
	// A permanent first secret unlocks ordinary expiring rotation.
	if _, err := principal.IssueSecret("s-permanent", nil, epoch); err != nil {
		t.Fatalf("permanent first secret: %v", err)
	}
	if _, err := principal.IssueSecret("s-exp", &expiry, epoch); err != nil {
		t.Fatalf("expiring second secret alongside a permanent one: %v", err)
	}
}

// The expiry that is validated, returned and persisted is one instant: the
// domain truncates to the microseconds Postgres stores before anything reads it.
func TestIssuedExpiryIsMicrosecondTruncated(t *testing.T) {
	principal, _ := newTestPrincipal(t, Scope{OrganizationID: orgA, ProjectID: projA})
	precise := epoch.Add(time.Hour).Add(789 * time.Nanosecond)
	if _, err := principal.IssueSecret("s-precise", &precise, epoch); err != nil {
		t.Fatalf("IssueSecret: %v", err)
	}
	secrets := principal.Secrets()
	issued := secrets[len(secrets)-1]
	if issued.ExpiresAt == nil || !issued.ExpiresAt.Equal(precise.Truncate(time.Microsecond)) {
		t.Fatalf("issued expiry = %v; want the microsecond truncation of %v", issued.ExpiresAt, precise)
	}
	// A value that is future only below microsecond precision is born expired.
	hairline := epoch.Add(500 * time.Nanosecond)
	if _, err := principal.IssueSecret("s-hairline", &hairline, epoch); !errors.Is(err, ErrInvalid) {
		t.Fatalf("sub-microsecond future expiry must be refused, got %v", err)
	}
}
