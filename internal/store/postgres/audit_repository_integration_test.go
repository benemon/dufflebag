//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
	platform "github.com/benemon/dufflebag/internal/platform/v1"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/google/uuid"
)

type auditAPIAuthenticator struct {
	principalID string
	secretID    string
}

func (a auditAPIAuthenticator) Verify(string) (identity.Verified, error) {
	return identity.Verified{PrincipalID: a.principalID, SecretID: a.secretID}, nil
}

func (a auditAPIAuthenticator) VerifyExpired(string) (identity.Verified, error) {
	return identity.Verified{}, identity.ErrInvalid
}

func (a auditAPIAuthenticator) Reissue(*identity.Principal, string, time.Time) (string, error) {
	return "", identity.ErrInvalid
}

type auditAPIPrincipals struct{ principal *identity.Principal }

func (p auditAPIPrincipals) GetPrincipalByID(context.Context, string) (*identity.Principal, error) {
	return p.principal, nil
}

func (p auditAPIPrincipals) TouchSecretLastUsed(context.Context, string, time.Time) error {
	return nil
}

func TestAuditAttributionSurvivesPrincipalDeletion(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	at := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	actor, err := identity.NewPrincipal(
		"root-audited", "Audited Operator", "root-audited-client",
		identity.Scope{}, identity.RoleRoot, at,
	)
	if err != nil {
		t.Fatalf("new audited root: %v", err)
	}
	secretID := "root-audited-secret"
	if _, err := actor.IssueSecret(secretID, nil, at); err != nil {
		t.Fatalf("issue audited root secret: %v", err)
	}
	remainingRoot, err := identity.NewPrincipal(
		"root-remaining", "Remaining Operator", "root-remaining-client",
		identity.Scope{}, identity.RoleRoot, at.Add(time.Second),
	)
	if err != nil {
		t.Fatalf("new remaining root: %v", err)
	}
	for _, principal := range []*identity.Principal{actor, remainingRoot} {
		if err := repository.CreatePrincipal(ctx, principal); err != nil {
			t.Fatalf("create principal %s: %v", principal.ID, err)
		}
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	broker, err := audit.NewBroker(logger)
	if err != nil {
		t.Fatalf("new audit broker: %v", err)
	}
	platformHandler := platform.NewHandler(
		repository, repository,
		auditAPIAuthenticator{principalID: actor.ID, secretID: secretID},
		repository, logger, repository, broker, nil, nil, nil, nil, platform.BuildInfo{},
	)
	resolver, ok := platformHandler.(audit.Resolver)
	if !ok {
		t.Fatalf("platform handler %T has no audit resolver", platformHandler)
	}
	path := filepath.Join(t.TempDir(), "audit.log")
	sink, err := audit.NewFileSink(path, logger)
	if err != nil {
		t.Fatalf("open audit sink: %v", err)
	}
	handler := audit.NewHTTPHandler(
		sink, resolver, platformHandler, audit.StaticHMACKey("test-v1", []byte("test-audit-hmac-key")),
	)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/organizations", nil)
	request.Header.Set("Authorization", "Bearer integration-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("audited request = %d, want 200; body %s", response.Code, response.Body)
	}

	if err := repository.DeletePrincipal(ctx, actor.ID); err != nil {
		t.Fatalf("delete audited principal: %v", err)
	}
	if _, err := repository.GetPrincipalByID(ctx, actor.ID); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("deleted principal lookup = %v, want ErrNotFound", err)
	}
	if err := sink.Close(ctx); err != nil {
		t.Fatalf("close audit sink: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit sink after principal deletion: %v", err)
	}
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("decode audit record: %v", err)
		}
		if record["kind"] == "response" && record["operation"] == "organization.list" {
			if got := record["principal_name"]; got != actor.Name {
				t.Fatalf("principal_name after principal deletion = %v, want %q", got, actor.Name)
			}
			return
		}
	}
	t.Fatalf("organization.list response missing after principal deletion: %s", raw)
}

func TestAuditTargetAPIAllocatesLowestFreeSlotsAndReturnsConflictAtThree(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	at := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	if _, err := db.ExecContext(ctx,
		`INSERT INTO audit_targets (id, slot, path, created_at) VALUES ($1, 1, $2, $3)`,
		uuid.NewString(), filepath.Join(t.TempDir(), "occupied.log"), at,
	); err != nil {
		t.Fatalf("seed occupied slot 1: %v", err)
	}

	principal, err := identity.NewPrincipal("root", "root", "root-client", identity.Scope{}, identity.RoleRoot, at)
	if err != nil {
		t.Fatalf("new root: %v", err)
	}
	secretID := "root-secret"
	if _, err := principal.IssueSecret(secretID, nil, at); err != nil {
		t.Fatalf("issue root secret: %v", err)
	}
	broker, err := audit.NewBroker(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new audit broker: %v", err)
	}
	handler := platform.NewHandler(
		repository, repository,
		auditAPIAuthenticator{principalID: principal.ID, secretID: secretID},
		auditAPIPrincipals{principal: principal},
		slog.New(slog.NewTextHandler(io.Discard, nil)), repository, broker, nil, nil, nil, nil, platform.BuildInfo{},
	)

	createdIDs := make([]string, 0, 2)
	for i, wantSlot := range []int{2, 3} {
		path := filepath.Join(t.TempDir(), "audit.log")
		body, _ := json.Marshal(map[string]string{"path": path})
		request := httptest.NewRequest(http.MethodPost, "/api/v1/audit/targets", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer integration-token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusCreated {
			t.Fatalf("API create %d = %d, want 201; body %s", i+1, response.Code, response.Body)
		}
		var target platform.AuditTarget
		if err := json.Unmarshal(response.Body.Bytes(), &target); err != nil {
			t.Fatalf("decode API create %d: %v", i+1, err)
		}
		createdIDs = append(createdIDs, target.Id.String())
		var slot int
		if err := db.QueryRowContext(ctx, `SELECT slot FROM audit_targets WHERE id = $1`, target.Id).Scan(&slot); err != nil {
			t.Fatalf("read API-created slot: %v", err)
		}
		if slot != wantSlot {
			t.Fatalf("API create %d took slot %d, want lowest free slot %d", i+1, slot, wantSlot)
		}
	}

	body, _ := json.Marshal(map[string]string{"path": filepath.Join(t.TempDir(), "fourth.log")})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/audit/targets", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer integration-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("fourth target through generated API = %d, want 409; body %s", response.Code, response.Body)
	}
	for _, id := range createdIDs {
		if err := broker.Remove(id); err != nil {
			t.Fatalf("close API target %s: %v", id, err)
		}
	}
}

// The three-target limit has to be a property of the schema, not a count in Go,
// or it is only enforced on the path someone remembered to guard. This asserts
// the limit holds against a DIRECT insert that bypasses the repository
// entirely — the mutation being killed is "enforce max-3 in application code".
func TestFourthAuditTargetIsUnwriteable(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	at := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

	// FIRST, while the table is EMPTY. An earlier version put these after the
	// three creates below, where slot 3 was already taken — so both inserts
	// failed on slot uniqueness whether or not the NOT NULL they targeted was
	// present, and neither proved anything. A NULL check has to be the only
	// reason its insert can fail.
	for _, missing := range []struct {
		column string
		insert string
	}{
		{"path", `INSERT INTO audit_targets (id, slot, path, created_at) VALUES ($1, 3, NULL, $2)`},
		{"created_at", `INSERT INTO audit_targets (id, slot, path, created_at) VALUES ($1, 3, '/tmp/x.log', NULL)`},
	} {
		args := []any{uuid.NewString()}
		if missing.column == "path" {
			args = append(args, at)
		}
		if _, err := db.ExecContext(ctx, missing.insert, args...); err == nil {
			t.Fatalf("direct insert with a NULL %s succeeded", missing.column)
		}
	}

	for i := range 3 {
		if _, err := repository.CreateAuditTarget(
			ctx, uuid.NewString(), "/var/log/dufflebag/audit.log", at,
		); err != nil {
			t.Fatalf("create audit target %d: %v", i+1, err)
		}
	}

	// Through the repository: the defined full answer, not a driver error.
	if _, err := repository.CreateAuditTarget(
		ctx, uuid.NewString(), "/var/log/dufflebag/fourth.log", at,
	); !errors.Is(err, registry.ErrConflict) {
		t.Fatalf("fourth target through the repository: want ErrConflict, got %v", err)
	}

	// NULL first, because NOT NULL is the constraint it is easiest to think
	// redundant and is in fact load-bearing: a NULL slot makes CHECK evaluate
	// to unknown, which Postgres accepts, and ordinary NULLs do not collide
	// under UNIQUE — so dropping NOT NULL alone would allow unlimited targets
	// while both visible constraints remained in place.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO audit_targets (id, slot, path, created_at) VALUES ($1, NULL, $2, $3)`,
		uuid.NewString(), "/var/log/dufflebag/null-slot.log", at,
	); err == nil {
		t.Fatal("direct insert with a NULL slot succeeded: NOT NULL is load-bearing and unenforced")
	}

	// Bypassing it entirely. Every slot value is refused: 1..3 collide on
	// UNIQUE, anything else fails the CHECK. If this insert ever succeeds, the
	// limit lives in Go and the repository is the only thing enforcing it.
	for _, slot := range []int{1, 2, 3, 4, 0, -1} {
		_, err := db.ExecContext(ctx,
			`INSERT INTO audit_targets (id, slot, path, created_at) VALUES ($1, $2, $3, $4)`,
			uuid.NewString(), slot, "/var/log/dufflebag/direct.log", at,
		)
		if err == nil {
			t.Fatalf("direct insert at slot %d succeeded: max-3 is not structural", slot)
		}
	}
}

// Slots are storage detail, but the ALLOCATION has to be right or a delete
// leaves a hole no create can fill. Occupying slot 1 and requiring the next
// create to take 2 — while 3 is also free — kills "take the highest free
// slot", which passes every sequential test and strands a free slot forever.
func TestAuditTargetTakesLowestFreeSlot(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	at := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

	// The fixture is written with DIRECT SQL, not through the allocator, and
	// this is the whole point of the test. Two earlier versions failed to kill
	// a highest-free-slot mutation: the first left only one slot free, where
	// lowest and highest are the same answer; the second built its fixture with
	// the allocator itself, so a mirrored allocator laid the fixture out
	// mirrored too and the assertion still held. A test whose setup depends on
	// the behaviour under test cannot observe that behaviour changing.
	//
	// Occupying slot 1 leaves 2 and 3 free, so lowest-free (2) and highest-free
	// (3) are different answers no matter what the allocator does.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO audit_targets (id, slot, path, created_at) VALUES ($1, 1, $2, $3)`,
		uuid.NewString(), "/tmp/occupied.log", at,
	); err != nil {
		t.Fatalf("seed slot 1: %v", err)
	}

	target, err := repository.CreateAuditTarget(ctx, uuid.NewString(), "/tmp/refill.log", at)
	if err != nil {
		t.Fatalf("create into a free slot: %v", err)
	}
	var slot int
	if err := db.QueryRowContext(ctx,
		`SELECT slot FROM audit_targets WHERE id = $1`, target.ID,
	).Scan(&slot); err != nil {
		t.Fatalf("read slot: %v", err)
	}
	if slot != 2 {
		t.Fatalf("refill took slot %d, want the lowest free slot 2", slot)
	}
}

// The advisory lock is what stops two creates reading the same free slot, so
// removing it must fail a test. An earlier version launched three goroutines
// and asserted they got distinct slots — which proved nothing twice over: the
// scheduler may serialize them anyway, and UNIQUE makes committed duplicate
// slots impossible regardless of the lock.
//
// This holds the lock on a separate connection instead, so the outcome is
// deterministic. With the lock taken, a create MUST block; it is then given a
// deadline it cannot meet, and the timeout IS the proof. Remove
// LockAuditTargetSlots from the repository and the create sails past the held
// lock and succeeds, failing this test.
//
// The holder takes the SHARED lock deliberately, though production takes the
// exclusive one. Shared blocks exclusive, so the intended behaviour is
// unchanged — but shared does NOT block shared, so downgrading production to
// pg_advisory_xact_lock_shared makes the create sail through and fail here.
// With an exclusive holder that downgrade still blocked and the test passed
// while two real creates could have selected the same free slot concurrently.
func TestAuditTargetCreateWaitsForTheSlotLock(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	at := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

	holder, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("dedicated connection: %v", err)
	}
	defer func() { _ = holder.Close() }()
	holderTx, err := holder.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin holder: %v", err)
	}
	// Rolled back on every path. Without this the t.Fatal below leaves the
	// holder transaction open, and tearing the connection down behind it hangs
	// the run until the test binary panics — a ten-minute failure instead of a
	// legible one.
	defer func() { _ = holderTx.Rollback() }()
	if _, err := holderTx.ExecContext(ctx, `SELECT pg_advisory_xact_lock_shared(1646664800)`); err != nil {
		t.Fatalf("take slot lock: %v", err)
	}

	blocked, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err = repository.CreateAuditTarget(blocked, uuid.NewString(), "/tmp/blocked.log", at)
	if err == nil {
		t.Fatal("create succeeded while the slot lock was held: selection is not serialized")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("create failed for the wrong reason: %v", err)
	}

	// Releasing the lock must let the same create through, or the test would
	// pass for any reason a create fails.
	if err := holderTx.Rollback(); err != nil {
		t.Fatalf("release slot lock: %v", err)
	}

	if _, err := repository.CreateAuditTarget(ctx, uuid.NewString(), "/tmp/after.log", at); err != nil {
		t.Fatalf("create after the lock was released: %v", err)
	}
}

// Listing is the surface an operator reads a failing target from, so newest
// first and its field mapping are load-bearing rather than incidental.
func TestListAuditTargetsOrdersNewestFirstAndMapsFields(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	base := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC).UTC()

	seeds := []struct {
		slot int
		id   string
		path string
		at   time.Time
	}{
		{3, uuid.NewString(), "/tmp/aaa.log", base.Add(3 * time.Hour)},
		{1, uuid.NewString(), "/tmp/zzz.log", base.Add(1 * time.Hour)},
		{2, uuid.NewString(), "/tmp/mmm.log", base.Add(2 * time.Hour)},
	}
	for _, seed := range seeds {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO audit_targets (id, slot, path, created_at) VALUES ($1, $2, $3, $4)`,
			seed.id, seed.slot, seed.path, seed.at,
		); err != nil {
			t.Fatalf("seed slot %d: %v", seed.slot, err)
		}
	}

	targets, err := repository.ListAuditTargets(ctx)
	if err != nil {
		t.Fatalf("list audit targets: %v", err)
	}
	if len(targets) != 3 {
		t.Fatalf("listed %d targets, want 3", len(targets))
	}
	want := []struct {
		id   string
		path string
		at   time.Time
	}{
		{seeds[0].id, "/tmp/aaa.log", base.Add(3 * time.Hour)},
		{seeds[2].id, "/tmp/mmm.log", base.Add(2 * time.Hour)},
		{seeds[1].id, "/tmp/zzz.log", base.Add(1 * time.Hour)},
	}
	for i, expected := range want {
		if targets[i].Path != expected.path {
			t.Fatalf("target %d is %q, want %q — listing is not newest first", i, targets[i].Path, expected.path)
		}
		if targets[i].ID != expected.id {
			t.Fatalf("target %d id is %q, want %q — id is not paired with its own row", i, targets[i].ID, expected.id)
		}
		if !targets[i].CreatedAt.Equal(expected.at) {
			t.Fatalf("target %d created_at is %v, want %v", i, targets[i].CreatedAt, expected.at)
		}
	}
}

func TestDeleteAuditTargetRemovesItAndFreesTheSlot(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	at := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

	id := uuid.NewString()
	target, err := repository.CreateAuditTarget(ctx, id, "/tmp/gone.log", at)
	if err != nil {
		t.Fatalf("create audit target: %v", err)
	}
	// The created row is returned, not merely written: a create that answered
	// with a correct id and an empty path would otherwise pass every test here.
	if target.ID != id || target.Path != "/tmp/gone.log" || !target.CreatedAt.Equal(at) {
		t.Fatalf("create returned %+v, want id %s, path /tmp/gone.log, at %v", target, id, at)
	}
	if err := repository.DeleteAuditTarget(ctx, target.ID); err != nil {
		t.Fatalf("delete audit target: %v", err)
	}
	targets, err := repository.ListAuditTargets(ctx)
	if err != nil {
		t.Fatalf("list audit targets: %v", err)
	}
	if len(targets) != 0 {
		t.Fatalf("listed %d targets after deleting the only one — delete is a no-op", len(targets))
	}
	// Non-nil, so the API renders [] rather than null for an empty configuration.
	if targets == nil {
		t.Fatal("empty listing is nil, which serialises as null rather than []")
	}
}

// A duplicate id must read as a conflict, not as a raw constraint error from
// pgx naming the table and index (ADR-0017).
func TestDuplicateAuditTargetIDIsAConflict(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	at := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

	id := uuid.NewString()
	if _, err := repository.CreateAuditTarget(ctx, id, "/tmp/one.log", at); err != nil {
		t.Fatalf("create audit target: %v", err)
	}
	if _, err := repository.CreateAuditTarget(ctx, id, "/tmp/two.log", at); !errors.Is(err, registry.ErrConflict) {
		t.Fatalf("duplicate id: want ErrConflict, got %v", err)
	}
}

func TestDeleteMissingAuditTargetIsNotFound(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	repository := store.NewRepository(db)
	if err := repository.DeleteAuditTarget(context.Background(), uuid.NewString()); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("delete missing target: want ErrNotFound, got %v", err)
	}
}
