package postgres

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/benemon/dufflebag/internal/scan"
	"github.com/benemon/dufflebag/internal/store/objectstore"
)

// scanTranscriptRetention is the epic's fixed seven-day transcript window;
// run history retention is the caller-configured 90 days.
const scanTranscriptRetention = 7 * 24 * time.Hour

// maxScanTranscriptBytes bounds transcript decompression. The digest cannot
// be checked until the bytes are decompressed, so the limit is what stops a
// swapped object from exhausting memory first.
const maxScanTranscriptBytes = 256 << 20

// ScanRun is one terminal scanner attempt. Rows are immutable: the sequence
// is allocated before provider egress and the row is inserted exactly once,
// already terminal.
type ScanRun struct {
	ID               string
	BuildID          string
	RunSequence      int64
	Status           string
	Error            string
	Adapter          string
	Engine           string
	DatabaseRevision string
	ObservedAt       time.Time
	TranscriptDigest string
	Coverage         scan.Coverage
	CreatedAt        time.Time
}

const (
	ScanRunSucceeded = "succeeded"
	ScanRunFailed    = "failed"
)

// StoredFinding is a scan.Finding plus the store-owned first-seen provenance.
type StoredFinding struct {
	scan.Finding
	FirstSeenAt time.Time
}

// BuildScanState carries the per-build currency pointers.
type BuildScanState struct {
	BuildID              string
	CurrentFindingsRunID string
	LatestAttemptRunID   string
}

// AllocateScanRunSequence reserves the run's ordering position before any
// provider egress, so an attempt that dies mid-flight cannot reuse it.
func (r *Repository) AllocateScanRunSequence(ctx context.Context, tenant Tenant) (int64, error) {
	tx, _, err := r.begin(ctx, tenant)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var sequence int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO scan_run_counters (organization_id, project_id, next_sequence)
		VALUES ($1, $2, 2)
		ON CONFLICT (organization_id, project_id) DO UPDATE
			SET next_sequence = scan_run_counters.next_sequence + 1
		RETURNING next_sequence - 1`,
		tenant.OrganizationID, tenant.ProjectID,
	).Scan(&sequence); err != nil {
		return 0, fmt.Errorf("allocate scan run sequence: %w", err)
	}
	return sequence, tx.Commit()
}

// RecordScanRun persists one terminal attempt: transcript to object storage
// first (a transcript write failure fails the run — nothing is recorded),
// then the immutable run row, its findings, and the guarded advance of the
// per-build pointers, all in one build-serialized transaction.
func (r *Repository) RecordScanRun(ctx context.Context, tenant Tenant, run ScanRun, findings []scan.Finding, transcript []byte) error {
	objects, err := r.objectStore()
	if err != nil {
		return err
	}
	run.ObservedAt = scanWriteTime(run.ObservedAt)
	run.CreatedAt = scanWriteTime(run.CreatedAt)

	// The digest is what survives the transcript's seven-day life, so a
	// mismatch must fail before anything immutable is written rather than
	// surfacing as an unreadable transcript weeks later.
	sum := sha256.Sum256(transcript)
	if digest := hex.EncodeToString(sum[:]); digest != run.TranscriptDigest {
		return fmt.Errorf("scan run %s: transcript digest %s does not match the supplied transcript (%s)",
			run.ID, run.TranscriptDigest, digest)
	}

	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(transcript); err != nil {
		return fmt.Errorf("compress scan transcript: %w", err)
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("compress scan transcript: %w", err)
	}
	objectKey := objectstore.TranscriptKey(tenant.OrganizationID.String(), tenant.ProjectID.String(), run.ID, compressed.Bytes())
	payload := compressed.Bytes()
	if r.ring != nil {
		sealed, err := r.ring.Encrypt(payload, payloadAAD(tenant, "transcript", run.ID))
		if err != nil {
			return fmt.Errorf("seal scan transcript: %w", err)
		}
		payload = sealed
	}
	if err := objects.PutTranscript(ctx, objectKey, payload); err != nil {
		return fmt.Errorf("%w: %v", ErrObjectStorageUnavailable, err)
	}

	tx, _, err := r.begin(ctx, tenant)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Completion is serialized per build so two replicas racing on the same
	// event cannot interleave their pointer reads and writes.
	if _, err := tx.ExecContext(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		tenant.OrganizationID.String()+"|"+tenant.ProjectID.String()+"|"+run.BuildID,
	); err != nil {
		return fmt.Errorf("acquire build scan lock: %w", err)
	}

	coverage, err := json.Marshal(run.Coverage)
	if err != nil {
		return fmt.Errorf("encode coverage: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO scan_runs (
			organization_id, project_id, id, bucket_id, build_id, run_sequence, status,
			error, adapter, engine, database_revision, observed_at,
			transcript_digest, coverage, created_at, integrity_mac
		)
		SELECT $1, $2, $3, builds.bucket_id, builds.id, $5, $6, $7, $8,
			$9, $10, $11, $12, $13, $14, $15
		FROM builds
		WHERE builds.id = $4`,
		tenant.OrganizationID, tenant.ProjectID, run.ID, run.BuildID,
		run.RunSequence, run.Status, run.Error, run.Adapter, run.Engine,
		run.DatabaseRevision, run.ObservedAt, run.TranscriptDigest, coverage,
		run.CreatedAt, r.rowMAC(scanRunMACMessage(tenant, run)),
	); err != nil {
		return fmt.Errorf("insert scan run: %w", err)
	}

	expiresAt := scanWriteTime(run.CreatedAt.Add(scanTranscriptRetention))
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO scan_transcripts (organization_id, project_id, bucket_id, run_id, object_key, expires_at, integrity_mac)
		SELECT $1, $2, scan_runs.bucket_id, scan_runs.id, $4, $5, $6
		FROM scan_runs
		WHERE scan_runs.id = $3`,
		tenant.OrganizationID, tenant.ProjectID, run.ID, objectKey, expiresAt,
		r.rowMAC(scanTranscriptMACMessage(tenant, run.ID, objectKey, expiresAt)),
	); err != nil {
		return fmt.Errorf("insert scan transcript locator: %w", err)
	}

	state, err := lockBuildScanState(ctx, tx, r, tenant, run.BuildID)
	if err != nil {
		return err
	}

	// Copy first-seen forward only when this run will itself become current:
	// an older completion arriving late must not inherit a later run's
	// first-seen and end up claiming a finding was seen after it observed it.
	inherit := false
	if state != nil && state.CurrentFindingsRunID != "" && run.Status == ScanRunSucceeded {
		currentSequence, err := verifiedRunSequence(ctx, tx, r, tenant, state.CurrentFindingsRunID)
		if err != nil {
			return err
		}
		inherit = run.RunSequence > currentSequence
	}

	for _, f := range findings {
		stored := StoredFinding{Finding: f, FirstSeenAt: run.ObservedAt}
		if inherit {
			previous, err := readScanFinding(ctx, tx, r, tenant, state.CurrentFindingsRunID, f.Package, f.ID)
			switch {
			case err == nil:
				stored.FirstSeenAt = previous.FirstSeenAt
			case errors.Is(err, sql.ErrNoRows):
				// New finding: first seen now.
			default:
				return err
			}
		}
		if err := insertScanFinding(ctx, tx, r, tenant, run.ID, stored); err != nil {
			return err
		}
	}

	if err := advanceBuildScanState(ctx, tx, r, tenant, run, state); err != nil {
		return err
	}
	return tx.Commit()
}

func insertScanFinding(ctx context.Context, tx *sql.Tx, r *Repository, tenant Tenant, runID string, f StoredFinding) error {
	aliases, err := json.Marshal(orEmpty(f.Aliases))
	if err != nil {
		return fmt.Errorf("encode aliases: %w", err)
	}
	related, err := json.Marshal(orEmpty(f.Related))
	if err != nil {
		return fmt.Errorf("encode related: %w", err)
	}
	fixed, err := json.Marshal(orEmpty(f.FixedVersions))
	if err != nil {
		return fmt.Errorf("encode fixed versions: %w", err)
	}
	severities, err := json.Marshal(f.Severities)
	if err != nil {
		return fmt.Errorf("encode severities: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO scan_findings (
			organization_id, project_id, bucket_id, run_id, sbom_id, package_name,
			package_version, purl, advisory_id, summary, aliases, related,
			published, modified, withdrawn, fixed_versions, severities,
			derived_severity, first_seen_at, integrity_mac
		)
		SELECT $1, $2, scan_runs.bucket_id, scan_runs.id, $4, $5, $6, $7, $8, $9, $10, $11,
			NULLIF($12, '0001-01-01T00:00:00Z'::timestamptz),
			NULLIF($13, '0001-01-01T00:00:00Z'::timestamptz),
			NULLIF($14, '0001-01-01T00:00:00Z'::timestamptz),
			$15, $16, $17, $18, $19
		FROM scan_runs
		WHERE scan_runs.id = $3`,
		tenant.OrganizationID, tenant.ProjectID, runID,
		f.Package.SBOMID, f.Package.Name, f.Package.Version, f.Package.Purl,
		f.ID, f.Summary, aliases, related,
		f.Published.UTC(), f.Modified.UTC(), f.Withdrawn.UTC(),
		fixed, severities, string(f.Severity), f.FirstSeenAt,
		r.rowMAC(scanFindingMACMessage(tenant, runID, canonicalStoredFinding(f))),
	); err != nil {
		return fmt.Errorf("insert scan finding %s: %w", f.ID, err)
	}
	return nil
}

// canonicalStoredFinding normalizes the fields whose stored form differs from
// the in-memory one, so write- and read-side MAC messages agree.
func canonicalStoredFinding(f StoredFinding) StoredFinding {
	f.Aliases = orEmpty(f.Aliases)
	f.Related = orEmpty(f.Related)
	f.FixedVersions = orEmpty(f.FixedVersions)
	f.Published = scanWriteTime(f.Published)
	f.Modified = scanWriteTime(f.Modified)
	f.Withdrawn = scanWriteTime(f.Withdrawn)
	f.FirstSeenAt = scanWriteTime(f.FirstSeenAt)
	return f
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// lockBuildScanState reads the pointer row inside the completion transaction
// and verifies its MAC: the row is about to be carried forward and re-signed,
// so consuming it unverified would launder a tampered pointer into a valid
// one. The advisory lock already serializes writers; FOR UPDATE keeps the
// read honest anyway.
func lockBuildScanState(ctx context.Context, tx *sql.Tx, r *Repository, tenant Tenant, buildID string) (*BuildScanState, error) {
	state := BuildScanState{BuildID: buildID}
	var current sql.NullString
	var mac []byte
	err := tx.QueryRowContext(ctx, `
		SELECT current_findings_run_id, latest_attempt_run_id, integrity_mac FROM build_scan_state
		WHERE organization_id = $1 AND project_id = $2 AND build_id = $3
		FOR UPDATE`,
		tenant.OrganizationID, tenant.ProjectID, buildID,
	).Scan(&current, &state.LatestAttemptRunID, &mac)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read build scan state: %w", err)
	}
	state.CurrentFindingsRunID = current.String
	if err := r.verifyRowMAC("build scan state "+buildID, mac,
		buildScanStateMACMessage(tenant, buildID, state.CurrentFindingsRunID, state.LatestAttemptRunID)); err != nil {
		return nil, err
	}
	return &state, nil
}

// verifiedRunSequence returns a referenced run's ordering position, verifying
// the whole row: the sequence decides whether current advances, so reading it
// unverified would let a tampered run dictate currency. A pointer to a run of
// another build is refused — the FK permits it, the invariant does not.
func verifiedRunSequence(ctx context.Context, tx *sql.Tx, r *Repository, tenant Tenant, runID string) (int64, error) {
	if runID == "" {
		return -1, nil
	}
	run, err := readScanRun(ctx, tx, r, tenant, runID)
	if err != nil {
		return 0, err
	}
	return run.RunSequence, nil
}

// readScanFinding returns one verified finding of a run.
func readScanFinding(ctx context.Context, tx *sql.Tx, r *Repository, tenant Tenant, runID string, pkg scan.Package, advisoryID string) (*StoredFinding, error) {
	rows, err := queryScanFindings(ctx, tx, r, tenant, runID, `
		  AND sbom_id = $4 AND package_name = $5 AND package_version = $6
		  AND purl = $7 AND advisory_id = $8`,
		pkg.SBOMID, pkg.Name, pkg.Version, pkg.Purl, advisoryID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, sql.ErrNoRows
	}
	return &rows[0], nil
}

func advanceBuildScanState(ctx context.Context, tx *sql.Tx, r *Repository, tenant Tenant, run ScanRun, state *BuildScanState) error {
	sequenceOf := func(runID string) (int64, error) {
		return verifiedRunSequence(ctx, tx, r, tenant, runID)
	}

	current, latest := "", ""
	if state != nil {
		current, latest = state.CurrentFindingsRunID, state.LatestAttemptRunID
	}
	latestSequence, err := sequenceOf(latest)
	if err != nil {
		return err
	}
	if run.RunSequence > latestSequence {
		latest = run.ID
	}
	if run.Status == ScanRunSucceeded {
		currentSequence, err := sequenceOf(current)
		if err != nil {
			return err
		}
		// The ordering guard: an older completion arriving late can never
		// overwrite newer current findings.
		if run.RunSequence > currentSequence {
			current = run.ID
		}
	}

	mac := r.rowMAC(buildScanStateMACMessage(tenant, run.BuildID, current, latest))
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO build_scan_state (organization_id, project_id, bucket_id, build_id, current_findings_run_id, latest_attempt_run_id, integrity_mac)
		SELECT $1, $2, builds.bucket_id, builds.id, NULLIF($4, ''), $5, $6
		FROM builds
		WHERE builds.id = $3
		ON CONFLICT (organization_id, project_id, build_id) DO UPDATE SET
			current_findings_run_id = NULLIF($4, ''),
			latest_attempt_run_id = $5,
			integrity_mac = $6`,
		tenant.OrganizationID, tenant.ProjectID, run.BuildID, current, latest, mac,
	); err != nil {
		return fmt.Errorf("advance build scan state: %w", err)
	}
	return nil
}

// GetBuildScanState returns the verified pointer row, or nil when the build
// has never been scanned.
func (r *Repository) GetBuildScanState(ctx context.Context, tenant Tenant, buildID string) (*BuildScanState, error) {
	tx, _, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	state := BuildScanState{BuildID: buildID}
	var current sql.NullString
	var mac []byte
	err = tx.QueryRowContext(ctx, `
		SELECT current_findings_run_id, latest_attempt_run_id, integrity_mac FROM build_scan_state
		WHERE organization_id = $1 AND project_id = $2 AND build_id = $3`,
		tenant.OrganizationID, tenant.ProjectID, buildID,
	).Scan(&current, &state.LatestAttemptRunID, &mac)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read build scan state: %w", err)
	}
	state.CurrentFindingsRunID = current.String
	if err := r.verifyRowMAC("build scan state "+buildID,
		mac, buildScanStateMACMessage(tenant, buildID, state.CurrentFindingsRunID, state.LatestAttemptRunID)); err != nil {
		return nil, err
	}
	return &state, nil
}

// GetScanRun returns the verified run row.
func (r *Repository) GetScanRun(ctx context.Context, tenant Tenant, runID string) (*ScanRun, error) {
	tx, _, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	return readScanRun(ctx, tx, r, tenant, runID)
}

func readScanRun(ctx context.Context, tx *sql.Tx, r *Repository, tenant Tenant, runID string) (*ScanRun, error) {
	run := ScanRun{ID: runID}
	var coverage []byte
	var mac []byte
	err := tx.QueryRowContext(ctx, `
		SELECT build_id, run_sequence, status, error, adapter, engine,
			database_revision, observed_at, transcript_digest, coverage,
			created_at, integrity_mac
		FROM scan_runs
		WHERE organization_id = $1 AND project_id = $2 AND id = $3`,
		tenant.OrganizationID, tenant.ProjectID, runID,
	).Scan(&run.BuildID, &run.RunSequence, &run.Status, &run.Error,
		&run.Adapter, &run.Engine, &run.DatabaseRevision, &run.ObservedAt,
		&run.TranscriptDigest, &coverage, &run.CreatedAt, &mac)
	if err != nil {
		return nil, fmt.Errorf("read scan run %s: %w", runID, err)
	}
	if err := json.Unmarshal(coverage, &run.Coverage); err != nil {
		return nil, fmt.Errorf("decode coverage: %w", err)
	}
	run.ObservedAt = scanWriteTime(run.ObservedAt)
	run.CreatedAt = scanWriteTime(run.CreatedAt)
	if err := r.verifyRowMAC("scan run "+runID, mac, scanRunMACMessage(tenant, run)); err != nil {
		return nil, err
	}
	return &run, nil
}

// ListScanFindings returns the verified findings of one run.
func (r *Repository) ListScanFindings(ctx context.Context, tenant Tenant, runID string) ([]StoredFinding, error) {
	tx, _, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	return queryScanFindings(ctx, tx, r, tenant, runID, "")
}

// queryScanFindings reads findings of a run, verifying every row's MAC.
func queryScanFindings(ctx context.Context, tx *sql.Tx, r *Repository, tenant Tenant, runID, filter string, args ...any) ([]StoredFinding, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT sbom_id, package_name, package_version, purl, advisory_id,
			summary, aliases, related, published, modified, withdrawn,
			fixed_versions, severities, derived_severity, first_seen_at,
			integrity_mac
		FROM scan_findings
		WHERE organization_id = $1 AND project_id = $2 AND run_id = $3`+filter+`
		ORDER BY sbom_id, package_name, package_version, purl, advisory_id`,
		append([]any{tenant.OrganizationID, tenant.ProjectID, runID}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("list scan findings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var findings []StoredFinding
	for rows.Next() {
		var f StoredFinding
		var aliases, related, fixed, severities, mac []byte
		var published, modified, withdrawn sql.NullTime
		if err := rows.Scan(&f.Package.SBOMID, &f.Package.Name, &f.Package.Version,
			&f.Package.Purl, &f.ID, &f.Summary, &aliases, &related,
			&published, &modified, &withdrawn, &fixed, &severities,
			&f.Severity, &f.FirstSeenAt, &mac); err != nil {
			return nil, fmt.Errorf("scan finding row: %w", err)
		}
		if err := json.Unmarshal(aliases, &f.Aliases); err != nil {
			return nil, fmt.Errorf("decode aliases: %w", err)
		}
		if err := json.Unmarshal(related, &f.Related); err != nil {
			return nil, fmt.Errorf("decode related: %w", err)
		}
		if err := json.Unmarshal(fixed, &f.FixedVersions); err != nil {
			return nil, fmt.Errorf("decode fixed versions: %w", err)
		}
		if err := json.Unmarshal(severities, &f.Severities); err != nil {
			return nil, fmt.Errorf("decode severities: %w", err)
		}
		f.Published, f.Modified, f.Withdrawn = published.Time, modified.Time, withdrawn.Time
		f.FirstSeenAt = scanWriteTime(f.FirstSeenAt)
		if err := r.verifyRowMAC("scan finding "+f.ID, mac,
			scanFindingMACMessage(tenant, runID, canonicalStoredFinding(f))); err != nil {
			return nil, err
		}
		findings = append(findings, f)
	}
	return findings, rows.Err()
}

// GetScanTranscript returns the decompressed transcript bytes, verified
// against the run row's digest. It backs audit inspection and tests only —
// the findings API never serves transcripts.
func (r *Repository) GetScanTranscript(ctx context.Context, tenant Tenant, runID string) ([]byte, error) {
	objects, err := r.objectStore()
	if err != nil {
		return nil, err
	}
	tx, _, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	run, err := readScanRun(ctx, tx, r, tenant, runID)
	if err != nil {
		return nil, err
	}
	objectKey, err := readScanTranscriptLocator(ctx, tx, r, tenant, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("scan transcript for %s: expired or never stored", runID)
	}
	if err != nil {
		return nil, err
	}
	payload, err := objects.Get(ctx, objectKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrObjectStorageUnavailable, err)
	}
	if r.ring != nil {
		payload, err = r.ring.Decrypt(payload, payloadAAD(tenant, "transcript", runID))
		if err != nil {
			return nil, fmt.Errorf("unseal scan transcript: %w", err)
		}
	}
	zr, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("decompress scan transcript: %w", err)
	}
	// Bounded: the digest can only be checked after decompression, so an
	// unbounded read would let a swapped object exhaust memory first.
	transcript, err := io.ReadAll(io.LimitReader(zr, maxScanTranscriptBytes+1))
	if err != nil {
		return nil, fmt.Errorf("decompress scan transcript: %w", err)
	}
	if int64(len(transcript)) > maxScanTranscriptBytes {
		return nil, fmt.Errorf("scan transcript for %s exceeds %d bytes", runID, maxScanTranscriptBytes)
	}
	sum := sha256.Sum256(transcript)
	if hex.EncodeToString(sum[:]) != run.TranscriptDigest {
		return nil, fmt.Errorf("scan transcript for %s: digest mismatch", runID)
	}
	return transcript, nil
}

// readScanTranscriptLocator returns the verified object key for a run.
func readScanTranscriptLocator(ctx context.Context, tx *sql.Tx, r *Repository, tenant Tenant, runID string) (string, error) {
	var objectKey string
	var expiresAt time.Time
	var mac []byte
	err := tx.QueryRowContext(ctx, `
		SELECT object_key, expires_at, integrity_mac FROM scan_transcripts
		WHERE organization_id = $1 AND project_id = $2 AND run_id = $3`,
		tenant.OrganizationID, tenant.ProjectID, runID,
	).Scan(&objectKey, &expiresAt, &mac)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		return "", fmt.Errorf("read scan transcript locator: %w", err)
	}
	if err := r.verifyRowMAC("scan transcript "+runID, mac,
		scanTranscriptMACMessage(tenant, runID, objectKey, scanWriteTime(expiresAt))); err != nil {
		return "", err
	}
	return objectKey, nil
}

// ExpireScanTranscripts deletes transcript objects and locators past the
// seven-day window, bounded per call. The run row keeps its digest forever.
func (r *Repository) ExpireScanTranscripts(ctx context.Context, tenant Tenant, now time.Time, limit int) (int, error) {
	objects, err := r.objectStore()
	if err != nil {
		return 0, err
	}
	tx, _, err := r.begin(ctx, tenant)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
		SELECT run_id, object_key, expires_at, integrity_mac FROM scan_transcripts
		WHERE organization_id = $1 AND project_id = $2 AND expires_at <= $3
		ORDER BY expires_at
		LIMIT $4
		FOR UPDATE`,
		tenant.OrganizationID, tenant.ProjectID, now, limit)
	if err != nil {
		return 0, fmt.Errorf("list expired transcripts: %w", err)
	}
	type victim struct{ runID, objectKey string }
	var victims []victim
	for rows.Next() {
		var v victim
		var expiresAt time.Time
		var mac []byte
		if err := rows.Scan(&v.runID, &v.objectKey, &expiresAt, &mac); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("expired transcript row: %w", err)
		}
		// Verified before the key is used as a delete target: an
		// unauthenticated locator would let one tenant's expiry aim at
		// another tenant's object.
		if err := r.verifyRowMAC("scan transcript "+v.runID, mac,
			scanTranscriptMACMessage(tenant, v.runID, v.objectKey, scanWriteTime(expiresAt))); err != nil {
			_ = rows.Close()
			return 0, err
		}
		victims = append(victims, v)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// Locators first, committed, then objects: a crash in between leaves an
	// unreferenced object (content-addressed, harmless, re-collectable),
	// whereas the reverse order leaves a locator promising bytes that are
	// already gone.
	for _, v := range victims {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM scan_transcripts
			WHERE organization_id = $1 AND project_id = $2 AND run_id = $3`,
			tenant.OrganizationID, tenant.ProjectID, v.runID); err != nil {
			return 0, fmt.Errorf("delete transcript locator: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	for _, v := range victims {
		if err := objects.Delete(ctx, v.objectKey); err != nil {
			return 0, err
		}
	}
	return len(victims), nil
}

// PurgeSupersededScanRuns deletes run history older than the cutoff, bounded
// per call, never touching any run a build's pointers still reference.
func (r *Repository) PurgeSupersededScanRuns(ctx context.Context, tenant Tenant, cutoff time.Time, limit int) (int, error) {
	tx, _, err := r.begin(ctx, tenant)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		DELETE FROM scan_runs
		WHERE (organization_id, project_id, id) IN (
			SELECT sr.organization_id, sr.project_id, sr.id FROM scan_runs sr
			WHERE sr.organization_id = $1 AND sr.project_id = $2
			  AND sr.created_at < $3
			  AND NOT EXISTS (
				SELECT 1 FROM build_scan_state bss
				WHERE bss.organization_id = sr.organization_id
				  AND bss.project_id = sr.project_id
				  AND (bss.current_findings_run_id = sr.id OR bss.latest_attempt_run_id = sr.id)
			  )
			ORDER BY sr.created_at
			LIMIT $4
		)`,
		tenant.OrganizationID, tenant.ProjectID, cutoff, limit)
	if err != nil {
		return 0, fmt.Errorf("purge superseded scan runs: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(deleted), tx.Commit()
}
