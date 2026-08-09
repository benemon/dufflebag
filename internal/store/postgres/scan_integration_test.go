//go:build integration

package postgres_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/scan"
	"github.com/benemon/dufflebag/internal/store/objectstore"
	store "github.com/benemon/dufflebag/internal/store/postgres"
)

// seedScanParents inserts the FK ancestry a scan run needs: bucket, version,
// build, sbom and one sbom_packages row, all under the tenant.
func seedScanParents(t *testing.T, db *sql.DB, org, project, suffix string) (buildID, sbomID string) {
	t.Helper()
	ctx := context.Background()
	tx, err := store.BeginTenant(ctx, db, org, project)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	buildID, sbomID = "scanbuild-"+suffix, "scansbom-"+suffix
	for _, stmt := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO buckets (organization_id, project_id, id, name, created_at, updated_at)
			VALUES ($1,$2,'scanbucket-'||$3,'scan-'||$3,$4,$4)`, []any{org, project, suffix, now}},
		{`INSERT INTO versions (organization_id, project_id, id, bucket_id, fingerprint, template_type, complete, sequence, created_at, updated_at)
			VALUES ($1,$2,'scanversion-'||$3,'scanbucket-'||$3,'fp-scan-'||$3,'HCL2',true,1,$4,$4)`, []any{org, project, suffix, now}},
		{`INSERT INTO builds (organization_id, project_id, id, version_id, component_type, status, platform, metadata_seen, created_at, updated_at)
			VALUES ($1,$2,$3,'scanversion-'||$4,'docker','done','docker',true,$5,$5)`, []any{org, project, buildID, suffix, now}},
		{`INSERT INTO sboms (organization_id, project_id, id, build_id, name, format, object_key, created_at)
			VALUES ($1,$2,$3,$4,'sbom.spdx.json','SPDX','scan-key-'||$5,$6)`, []any{org, project, sbomID, buildID, suffix, now}},
		{`INSERT INTO sbom_packages (organization_id, project_id, sbom_id, name, version, purl)
			VALUES ($1,$2,$3,'busybox','1.36.1-r0','pkg:apk/alpine/busybox@1.36.1-r0')`, []any{org, project, sbomID}},
	} {
		if _, err := tx.ExecContext(ctx, stmt.query, stmt.args...); err != nil {
			t.Fatalf("seed scan parents: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return buildID, sbomID
}

func scanFindingFixture(sbomID, advisory string, seen time.Time) scan.Finding {
	return scan.Finding{
		Package: scan.Package{SBOMID: sbomID, Name: "busybox", Version: "1.36.1-r0",
			Purl: "pkg:apk/alpine/busybox@1.36.1-r0"},
		ID:            advisory,
		Summary:       "stack overflow in ash",
		Aliases:       []string{"CVE-2022-48174"},
		FixedVersions: []string{"1.36.1-r2"},
		Modified:      seen,
		Severities: []scan.SeverityValue{
			{Source: "osv", Type: "CVSS_V3", Value: "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"},
		},
		Severity: scan.SeverityCritical,
	}
}

func scanRunFixture(id, buildID string, sequence int64, status string, at time.Time, transcript []byte) store.ScanRun {
	sum := sha256.Sum256(transcript)
	return store.ScanRun{
		ID: id, BuildID: buildID, RunSequence: sequence, Status: status,
		Adapter: "osv", Engine: "https://api.osv.dev", DatabaseRevision: "unreported",
		ObservedAt: at, TranscriptDigest: hex.EncodeToString(sum[:]),
		Coverage:  scan.Coverage{Submitted: 1},
		CreatedAt: at,
	}
}

func TestScanStore(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	config, objects := openTestObjectStore(t)
	repo := store.NewRepositoryWithObjectStore(db, objects)
	repo.SetKeyring(testRing(t))
	tenant := store.ParseTenant(orgA, projectA)
	ctx := context.Background()
	base := time.Date(2026, 8, 7, 9, 0, 0, 0, time.UTC)

	allocate := func() int64 {
		t.Helper()
		sequence, err := repo.AllocateScanRunSequence(ctx, tenant)
		if err != nil {
			t.Fatal(err)
		}
		return sequence
	}

	t.Run("round trip with ordering guard and first seen", func(t *testing.T) {
		buildID, sbomID := seedScanParents(t, db, orgA, projectA, "roundtrip")
		transcript := []byte("transcript-roundtrip-one")
		seq1, seq2 := allocate(), allocate()

		// The NEWER run completes first.
		run2 := scanRunFixture("run-rt-2", buildID, seq2, store.ScanRunSucceeded, base.Add(time.Hour), transcript)
		if err := repo.RecordScanRun(ctx, tenant, run2, []scan.Finding{scanFindingFixture(sbomID, "ALPINE-CVE-2022-48174", base.Add(time.Hour))}, transcript); err != nil {
			t.Fatal(err)
		}
		// The older completion arrives late and must not advance anything.
		run1 := scanRunFixture("run-rt-1", buildID, seq1, store.ScanRunSucceeded, base, transcript)
		if err := repo.RecordScanRun(ctx, tenant, run1, nil, transcript); err != nil {
			t.Fatal(err)
		}
		state, err := repo.GetBuildScanState(ctx, tenant, buildID)
		if err != nil {
			t.Fatal(err)
		}
		if state.CurrentFindingsRunID != "run-rt-2" || state.LatestAttemptRunID != "run-rt-2" {
			t.Fatalf("state = %+v: an older run advanced a pointer", state)
		}

		// A newer FAILED run becomes latest_attempt but never erases current.
		seq3 := allocate()
		run3 := scanRunFixture("run-rt-3", buildID, seq3, store.ScanRunFailed, base.Add(2*time.Hour), transcript)
		run3.Error = "provider unreachable"
		if err := repo.RecordScanRun(ctx, tenant, run3, nil, transcript); err != nil {
			t.Fatal(err)
		}
		state, err = repo.GetBuildScanState(ctx, tenant, buildID)
		if err != nil {
			t.Fatal(err)
		}
		if state.CurrentFindingsRunID != "run-rt-2" || state.LatestAttemptRunID != "run-rt-3" {
			t.Fatalf("state = %+v: failed run handling wrong", state)
		}

		// A newer success copies first_seen_at forward for the same finding.
		seq4 := allocate()
		run4 := scanRunFixture("run-rt-4", buildID, seq4, store.ScanRunSucceeded, base.Add(3*time.Hour), transcript)
		findings := []scan.Finding{
			scanFindingFixture(sbomID, "ALPINE-CVE-2022-48174", base.Add(3*time.Hour)),
			scanFindingFixture(sbomID, "ALPINE-CVE-2099-9999", base.Add(3*time.Hour)),
		}
		if err := repo.RecordScanRun(ctx, tenant, run4, findings, transcript); err != nil {
			t.Fatal(err)
		}
		stored, err := repo.ListScanFindings(ctx, tenant, "run-rt-4")
		if err != nil {
			t.Fatal(err)
		}
		if len(stored) != 2 {
			t.Fatalf("findings = %d, want 2", len(stored))
		}
		for _, f := range stored {
			switch f.ID {
			case "ALPINE-CVE-2022-48174":
				if !f.FirstSeenAt.Equal(base.Add(time.Hour)) {
					t.Errorf("first seen = %v, want copied forward from run-rt-2", f.FirstSeenAt)
				}
				if len(f.Severities) != 1 || f.Severities[0].Value != "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H" {
					t.Errorf("severities did not round-trip verbatim: %+v", f.Severities)
				}
				if len(f.FixedVersions) != 1 || f.FixedVersions[0] != "1.36.1-r2" {
					t.Errorf("fixed versions did not round-trip: %v", f.FixedVersions)
				}
			case "ALPINE-CVE-2099-9999":
				if !f.FirstSeenAt.Equal(base.Add(3 * time.Hour)) {
					t.Errorf("new finding first seen = %v, want observation time", f.FirstSeenAt)
				}
			default:
				t.Errorf("unexpected finding %s", f.ID)
			}
		}

		// The transcript round-trips through compression and sealing.
		got, err := repo.GetScanTranscript(ctx, tenant, "run-rt-4")
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(transcript) {
			t.Fatalf("transcript = %q", got)
		}
	})

	t.Run("immutability and FK teeth", func(t *testing.T) {
		buildID, sbomID := seedScanParents(t, db, orgA, projectA, "teeth")
		transcript := []byte("transcript-teeth")
		run := scanRunFixture("run-teeth-1", buildID, allocate(), store.ScanRunSucceeded, base, transcript)
		if err := repo.RecordScanRun(ctx, tenant, run, []scan.Finding{scanFindingFixture(sbomID, "ALPINE-CVE-2022-48174", base)}, transcript); err != nil {
			t.Fatal(err)
		}
		tx, err := store.BeginTenant(ctx, db, orgA, projectA)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.ExecContext(ctx,
			`UPDATE scan_runs SET status = 'failed' WHERE id = 'run-teeth-1'`); err == nil ||
			!strings.Contains(err.Error(), "immutable") {
			t.Fatalf("scan_runs UPDATE err = %v, want immutability rejection", err)
		}
		_ = tx.Rollback()

		tx, err = store.BeginTenant(ctx, db, orgA, projectA)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.ExecContext(ctx,
			`UPDATE scan_findings SET derived_severity = 'low' WHERE run_id = 'run-teeth-1'`); err == nil ||
			!strings.Contains(err.Error(), "immutable") {
			t.Fatalf("scan_findings UPDATE err = %v, want immutability rejection", err)
		}
		_ = tx.Rollback()

		// A finding whose package identity is not in sbom_packages fails.
		badRun := scanRunFixture("run-teeth-2", buildID, allocate(), store.ScanRunSucceeded, base, transcript)
		bad := scanFindingFixture(sbomID, "ALPINE-CVE-2022-48174", base)
		bad.Package.Version = "no-such-version"
		if err := repo.RecordScanRun(ctx, tenant, badRun, []scan.Finding{bad}, transcript); err == nil {
			t.Fatal("finding with an incomplete package identity was accepted")
		}
		// A finding against another tenant's SBOM fails inside this tenant.
		otherRun := scanRunFixture("run-teeth-3", buildID, allocate(), store.ScanRunSucceeded, base, transcript)
		foreign := scanFindingFixture("not-my-sbom", "ALPINE-CVE-2022-48174", base)
		if err := repo.RecordScanRun(ctx, tenant, otherRun, []scan.Finding{foreign}, transcript); err == nil {
			t.Fatal("finding against a foreign sbom id was accepted")
		}
	})

	t.Run("transcript expiry retains digest", func(t *testing.T) {
		buildID, _ := seedScanParents(t, db, orgA, projectA, "expiry")
		transcript := []byte("transcript-expiry")
		run := scanRunFixture("run-exp-1", buildID, allocate(), store.ScanRunSucceeded, base, transcript)
		if err := repo.RecordScanRun(ctx, tenant, run, nil, transcript); err != nil {
			t.Fatal(err)
		}
		var objectKey string
		tx, _ := store.BeginTenant(ctx, db, orgA, projectA)
		if err := tx.QueryRowContext(ctx,
			`SELECT object_key FROM scan_transcripts WHERE run_id = 'run-exp-1'`).Scan(&objectKey); err != nil {
			t.Fatal(err)
		}
		_ = tx.Rollback()

		expired, err := repo.ExpireScanTranscripts(ctx, tenant, base.Add(8*24*time.Hour), 100)
		if err != nil {
			t.Fatal(err)
		}
		if expired < 1 {
			t.Fatalf("expired = %d, want at least the run-exp-1 transcript", expired)
		}
		if _, err := objects.Get(ctx, objectKey); err == nil {
			t.Fatal("transcript object survived expiry")
		}
		stored, err := repo.GetScanRun(ctx, tenant, "run-exp-1")
		if err != nil {
			t.Fatal(err)
		}
		if stored.TranscriptDigest == "" {
			t.Fatal("digest did not survive expiry")
		}
		if _, err := repo.GetScanTranscript(ctx, tenant, "run-exp-1"); err == nil {
			t.Fatal("expired transcript still served")
		}
		// Idempotent: a rerun finds nothing new to do.
		if again, err := repo.ExpireScanTranscripts(ctx, tenant, base.Add(8*24*time.Hour), 100); err != nil || again != 0 {
			t.Fatalf("rerun = %d, %v", again, err)
		}
	})

	t.Run("retention preserves pointer targets", func(t *testing.T) {
		buildID, sbomID := seedScanParents(t, db, orgA, projectA, "retention")
		transcript := []byte("transcript-retention")
		seq := []int64{allocate(), allocate(), allocate()}
		runs := []store.ScanRun{
			scanRunFixture("run-ret-1", buildID, seq[0], store.ScanRunSucceeded, base, transcript),
			scanRunFixture("run-ret-2", buildID, seq[1], store.ScanRunSucceeded, base.Add(time.Hour), transcript),
			scanRunFixture("run-ret-3", buildID, seq[2], store.ScanRunFailed, base.Add(2*time.Hour), transcript),
		}
		for _, run := range runs {
			if err := repo.RecordScanRun(ctx, tenant, run, []scan.Finding{scanFindingFixture(sbomID, "ALPINE-CVE-2022-48174", run.ObservedAt)}, transcript); err != nil {
				t.Fatal(err)
			}
		}
		// The purge is tenant-wide, so superseded runs left behind by the
		// earlier subtests are legitimate victims too; the property under
		// test is that pointer targets survive and superseded history does
		// not.
		purged, err := repo.PurgeSupersededScanRuns(ctx, tenant, base.Add(3*time.Hour), 100)
		if err != nil {
			t.Fatal(err)
		}
		if purged < 1 {
			t.Fatalf("purged = %d, want at least run-ret-1", purged)
		}
		if _, err := repo.GetScanRun(ctx, tenant, "run-ret-2"); err != nil {
			t.Fatalf("current findings run was purged: %v", err)
		}
		if _, err := repo.GetScanRun(ctx, tenant, "run-ret-3"); err != nil {
			t.Fatalf("latest attempt run was purged: %v", err)
		}
		if _, err := repo.GetScanRun(ctx, tenant, "run-ret-1"); err == nil {
			t.Fatal("superseded run survived retention")
		}
	})

	t.Run("transcript write failure records nothing", func(t *testing.T) {
		buildID, _ := seedScanParents(t, db, orgA, projectA, "putfail")
		dead, err := objectstore.New(objectstore.Config{
			Endpoint: "http://127.0.0.1:1", Region: config.Region,
			Bucket: config.Bucket, AccessKey: config.AccessKey, SecretKey: config.SecretKey,
		})
		if err != nil {
			t.Fatal(err)
		}
		broken := store.NewRepositoryWithObjectStore(db, dead)
		transcript := []byte("transcript-putfail")
		run := scanRunFixture("run-putfail-1", buildID, allocate(), store.ScanRunSucceeded, base, transcript)
		if err := broken.RecordScanRun(ctx, tenant, run, nil, transcript); err == nil {
			t.Fatal("run recorded despite transcript write failure")
		}
		if got, err := repo.GetScanRun(ctx, tenant, "run-putfail-1"); err == nil {
			t.Fatalf("run row exists after put failure: %+v", got)
		}
		if state, err := repo.GetBuildScanState(ctx, tenant, buildID); err != nil || state != nil {
			t.Fatalf("state advanced after put failure: %+v, %v", state, err)
		}
	})

	t.Run("sealed bucket bytes are not plaintext", func(t *testing.T) {
		buildID, _ := seedScanParents(t, db, orgA, projectA, "sealed")
		transcript := []byte("SECRET-TRANSCRIPT-MARKER-dufflebag")
		run := scanRunFixture("run-sealed-1", buildID, allocate(), store.ScanRunSucceeded, base, transcript)
		if err := repo.RecordScanRun(ctx, tenant, run, nil, transcript); err != nil {
			t.Fatal(err)
		}
		var objectKey string
		tx, _ := store.BeginTenant(ctx, db, orgA, projectA)
		if err := tx.QueryRowContext(ctx,
			`SELECT object_key FROM scan_transcripts WHERE run_id = 'run-sealed-1'`).Scan(&objectKey); err != nil {
			t.Fatal(err)
		}
		_ = tx.Rollback()
		raw, err := objects.Get(ctx, objectKey)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "SECRET-TRANSCRIPT-MARKER") {
			t.Fatal("bucket bytes contain the plaintext transcript")
		}
	})
}

// TestScanStoreReviewFindings covers the guards added after the duf-o0ou.3
// adversarial review: each was a way a tampered or mistaken input could reach
// a valid-looking stored result.
func TestScanStoreReviewFindings(t *testing.T) {
	db, adminURL, cleanup := openTestDatabase(t)
	defer cleanup()
	_, objects := openTestObjectStore(t)
	repo := store.NewRepositoryWithObjectStore(db, objects)
	repo.SetKeyring(testRing(t))
	tenant := store.ParseTenant(orgA, projectA)
	ctx := context.Background()
	base := time.Date(2026, 8, 7, 9, 0, 0, 0, time.UTC)

	allocate := func() int64 {
		t.Helper()
		sequence, err := repo.AllocateScanRunSequence(ctx, tenant)
		if err != nil {
			t.Fatal(err)
		}
		return sequence
	}

	t.Run("digest mismatch is refused before anything is written", func(t *testing.T) {
		buildID, _ := seedScanParents(t, db, orgA, projectA, "digest")
		run := scanRunFixture("run-digest-1", buildID, allocate(), store.ScanRunSucceeded, base, []byte("declared"))
		if err := repo.RecordScanRun(ctx, tenant, run, nil, []byte("actually-different")); err == nil {
			t.Fatal("a run whose digest does not match its transcript was recorded")
		}
		if _, err := repo.GetScanRun(ctx, tenant, "run-digest-1"); err == nil {
			t.Fatal("run row written despite the digest mismatch")
		}
	})

	t.Run("late older run does not inherit a newer first seen", func(t *testing.T) {
		buildID, sbomID := seedScanParents(t, db, orgA, projectA, "firstseen")
		transcript := []byte("transcript-firstseen")
		seq1, seq2 := allocate(), allocate()
		newer := scanRunFixture("run-fs-2", buildID, seq2, store.ScanRunSucceeded, base.Add(time.Hour), transcript)
		if err := repo.RecordScanRun(ctx, tenant, newer, []scan.Finding{scanFindingFixture(sbomID, "ALPINE-CVE-2022-48174", base.Add(time.Hour))}, transcript); err != nil {
			t.Fatal(err)
		}
		older := scanRunFixture("run-fs-1", buildID, seq1, store.ScanRunSucceeded, base, transcript)
		if err := repo.RecordScanRun(ctx, tenant, older, []scan.Finding{scanFindingFixture(sbomID, "ALPINE-CVE-2022-48174", base)}, transcript); err != nil {
			t.Fatal(err)
		}
		stored, err := repo.ListScanFindings(ctx, tenant, "run-fs-1")
		if err != nil {
			t.Fatal(err)
		}
		if len(stored) != 1 {
			t.Fatalf("findings = %d", len(stored))
		}
		if stored[0].FirstSeenAt.After(older.ObservedAt) {
			t.Fatalf("first seen %v is after the run's own observation %v: a later run's value was inherited",
				stored[0].FirstSeenAt, older.ObservedAt)
		}
	})

	t.Run("delimiter collision cannot forge a MAC", func(t *testing.T) {
		buildID, _ := seedScanParents(t, db, orgA, projectA, "delim")
		transcript := []byte("transcript-delim")
		run := scanRunFixture("run-delim-1", buildID, allocate(), store.ScanRunFailed, base, transcript)
		run.Error = "timeout|osv"
		run.Adapter = "official"
		if err := repo.RecordScanRun(ctx, tenant, run, nil, transcript); err != nil {
			t.Fatal(err)
		}
		superURL, err := url.Parse(adminURL)
		if err != nil {
			t.Fatal(err)
		}
		superURL.User = url.UserPassword("postgres", "postgres")
		admin, err := sql.Open("pgx", superURL.String())
		if err != nil {
			t.Fatal(err)
		}
		defer admin.Close()
		// The shifted-boundary rewrite: same concatenation, different values.
		for _, statement := range []string{
			`ALTER TABLE scan_runs DISABLE TRIGGER scan_runs_immutable`,
			`UPDATE scan_runs SET error = 'timeout', adapter = 'osv|official' WHERE id = 'run-delim-1'`,
			`ALTER TABLE scan_runs ENABLE TRIGGER scan_runs_immutable`,
		} {
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatalf("tamper: %v", err)
			}
		}
		if _, err := repo.GetScanRun(ctx, tenant, "run-delim-1"); err == nil {
			t.Fatal("a delimiter-shifted row verified against the original MAC")
		}
	})

	t.Run("tampered transcript locator is not a delete target", func(t *testing.T) {
		buildID, _ := seedScanParents(t, db, orgA, projectA, "locator")
		transcript := []byte("transcript-locator")
		run := scanRunFixture("run-loc-1", buildID, allocate(), store.ScanRunSucceeded, base, transcript)
		if err := repo.RecordScanRun(ctx, tenant, run, nil, transcript); err != nil {
			t.Fatal(err)
		}
		var victimKey string
		tx, _ := store.BeginTenant(ctx, db, orgA, projectA)
		if err := tx.QueryRowContext(ctx,
			`SELECT object_key FROM scan_transcripts WHERE run_id = 'run-loc-1'`).Scan(&victimKey); err != nil {
			t.Fatal(err)
		}
		_ = tx.Rollback()

		superURL, err := url.Parse(adminURL)
		if err != nil {
			t.Fatal(err)
		}
		superURL.User = url.UserPassword("postgres", "postgres")
		admin, err := sql.Open("pgx", superURL.String())
		if err != nil {
			t.Fatal(err)
		}
		defer admin.Close()
		if _, err := admin.ExecContext(ctx,
			`UPDATE scan_transcripts SET object_key = 'someone-elses-object' WHERE run_id = 'run-loc-1'`); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.ExpireScanTranscripts(ctx, tenant, base.Add(8*24*time.Hour), 100); err == nil {
			t.Fatal("expiry accepted a tampered locator as a delete target")
		}
		if _, err := repo.GetScanTranscript(ctx, tenant, "run-loc-1"); err == nil {
			t.Fatal("a tampered locator was served")
		}
	})
}

// TestScanRowTamperingFailsClosed proves each MAC-protected scan row type
// refuses to load after direct SQL modification — the psql-attacker posture
// of ADR-0024, using the superuser connection that bypasses both RLS and
// (after disabling the trigger) the immutability guard.
func TestScanRowTamperingFailsClosed(t *testing.T) {
	db, adminURL, cleanup := openTestDatabase(t)
	defer cleanup()
	_, objects := openTestObjectStore(t)
	repo := store.NewRepositoryWithObjectStore(db, objects)
	repo.SetKeyring(testRing(t))
	tenant := store.ParseTenant(orgA, projectA)
	ctx := context.Background()
	base := time.Date(2026, 8, 7, 9, 0, 0, 0, time.UTC)

	buildID, sbomID := seedScanParents(t, db, orgA, projectA, "tamper")
	transcript := []byte("transcript-tamper")
	sequence, err := repo.AllocateScanRunSequence(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	run := scanRunFixture("run-tamper-1", buildID, sequence, store.ScanRunSucceeded, base, transcript)
	if err := repo.RecordScanRun(ctx, tenant, run, []scan.Finding{scanFindingFixture(sbomID, "ALPINE-CVE-2022-48174", base)}, transcript); err != nil {
		t.Fatal(err)
	}

	// openTestDatabase returns the unprivileged application URL; the tamper
	// posture is the container superuser, who owns the tables and bypasses
	// RLS — the realistic psql attacker.
	superURL, err := url.Parse(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	superURL.User = url.UserPassword("postgres", "postgres")
	admin, err := sql.Open("pgx", superURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	tamper := func(statements ...string) {
		t.Helper()
		for _, statement := range statements {
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatalf("tamper: %s: %v", statement, err)
			}
		}
	}

	tamper(
		`ALTER TABLE scan_runs DISABLE TRIGGER scan_runs_immutable`,
		`UPDATE scan_runs SET status = 'failed' WHERE id = 'run-tamper-1'`,
		`ALTER TABLE scan_runs ENABLE TRIGGER scan_runs_immutable`,
	)
	if _, err := repo.GetScanRun(ctx, tenant, "run-tamper-1"); err == nil {
		t.Fatal("tampered scan run loaded")
	}
	tamper(
		`ALTER TABLE scan_runs DISABLE TRIGGER scan_runs_immutable`,
		`UPDATE scan_runs SET status = 'succeeded' WHERE id = 'run-tamper-1'`,
		`ALTER TABLE scan_runs ENABLE TRIGGER scan_runs_immutable`,
	)
	if _, err := repo.GetScanRun(ctx, tenant, "run-tamper-1"); err != nil {
		t.Fatalf("restored run still refused: %v", err)
	}

	tamper(
		`ALTER TABLE scan_findings DISABLE TRIGGER scan_findings_immutable`,
		`UPDATE scan_findings SET derived_severity = 'negligible' WHERE run_id = 'run-tamper-1'`,
		`ALTER TABLE scan_findings ENABLE TRIGGER scan_findings_immutable`,
	)
	if _, err := repo.ListScanFindings(ctx, tenant, "run-tamper-1"); err == nil {
		t.Fatal("tampered finding loaded")
	}

	tamper(`UPDATE build_scan_state SET current_findings_run_id = NULL WHERE build_id = ` + fmt.Sprintf("'%s'", buildID))
	if _, err := repo.GetBuildScanState(ctx, tenant, buildID); err == nil {
		t.Fatal("tampered build scan state loaded")
	}
}
