package identity

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const TokenAudience = "https://api.hashicorp.cloud"

// TokenIssuer authenticates a credential, issues a token, and verifies tokens
// carrying the resulting principal scope.
type TokenIssuer interface {
	Issue(principal *Principal, credential string) (token string, secretID string, err error)
	Reissue(principal *Principal, secretID string, authTime time.Time) (string, error)
	Verify(token string) (Verified, error)
	VerifyExpired(token string) (Verified, error)
}

// Verified is the authenticated identity a token carries.
//
// The principal is returned alongside the scope because authorizing a request
// and recording who made it are different needs, and a not-found answer at a
// tenancy boundary must carry enough server-side detail to diagnose (ADR-0017).
// Scope alone cannot say which principal acted.
type Verified struct {
	PrincipalID string
	Scope       Scope
	// SecretID names the credential that minted this token, so a caller
	// resolving the principal can refuse a token whose credential has since
	// been revoked (review finding 14).
	SecretID  string
	AuthTime  time.Time
	ExpiresAt time.Time
}

// Grant is a resource-scoped set of actions carried by a token.
type Grant struct {
	Resource string   `json:"resource"`
	Actions  []string `json:"actions"`
}

// Claims is the JWT contract shared by every issuance path.
type Claims struct {
	jwt.RegisteredClaims
	AuthTime *jwt.NumericDate `json:"auth_time,omitempty"`

	OrganizationID string `json:"organization_id,omitempty"`
	ProjectID      string `json:"project_id,omitempty"`
	BucketID       string `json:"bucket_id,omitempty"`
	// SecretID is the credential this token was minted from. Not a secret
	// itself — an opaque identifier — and it is what makes revocation take
	// effect immediately rather than at expiry.
	SecretID string `json:"sid,omitempty"`
	// Scope is a deliberately reserved seam for authorization bindings
	// (duf-vgn). It is emitted as an empty array until bindings exist, and
	// token tests assert that emptiness.
	Scope []string `json:"scope"`
	// Grants is a deliberately reserved seam for authorization bindings
	// (duf-vgn). It is emitted as an empty array until bindings exist, and
	// token tests assert that emptiness.
	Grants []Grant `json:"grants"`
}

// BasicAuthIssuer issues tokens after authenticating a Principal secret.
type BasicAuthIssuer struct {
	issuer string
	keys   func() [][]byte
	ttl    time.Duration
}

var _ TokenIssuer = (*BasicAuthIssuer)(nil)

func NewBasicAuthIssuer(issuer string, signingKey []byte, ttl time.Duration) (*BasicAuthIssuer, error) {
	switch {
	case issuer == "":
		return nil, fmt.Errorf("%w: token issuer is required", ErrInvalid)
	case len(signingKey) == 0:
		return nil, fmt.Errorf("%w: token signing key is required", ErrInvalid)
	case ttl <= 0:
		return nil, fmt.Errorf("%w: token ttl must be positive", ErrInvalid)
	}
	key := append([]byte(nil), signingKey...)
	return &BasicAuthIssuer{issuer: issuer, keys: func() [][]byte {
		return [][]byte{append([]byte(nil), key...)}
	}, ttl: ttl}, nil
}

// NewKeyringAuthIssuer reads the current keyring on every issue and verify so
// rotation takes effect without rebuilding the HTTP planes.
func NewKeyringAuthIssuer(issuer string, keys func() [][]byte, ttl time.Duration) (*BasicAuthIssuer, error) {
	switch {
	case issuer == "":
		return nil, fmt.Errorf("%w: token issuer is required", ErrInvalid)
	case keys == nil || len(keys()) == 0:
		return nil, fmt.Errorf("%w: token signing key is required", ErrInvalid)
	case ttl <= 0:
		return nil, fmt.Errorf("%w: token ttl must be positive", ErrInvalid)
	}
	return &BasicAuthIssuer{issuer: issuer, keys: keys, ttl: ttl}, nil
}

func (i *BasicAuthIssuer) Issue(principal *Principal, credential string) (string, string, error) {
	// A platform-scoped principal has no organization by design (ADR-0019), so
	// the guard is on identity rather than tenancy — requiring an organization
	// here would make the bootstrap credential unable to obtain a token at all.
	if principal == nil || principal.ID == "" {
		return "", "", ErrInvalid
	}
	now := time.Now().UTC()
	secretID, authenticated := principal.Authenticate(credential, now)
	if !authenticated {
		return "", "", ErrInvalid
	}

	signed, err := i.sign(principal, secretID, now, now)
	if err != nil {
		return "", "", err
	}
	return signed, secretID, nil
}

func (i *BasicAuthIssuer) Reissue(principal *Principal, secretID string, authTime time.Time) (string, error) {
	if principal == nil || principal.ID == "" || secretID == "" || authTime.IsZero() {
		return "", ErrInvalid
	}
	return i.sign(principal, secretID, authTime.UTC(), time.Now().UTC())
}

func (i *BasicAuthIssuer) sign(
	principal *Principal, secretID string, authTime, now time.Time,
) (string, error) {
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    i.issuer,
			Subject:   principal.ID,
			Audience:  jwt.ClaimStrings{TokenAudience},
			ExpiresAt: jwt.NewNumericDate(now.Add(i.ttl)),
			IssuedAt:  jwt.NewNumericDate(now),
		},
		AuthTime: jwt.NewNumericDate(authTime),
		Scope:    []string{},
		Grants:   []Grant{},
		SecretID: secretID,
	}
	// Absent tenancy claims mean platform scope. Emitting a zero UUID instead
	// would read as a real organization to anything not special-casing it, the
	// same reason a zero project is never stored (ADR-0016).
	if principal.Scope.OrganizationID != uuid.Nil {
		claims.OrganizationID = principal.Scope.OrganizationID.String()
	}
	if principal.Scope.ProjectID != uuid.Nil {
		claims.ProjectID = principal.Scope.ProjectID.String()
	}
	if principal.Scope.BucketID != "" {
		claims.BucketID = principal.Scope.BucketID
	}

	keys := i.keys()
	if len(keys) == 0 {
		return "", ErrInvalid
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(keys[0])
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return signed, nil
}

func (i *BasicAuthIssuer) Verify(signed string) (Verified, error) {
	return i.verify(signed, false)
}

// VerifyExpired performs the same signature and claim checks as Verify while
// tolerating an expired exp claim. It exists only for the cookie-session
// renewal path; bearer middleware must continue to call Verify.
func (i *BasicAuthIssuer) VerifyExpired(signed string) (Verified, error) {
	return i.verify(signed, true)
}

func (i *BasicAuthIssuer) verify(signed string, allowExpired bool) (Verified, error) {
	var claims Claims
	valid := false
	for _, key := range i.keys() {
		candidate := Claims{}
		token, err := jwt.ParseWithClaims(
			signed,
			&candidate,
			func(token *jwt.Token) (any, error) {
				if token.Method.Alg() != jwt.SigningMethodHS256.Alg() {
					return nil, ErrInvalid
				}
				return key, nil
			},
			jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
			// Claims are validated below rather than by the library, so every check
			// that matters is visible in one place.
			jwt.WithoutClaimsValidation(),
		)
		if err == nil && token.Valid {
			claims = candidate
			valid = true
			break
		}
	}
	if !valid {
		return Verified{}, ErrInvalid
	}

	now := time.Now()
	if claims.Subject == "" ||
		claims.Issuer != i.issuer ||
		claims.IssuedAt == nil ||
		claims.ExpiresAt == nil ||
		claims.AuthTime == nil ||
		(!allowExpired && !now.Before(claims.ExpiresAt.Time)) ||
		len(claims.Audience) != 1 ||
		claims.Audience[0] != TokenAudience {
		return Verified{}, ErrInvalid
	}

	// Absent scope or grants means no authority, not an invalid token. Rejecting
	// their absence would make a token minted before those claims existed fail
	// against a newer server — a rolling upgrade would break every token in
	// flight, for no security gain, since empty already grants nothing.

	// An absent organization is platform scope, not a malformed token. A present
	// one must parse and must not be the zero UUID.
	organizationID := uuid.Nil
	if claims.OrganizationID != "" {
		var err error
		organizationID, err = uuid.Parse(claims.OrganizationID)
		if err != nil || organizationID == uuid.Nil {
			return Verified{}, ErrInvalid
		}
	}
	projectID := uuid.Nil
	if claims.ProjectID != "" {
		var err error
		projectID, err = uuid.Parse(claims.ProjectID)
		if err != nil || projectID == uuid.Nil {
			return Verified{}, ErrInvalid
		}
	}
	// A project without an organization is malformed rather than narrow.
	if organizationID == uuid.Nil && projectID != uuid.Nil {
		return Verified{}, ErrInvalid
	}
	// A bucket without a project is malformed rather than narrow.
	if claims.BucketID != "" && projectID == uuid.Nil {
		return Verified{}, ErrInvalid
	}
	// An absent sid is refused rather than tolerated. A token that cannot say
	// which credential minted it cannot be revoked by revoking that credential,
	// so accepting one would leave exactly the hole this closes — and the only
	// tokens without one are from a build before this change, which conventions
	// rule 3 says to recreate rather than carry (ADR-0017: deny by default).
	if claims.SecretID == "" {
		return Verified{}, ErrInvalid
	}
	return Verified{
		PrincipalID: claims.Subject,
		Scope: Scope{
			OrganizationID: organizationID,
			ProjectID:      projectID,
			BucketID:       claims.BucketID,
		},
		SecretID:  claims.SecretID,
		AuthTime:  claims.AuthTime.Time,
		ExpiresAt: claims.ExpiresAt.Time,
	}, nil
}

// TTL is the lifetime of tokens this issuer mints.
//
// Exposed so the token endpoint can report expires_in from the same value that
// produced the token's exp claim, rather than holding a second copy that could
// drift.
func (i *BasicAuthIssuer) TTL() time.Duration { return i.ttl }
