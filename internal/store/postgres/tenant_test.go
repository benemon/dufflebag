package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/benemon/dufflebag/internal/domain/registry"
)

func TestBeginTenantRequiresBothIDs(t *testing.T) {
	for _, tc := range []struct {
		name, organizationID, projectID string
	}{
		{"missing organization", "", "project"},
		{"missing project", "organization", ""},
		{"missing both", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := BeginTenant(context.Background(), nil, tc.organizationID, tc.projectID)
			if err == nil {
				_ = tx.Rollback()
				t.Fatal("BeginTenant accepted an incomplete tenant")
			}
		})
	}
}

// ADR-0017's backstop: a handler that never passed authorization holds a denied
// tenant, and no repository operation may resolve one. Every operation funnels
// through begin, so the refusal is asserted there — and against a nil db, so
// the tenant is proven to be refused BEFORE storage is touched. Were either
// flag ignored, begin would try to open a transaction and fail loudly rather
// than let the test pass against a coincidentally empty database.
func TestBeginRefusesADeniedOrMalformedTenant(t *testing.T) {
	repository := NewRepository(nil)
	for _, tc := range []struct {
		name   string
		tenant Tenant
	}{
		{"denied", DeniedTenant()},
		{"malformed", ParseTenant("not-a-uuid", "not-a-uuid-either")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := repository.begin(context.Background(), tc.tenant)
			if !errors.Is(err, registry.ErrNotFound) {
				t.Fatalf("begin = %v, want ErrNotFound", err)
			}
		})
	}
}
