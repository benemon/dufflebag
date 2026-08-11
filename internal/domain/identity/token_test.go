package identity

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testIssuer = "https://dufflebag.example"

var testSigningKey = []byte("01234567890123456789012345678901")

func newTestIssuer(t *testing.T) *BasicAuthIssuer {
	t.Helper()
	issuer, err := NewBasicAuthIssuer(testIssuer, testSigningKey, 5*time.Minute)
	if err != nil {
		t.Fatalf("NewBasicAuthIssuer: %v", err)
	}
	return issuer
}

func TestIssueAndVerifyReturnsPrincipalScope(t *testing.T) {
	for _, c := range []struct {
		name  string
		scope Scope
	}{
		{"organization scoped", Scope{OrganizationID: orgA}},
		{"project scoped", Scope{OrganizationID: orgA, ProjectID: projA}},
	} {
		t.Run(c.name, func(t *testing.T) {
			principal, secret := newTestPrincipal(t, c.scope)
			issuer := newTestIssuer(t)

			signed, _, err := issuer.Issue(principal, secret)
			if err != nil {
				t.Fatalf("Issue: %v", err)
			}
			got, err := issuer.Verify(signed)
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if got.Scope != c.scope {
				t.Fatalf("Verify scope = %#v, want %#v", got.Scope, c.scope)
			}
			// The principal must survive verification, or an authorized request
			// cannot be attributed to whoever made it.
			if got.PrincipalID != principal.ID {
				t.Fatalf("Verify principal = %q, want %q", got.PrincipalID, principal.ID)
			}
			if got.AuthTime.IsZero() {
				t.Fatal("Issue emitted no auth_time")
			}
		})
	}
}

func TestOrganizationScopedTokenOmitsProjectAndCarriesAuthorizationClaims(t *testing.T) {
	principal, secret := newTestPrincipal(t, Scope{OrganizationID: orgA})
	signed, _, err := newTestIssuer(t).Issue(principal, secret)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	parts := strings.Split(signed, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts, want 3", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if project, ok := claims["project_id"]; ok {
		t.Fatalf("organization-scoped project_id = %v, want absent", project)
	}
	if scope, ok := claims["scope"].([]any); !ok || len(scope) != 0 {
		t.Fatalf("scope claim = %#v, want an empty array", claims["scope"])
	}
	if grants, ok := claims["grants"].([]any); !ok || len(grants) != 0 {
		t.Fatalf("grants claim = %#v, want an empty array", claims["grants"])
	}
}

func TestVerifyFailsClosed(t *testing.T) {
	issuer := newTestIssuer(t)
	validClaims := func() Claims {
		now := time.Now()
		return Claims{
			RegisteredClaims: jwt.RegisteredClaims{
				Issuer:    testIssuer,
				Subject:   "p-1",
				Audience:  jwt.ClaimStrings{TokenAudience},
				ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
				IssuedAt:  jwt.NewNumericDate(now),
			},
			AuthTime:       jwt.NewNumericDate(now),
			OrganizationID: orgA.String(),
			ProjectID:      projA.String(),
			Scope:          []string{},
			Grants:         []Grant{},
			SecretID:       "s-1",
		}
	}
	sign := func(t *testing.T, method jwt.SigningMethod, claims Claims, key any) string {
		t.Helper()
		signed, err := jwt.NewWithClaims(method, claims).SignedString(key)
		if err != nil {
			t.Fatalf("SignedString: %v", err)
		}
		return signed
	}
	assertInvalid := func(t *testing.T, signed string) {
		t.Helper()
		verified, err := issuer.Verify(signed)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("Verify error = %v, want ErrInvalid", err)
		}
		if verified != (Verified{}) {
			t.Fatalf("Verify = %#v, want zero Verified", verified)
		}
	}

	t.Run("expired token", func(t *testing.T) {
		claims := validClaims()
		claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
		assertInvalid(t, sign(t, jwt.SigningMethodHS256, claims, testSigningKey))
	})

	t.Run("tampered signature", func(t *testing.T) {
		principal, secret := newTestPrincipal(t, Scope{OrganizationID: orgA, ProjectID: projA})
		signed, _, err := issuer.Issue(principal, secret)
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		parts := strings.Split(signed, ".")
		if parts[2][0] == 'a' {
			parts[2] = "b" + parts[2][1:]
		} else {
			parts[2] = "a" + parts[2][1:]
		}
		assertInvalid(t, strings.Join(parts, "."))
	})

	t.Run("wrong audience", func(t *testing.T) {
		claims := validClaims()
		claims.Audience = jwt.ClaimStrings{"https://example.com"}
		assertInvalid(t, sign(t, jwt.SigningMethodHS256, claims, testSigningKey))
	})

	t.Run("unexpected algorithm", func(t *testing.T) {
		assertInvalid(t, sign(t, jwt.SigningMethodHS512, validClaims(), testSigningKey))
	})

	t.Run("none algorithm", func(t *testing.T) {
		assertInvalid(t, sign(t, jwt.SigningMethodNone, validClaims(), jwt.UnsafeAllowNoneSignatureType))
	})

	t.Run("missing required claim", func(t *testing.T) {
		for _, c := range []struct {
			name   string
			remove func(*Claims)
		}{
			{"subject", func(c *Claims) { c.Subject = "" }},
			{"audience", func(c *Claims) { c.Audience = nil }},
			{"expiration", func(c *Claims) { c.ExpiresAt = nil }},
			{"issued at", func(c *Claims) { c.IssuedAt = nil }},
			{"issuer", func(c *Claims) { c.Issuer = "" }},
			{"organization", func(c *Claims) { c.OrganizationID = "" }},
			// scope and grants are deliberately NOT here. Every claim is covered
			// by the signature, so their absence cannot be induced by an attacker
			// without the signing key — requiring them buys no security and costs
			// a broken rolling upgrade. See
			// TestVerifyAcceptsATokenWithoutAuthorizationClaims.
		} {
			t.Run(c.name, func(t *testing.T) {
				claims := validClaims()
				c.remove(&claims)
				assertInvalid(t, sign(t, jwt.SigningMethodHS256, claims, testSigningKey))
			})
		}
	})

	t.Run("malformed token", func(t *testing.T) {
		assertInvalid(t, "not-a-jwt")
	})
}

func TestVerifiedScopeRejectsAnotherTenant(t *testing.T) {
	principal, secret := newTestPrincipal(t, Scope{OrganizationID: orgA, ProjectID: projA})
	issuer := newTestIssuer(t)
	signed, _, err := issuer.Issue(principal, secret)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	verified, err := issuer.Verify(signed)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if verified.Scope.Permits(orgB, projB) {
		t.Fatal("token issued for one tenant permits another tenant")
	}
}

// A token carrying no scope or grants claims must still verify, granting
// nothing. Rejecting it would make every token minted by an older instance fail
// against a newer one, breaking a rolling upgrade for no security gain — empty
// authority and absent authority are the same authority.
func TestVerifyAcceptsATokenWithoutAuthorizationClaims(t *testing.T) {
	issuer := newTestIssuer(t)
	now := time.Now()

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss":             testIssuer,
		"sub":             "p-1",
		"aud":             []string{TokenAudience},
		"iat":             now.Unix(),
		"auth_time":       now.Unix(),
		"exp":             now.Add(time.Minute).Unix(),
		"organization_id": orgA.String(),
		"sid":             "s-1",
		// scope and grants deliberately absent.
	}).SignedString(testSigningKey)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	verified, err := issuer.Verify(signed)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if verified.PrincipalID != "p-1" {
		t.Fatalf("principal = %q, want p-1", verified.PrincipalID)
	}
	if !verified.Scope.Permits(orgA, projA) {
		t.Fatal("an organization-scoped token must still permit its own organization")
	}
	if verified.Scope.Permits(orgB, projA) {
		t.Fatal("absent claims must not widen scope")
	}
}

func TestReissuePreservesAuthenticationTimeWithoutCredential(t *testing.T) {
	issuer := newTestIssuer(t)
	principal, plaintext := newTestPrincipal(t, Scope{OrganizationID: orgA})
	_, secretID, err := issuer.Issue(principal, plaintext)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	authTime := time.Now().UTC().Add(-4 * time.Hour).Truncate(time.Second)
	reissued, err := issuer.Reissue(principal, secretID, authTime)
	if err != nil {
		t.Fatalf("Reissue without plaintext credential: %v", err)
	}
	verified, err := issuer.Verify(reissued)
	if err != nil {
		t.Fatalf("Verify reissued token: %v", err)
	}
	if !verified.AuthTime.Equal(authTime) {
		t.Fatalf("auth_time = %s, want preserved %s", verified.AuthTime, authTime)
	}
	if verified.SecretID != secretID {
		t.Fatalf("sid = %q, want %q", verified.SecretID, secretID)
	}
	if _, err := issuer.Reissue(principal, secretID, time.Time{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Reissue without auth_time = %v, want ErrInvalid", err)
	}
}

func TestVerifyExpiredAcceptsOnlyValidlySignedCredentialTokens(t *testing.T) {
	issuer := newTestIssuer(t)
	now := time.Now().UTC().Truncate(time.Second)
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: testIssuer, Subject: "p-1",
			Audience:  jwt.ClaimStrings{TokenAudience},
			IssuedAt:  jwt.NewNumericDate(now.Add(-10 * time.Minute)),
			ExpiresAt: jwt.NewNumericDate(now.Add(-5 * time.Minute)),
		},
		AuthTime: jwt.NewNumericDate(now.Add(-time.Hour)),
		SecretID: "s-1", Scope: []string{}, Grants: []Grant{},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(testSigningKey)
	if err != nil {
		t.Fatalf("sign expired token: %v", err)
	}
	if _, err := issuer.VerifyExpired(signed); err != nil {
		t.Fatalf("VerifyExpired: %v", err)
	}
	if _, err := issuer.Verify(signed); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ordinary Verify of expired token = %v, want ErrInvalid", err)
	}
	parts := strings.Split(signed, ".")
	parts[2] = "invalid-signature"
	if _, err := issuer.VerifyExpired(strings.Join(parts, ".")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("VerifyExpired bad signature = %v, want ErrInvalid", err)
	}
	claims.SecretID = ""
	missingSID, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(testSigningKey)
	if err != nil {
		t.Fatalf("sign missing-sid token: %v", err)
	}
	if _, err := issuer.VerifyExpired(missingSID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("VerifyExpired missing sid = %v, want ErrInvalid", err)
	}
}

// A token that cannot say which credential minted it is refused.
//
// Review finding 14: revoking a secret left every token minted from it working
// until expiry. The fix is for the token to name its credential so verification
// can check it still exists — which only works if a token WITHOUT that name is
// refused rather than waved through. Tolerating one would leave the hole open
// for exactly the tokens an attacker can mint by replaying an older build's.
//
// Conventions rule 3 applies: zero users, so a token from a previous build is
// recreated rather than carried.
func TestVerifyRefusesATokenThatNamesNoCredential(t *testing.T) {
	issuer := newTestIssuer(t)
	now := time.Now()

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss":             testIssuer,
		"sub":             "p-1",
		"aud":             []string{TokenAudience},
		"iat":             now.Unix(),
		"exp":             now.Add(time.Minute).Unix(),
		"organization_id": orgA.String(),
		// sid deliberately absent.
	}).SignedString(testSigningKey)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	if _, err := issuer.Verify(signed); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Verify of a token naming no credential = %v, want ErrInvalid", err)
	}
}

// And a token DOES name the credential it was minted from, or the check above
// has nothing to check.
func TestIssueNamesTheCredentialItAuthenticated(t *testing.T) {
	issuer := newTestIssuer(t)
	principal, plaintext := newTestPrincipal(t, Scope{OrganizationID: orgA, ProjectID: projA})

	signed, secretID, err := issuer.Issue(principal, plaintext)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if secretID == "" {
		t.Fatal("Issue returned no matched secret id")
	}
	verified, err := issuer.Verify(signed)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if verified.SecretID == "" {
		t.Fatal("the token names no credential, so revoking one cannot revoke it")
	}
	if !principal.HasActiveSecret(verified.SecretID, time.Now().UTC()) {
		t.Fatalf("the token names %q, which the principal does not hold", verified.SecretID)
	}

	// Revoking a DIFFERENT secret must not invalidate this token, or rotation
	// would break every token on every rotation.
	if _, err := principal.IssueSecret("s-other", nil, time.Now()); err != nil {
		t.Fatalf("IssueSecret: %v", err)
	}
	if err := principal.RevokeSecret("s-other", epoch); err != nil {
		t.Fatalf("RevokeSecret: %v", err)
	}
	if !principal.HasActiveSecret(verified.SecretID, time.Now().UTC()) {
		t.Fatal("revoking another secret invalidated this token")
	}

	// Revoking THIS one does. A replacement is issued first here so the
	// principal stays usable across the assertion, not because it is required:
	// since the 2026-08-02 amendment to ADR-0004 a non-root principal may be
	// left with no secrets, so a compromised sole credential can be revoked
	// outright and replaced afterwards.
	if _, err := principal.IssueSecret("s-replacement", nil, time.Now()); err != nil {
		t.Fatalf("IssueSecret: %v", err)
	}
	if err := principal.RevokeSecret(verified.SecretID, time.Now().UTC()); err != nil {
		t.Fatalf("RevokeSecret: %v", err)
	}
	if principal.HasActiveSecret(verified.SecretID, time.Now().UTC()) {
		t.Fatal("the token's credential survived its own revocation")
	}
}

func TestKeyringIssuerSignsNewestAndVerifiesAllCurrentKeys(t *testing.T) {
	oldKey := []byte("old-key-012345678901234567890123")
	newKey := []byte("new-key-012345678901234567890123")
	keys := [][]byte{oldKey}
	issuer, err := NewKeyringAuthIssuer(testIssuer, func() [][]byte { return keys }, 5*time.Minute)
	if err != nil {
		t.Fatalf("NewKeyringAuthIssuer: %v", err)
	}
	principal, secret := newTestPrincipal(t, Scope{OrganizationID: orgA})
	oldToken, _, err := issuer.Issue(principal, secret)
	if err != nil {
		t.Fatalf("issue old token: %v", err)
	}
	keys = [][]byte{newKey, oldKey}
	if _, err := issuer.Verify(oldToken); err != nil {
		t.Fatalf("old token after rotation: %v", err)
	}
	newToken, _, err := issuer.Issue(principal, secret)
	if err != nil {
		t.Fatalf("issue new token: %v", err)
	}
	if _, err := issuer.Verify(newToken); err != nil {
		t.Fatalf("new token after rotation: %v", err)
	}
	if _, err := issuer.Verify("garbage"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("garbage token error = %v, want ErrInvalid", err)
	}
}
