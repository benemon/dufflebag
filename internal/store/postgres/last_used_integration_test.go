//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/compat/hcpauth"
	"github.com/benemon/dufflebag/internal/domain/identity"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/google/uuid"
)

func TestTokenMintTouchesExactlyTheMatchedSecret(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	principal, err := identity.NewPrincipal(
		"last-used-principal", "last used principal", "last-used-client",
		identity.Scope{OrganizationID: uuid.MustParse(orgA), ProjectID: uuid.MustParse(projectA)},
		identity.RoleBuilder, time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	if _, err := principal.IssueSecret("unused-secret", nil, time.Now().UTC()); err != nil {
		t.Fatalf("IssueSecret unused: %v", err)
	}
	matchedPlaintext, err := principal.IssueSecret("matched-secret", nil, time.Now().UTC())
	if err != nil {
		t.Fatalf("IssueSecret matched: %v", err)
	}
	if err := repository.CreatePrincipal(ctx, principal); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	issuer, err := identity.NewBasicAuthIssuer(
		"https://dufflebag.test", []byte("0123456789abcdef0123456789abcdef"), 5*time.Minute,
	)
	if err != nil {
		t.Fatalf("NewBasicAuthIssuer: %v", err)
	}
	handler := hcpauth.NewHandler(repository, issuer, slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := func(secret string) *httptest.ResponseRecorder {
		form := url.Values{"grant_type": {"client_credentials"}, "audience": {identity.TokenAudience}}
		r := httptest.NewRequest(http.MethodPost, hcpauth.TokenPath, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.SetBasicAuth(principal.ClientID, secret)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	if response := request(matchedPlaintext); response.Code != http.StatusOK {
		t.Fatalf("successful token status = %d: %s", response.Code, response.Body)
	}
	unusedAt, matchedAt := readLastUsed(t, db)
	if unusedAt.Valid {
		t.Fatalf("unmatched secret last_used_at = %v, want NULL", unusedAt.Time)
	}
	if !matchedAt.Valid {
		t.Fatal("matched secret last_used_at stayed NULL")
	}

	if response := request("wrong-secret"); response.Code != http.StatusUnauthorized {
		t.Fatalf("failed token status = %d: %s", response.Code, response.Body)
	}
	unusedAfterFailure, matchedAfterFailure := readLastUsed(t, db)
	if unusedAfterFailure.Valid {
		t.Fatalf("failed authentication touched unused secret at %v", unusedAfterFailure.Time)
	}
	if !matchedAfterFailure.Valid || !matchedAfterFailure.Time.Equal(matchedAt.Time) {
		t.Fatalf("failed authentication changed matched last_used_at from %v to %v", matchedAt, matchedAfterFailure)
	}
}

func readLastUsed(t *testing.T, db *sql.DB) (unused, matched sql.NullTime) {
	t.Helper()
	if err := db.QueryRow(`SELECT last_used_at FROM principal_secrets WHERE id = 'unused-secret'`).Scan(&unused); err != nil {
		t.Fatalf("read unused last_used_at: %v", err)
	}
	if err := db.QueryRow(`SELECT last_used_at FROM principal_secrets WHERE id = 'matched-secret'`).Scan(&matched); err != nil {
		t.Fatalf("read matched last_used_at: %v", err)
	}
	return unused, matched
}
