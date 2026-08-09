package hcp2023

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/google/uuid"
)

// testToken stands in for a real bearer token. Verification is faked so these
// tests stay about the handlers; the token format itself is covered by the
// identity package, and the authorization decision by TestTenantAuthorization.
const testToken = "test-token"

type testAuthenticator struct{}

func (testAuthenticator) Verify(token string) (identity.Verified, error) {
	if token != testToken {
		return identity.Verified{}, identity.ErrInvalid
	}
	return identity.Verified{
		PrincipalID: "p-test",
		Scope: identity.Scope{
			OrganizationID: uuid.MustParse(testOrg),
			ProjectID:      uuid.MustParse(testProject),
		},
		SecretID: testSecretID,
	}, nil
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakePrincipals resolves the authority of an already-authenticated caller.
type fakePrincipals struct {
	role        identity.Role
	scope       identity.Scope
	missing     bool
	belowReader bool
}

func (f fakePrincipals) GetPrincipalByID(_ context.Context, id string) (*identity.Principal, error) {
	if f.missing {
		return nil, identity.ErrNotFound
	}
	role := f.role
	if role == "" {
		role = identity.RoleBuilder
	}
	scope := f.scope
	if scope == (identity.Scope{}) && role != identity.RoleRoot {
		scope = identity.Scope{
			OrganizationID: uuid.MustParse(testOrg),
			ProjectID:      uuid.MustParse(testProject),
		}
	}
	principal, err := identity.RestorePrincipal(id, "test", "client", scope, role, testTime, testSecrets())
	if err != nil {
		return nil, err
	}
	// There is deliberately no persisted role below reader. This test-only
	// corruption proves the middleware still denies a principal whose resolved
	// authority is below the minimum read role.
	if f.belowReader {
		principal.Role = ""
	}
	return principal, nil
}

// The default for handler tests, which exercise the whole surface including
// promotion. Deliberately NOT builder: these tests are about what the handlers
// do, and giving them the lowest sufficient role for each route would make every
// one of them a role test by accident. The role boundaries have their own tests.
func testPrincipals() fakePrincipals { return fakePrincipals{role: identity.RolePublisher} }

// testSecretID is the credential every test token is minted from. Fixtures must
// carry a secret with this ID or the request is refused as revoked, which is the
// point of review finding 14 — a token names the credential behind it.
const testSecretID = "s-test"

// testSecrets is the stored credential a fixture principal holds. Only the
// argon2id prefix is validated on restore, so this needs no derivation.
func testSecrets() []identity.Secret {
	secret, err := identity.RestoreSecret(
		testSecretID,
		"$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHQ$aGFzaGhhc2g",
		time.Unix(0, 0).UTC(), nil, nil,
	)
	if err != nil {
		panic("test secret: " + err.Error())
	}
	return []identity.Secret{secret}
}
