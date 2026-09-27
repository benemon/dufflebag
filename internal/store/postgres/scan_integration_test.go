//go:build integration

package postgres_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/keyring"
	"github.com/benemon/dufflebag/internal/scan"
	"github.com/benemon/dufflebag/internal/store/objectstore"
	store "github.com/benemon/dufflebag/internal/store/postgres"
)

// seedScanParents inserts the FK ancestry a scan run needs: bucket, version,
// build, sbom and one sbom_packages row, all under the tenant.
func seedScanParents(t *testing.T, db *sql.DB, org, project, suffix string) (buildID, sbomID string) {
	t.Helper()
	ctx := context.Background()
	tx, err := store.BeginTenant(ctx, db, org, project, "")
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
		{`INSERT INTO builds (organization_id, project_id, id, bucket_id, version_id, component_type, status, platform, metadata_seen, created_at, updated_at)
			VALUES ($1,$2,$3,'scanbucket-'||$4,'scanversion-'||$4,'docker','done','docker',true,$5,$5)`, []any{org, project, buildID, suffix, now}},
		{`INSERT INTO sboms (organization_id, project_id, id, bucket_id, build_id, name, format, object_key, created_at)
			VALUES ($1,$2,$3,'scanbucket-'||$5,$4,'sbom.spdx.json','SPDX','scan-key-'||$5,$6)`, []any{org, project, sbomID, buildID, suffix, now}},
		{`INSERT INTO sbom_packages (organization_id, project_id, bucket_id, sbom_id, name, version, purl)
			VALUES ($1,$2,'scanbucket-'||$4,$3,'busybox','1.36.1-r0','pkg:apk/alpine/busybox@1.36.1-r0')`, []any{org, project, sbomID, suffix}},
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

// scanRunFixture binds the run to the build's inventory as it stands now, the
// way a claim does in production; a test that changes the inventory afterwards
// is testing the refusal.
func scanRunFixture(t *testing.T, db *sql.DB, id, buildID string, sequence int64, status string, at time.Time, transcript []byte) store.ScanRun {
	t.Helper()
	sum := sha256.Sum256(transcript)
	return store.ScanRun{
		ID: id, BuildID: buildID, RunSequence: sequence, Status: status,
		Adapter: "osv", Engine: "https://api.osv.dev", DatabaseRevision: "unreported",
		ObservedAt: at, TranscriptDigest: hex.EncodeToString(sum[:]),
		Coverage:  scan.Coverage{Submitted: 1},
		CreatedAt: at, InventoryDigest: inventoryDigestOf(t, db, buildID),
	}
}

func inventoryDigestOf(t *testing.T, db *sql.DB, buildID string) string {
	t.Helper()
	tx, err := store.BeginTenant(context.Background(), db, orgA, projectA, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	digest, err := store.InventoryDigestTx(context.Background(), tx, buildID)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func seedScanSiblingBuild(t *testing.T, db *sql.DB, suffix, buildID, sbomID string) {
	t.Helper()
	tx, err := store.BeginTenant(context.Background(), db, orgA, projectA, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO builds (organization_id, project_id, id, bucket_id, version_id, component_type, status, platform, metadata_seen, created_at, updated_at)
			VALUES ($1,$2,$3,'scanbucket-'||$4,'scanversion-'||$4,'docker.sibling','done','linux',true,$5,$5)`, []any{orgA, projectA, buildID, suffix, now}},
		{`INSERT INTO sboms (organization_id, project_id, id, bucket_id, build_id, name, format, object_key, created_at, parse_status)
			VALUES ($1,$2,$3,'scanbucket-'||$5,$4,'sbom.spdx.json','SPDX','scan-key-'||$5,$6,'parsed')`, []any{orgA, projectA, sbomID, buildID, suffix, now}},
		{`INSERT INTO sbom_packages (organization_id, project_id, bucket_id, sbom_id, name, version, purl)
			VALUES ($1,$2,'scanbucket-'||$4,$3,'busybox','1.36.1-r0','pkg:apk/alpine/busybox@1.36.1-r0')`, []any{orgA, projectA, sbomID, suffix}},
	} {
		if _, err := tx.ExecContext(context.Background(), statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestPagedBuildPackagesAndFindings(t *testing.T) {
	db, databaseURL, cleanup := openTestDatabase(t)
	defer cleanup()
	_, objects := openTestObjectStore(t)
	repository := store.NewRepositoryWithObjectStore(db, objects)
	repository.SetKeyring(testRing(t))
	tenant := store.ParseTenant(orgA, projectA)
	ctx := context.Background()
	at := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	bucketID := registry.NewID(at).String()
	versionID := registry.NewID(at.Add(time.Millisecond)).String()
	buildID := registry.NewID(at.Add(2 * time.Millisecond)).String()
	sbomA := registry.NewID(at.Add(3 * time.Millisecond)).String()
	sbomB := registry.NewID(at.Add(4 * time.Millisecond)).String()
	const bucketName = "package-pages"
	const fingerprint = "fp-package-pages"

	tx, err := store.BeginTenant(ctx, db, orgA, projectA, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO buckets (organization_id, project_id, id, name, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$5)`, []any{orgA, projectA, bucketID, bucketName, at}},
		{`INSERT INTO versions (organization_id, project_id, id, bucket_id, fingerprint, template_type, complete, sequence, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,'HCL2',true,1,$6,$6)`, []any{orgA, projectA, versionID, bucketID, fingerprint, at}},
		{`INSERT INTO builds (organization_id, project_id, id, bucket_id, version_id, component_type, status, platform, metadata_seen, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,'docker','done','linux',true,$6,$6)`, []any{orgA, projectA, buildID, bucketID, versionID, at}},
		{`INSERT INTO sboms (organization_id, project_id, id, bucket_id, build_id, name, format, object_key, parse_status, created_at)
			VALUES ($1,$2,$3,$4,$5,'a-sbom','SPDX','page-a','parsed',$6)`, []any{orgA, projectA, sbomA, bucketID, buildID, at}},
		{`INSERT INTO sboms (organization_id, project_id, id, bucket_id, build_id, name, format, object_key, parse_status, created_at)
			VALUES ($1,$2,$3,$4,$5,'z-sbom','SPDX','page-b','parsed',$6)`, []any{orgA, projectA, sbomB, bucketID, buildID, at}},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 250; i++ {
		name := fmt.Sprintf("package-%03d", i)
		version := "odd"
		if i%2 == 0 {
			version = "even"
		}
		purl := fmt.Sprintf("pkg:generic/%s@%s", name, version)
		for _, sbomID := range []string{sbomA, sbomB} {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO sbom_packages (organization_id, project_id, bucket_id, sbom_id, name, version, purl)
				VALUES ($1,$2,$3,$4,$5,$6,$7)
			`, orgA, projectA, bucketID, sbomID, name, version, purl); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	sequence, err := repository.AllocateScanRunSequence(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	transcript := []byte("paged-package-findings")
	run := scanRunFixture(t, db, "run-package-pages", buildID, sequence, store.ScanRunSucceeded, at, transcript)
	finding := func(index int) scan.Finding {
		name := fmt.Sprintf("package-%03d", index)
		version := "odd"
		if index%2 == 0 {
			version = "even"
		}
		value := scanFindingFixture(sbomA, fmt.Sprintf("CVE-2026-%04d", index), at)
		value.Package = scan.Package{
			SBOMID: sbomA, Name: name, Version: version,
			Purl: fmt.Sprintf("pkg:generic/%s@%s", name, version),
		}
		return value
	}
	if err := repository.RecordScanRun(ctx, tenant, run,
		[]scan.Finding{finding(99), finding(100)}, transcript); err != nil {
		t.Fatal(err)
	}

	total, unparseable, err := repository.CountBuildPackages(
		ctx, tenant, bucketName, fingerprint, buildID, store.BuildPackageFilter{})
	if err != nil || len(unparseable) != 0 || total != 250 {
		t.Fatalf("CountBuildPackages = %d, unparseable %#v, %v", total, unparseable, err)
	}
	all, err := repository.ListBuildPackages(ctx, tenant, buildID, store.BuildPackageFilter{}, 0, total)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("filters match the unpaged projection", func(t *testing.T) {
		filters := []store.BuildPackageFilter{
			{Name: "package-042"},
			{NamePrefix: "package-1"},
			{Version: "even"},
		}
		for _, filter := range filters {
			want := make([]store.ReportedPackage, 0)
			for _, pkg := range all {
				if filter.Name != "" && pkg.Name != filter.Name ||
					filter.NamePrefix != "" && !strings.HasPrefix(pkg.Name, filter.NamePrefix) ||
					filter.Version != "" && pkg.Version != filter.Version {
					continue
				}
				want = append(want, pkg)
			}
			count, broken, err := repository.CountBuildPackages(
				ctx, tenant, bucketName, fingerprint, buildID, filter)
			if err != nil || len(broken) != 0 || count != len(want) {
				t.Fatalf("filter %#v count = %d, unparseable %#v, %v; want %d", filter, count, broken, err, len(want))
			}
			got, err := repository.ListBuildPackages(ctx, tenant, buildID, filter, 0, count)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("filter %#v packages differ from unpaged projection", filter)
			}
		}
	})

	t.Run("page SBOMs and findings match the unpaged identity range", func(t *testing.T) {
		page, err := repository.ListBuildPackages(ctx, tenant, buildID, store.BuildPackageFilter{}, 95, 10)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(page, all[95:105]) {
			t.Fatalf("page packages = %#v, want %#v", page, all[95:105])
		}
		fullFindings, err := repository.ListScanFindings(ctx, tenant, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		identities := make(map[string]bool, len(page))
		for _, pkg := range page {
			identities[pkg.Name+"\x00"+pkg.Version+"\x00"+pkg.Purl] = true
		}
		want := make([]store.StoredFinding, 0)
		for _, stored := range fullFindings {
			if identities[stored.Package.Name+"\x00"+stored.Package.Version+"\x00"+stored.Package.Purl] {
				want = append(want, stored)
			}
		}
		got, err := repository.ListScanFindingsForPackages(ctx, tenant, run.ID, page)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("page findings = %#v, want %#v", got, want)
		}
	})

	t.Run("tampered second page does not poison the first", func(t *testing.T) {
		adminURL, err := url.Parse(databaseURL)
		if err != nil {
			t.Fatal(err)
		}
		adminURL.User = url.UserPassword("postgres", "postgres")
		admin, err := sql.Open("pgx", adminURL.String())
		if err != nil {
			t.Fatal(err)
		}
		defer admin.Close()
		for _, statement := range []string{
			`ALTER TABLE scan_findings DISABLE TRIGGER scan_findings_immutable`,
			`UPDATE scan_findings SET derived_severity = 'negligible' WHERE run_id = 'run-package-pages' AND package_name = 'package-100'`,
			`ALTER TABLE scan_findings ENABLE TRIGGER scan_findings_immutable`,
		} {
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatal(err)
			}
		}

		pageOne, err := repository.ListBuildPackages(ctx, tenant, buildID, store.BuildPackageFilter{}, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repository.ListScanFindingsForPackages(ctx, tenant, run.ID, pageOne); err != nil {
			t.Fatalf("page one read page two's tampered finding: %v", err)
		}
		pageTwo, err := repository.ListBuildPackages(ctx, tenant, buildID, store.BuildPackageFilter{}, 100, 100)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repository.ListScanFindingsForPackages(ctx, tenant, run.ID, pageTwo); !errors.Is(err, keyring.ErrMAC) {
			t.Fatalf("page two tampered finding error = %v, want %v", err, keyring.ErrMAC)
		}
	})
}

func TestFindingsSummaries(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	_, objects := openTestObjectStore(t)
	repository := store.NewRepositoryWithObjectStore(db, objects)
	repository.SetKeyring(testRing(t))
	tenant := store.ParseTenant(orgA, projectA)
	ctx := context.Background()
	base := time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)
	buildA, sbomA := seedScanParents(t, db, orgA, projectA, "summaries")
	buildB, sbomB := "scanbuild-summaries-b", "scansbom-summaries-b"
	seedScanSiblingBuild(t, db, "summaries", buildB, sbomB)
	transcript := []byte("findings-summary-fixture")
	allocate := func() int64 {
		t.Helper()
		sequence, err := repository.AllocateScanRunSequence(ctx, tenant)
		if err != nil {
			t.Fatal(err)
		}
		return sequence
	}

	runA := scanRunFixture(t, db, "summary-run-a", buildA, allocate(), store.ScanRunSucceeded, base, transcript)
	findingsA := []scan.Finding{
		scanFindingFixture(sbomA, "ALPINE-CVE-2022-48174", base),
		scanFindingFixture(sbomA, "ALPINE-CVE-2023-42363", base),
	}
	if err := repository.RecordScanRun(ctx, tenant, runA, findingsA, transcript); err != nil {
		t.Fatal(err)
	}
	summary, err := repository.GetVersionFindingsSummary(ctx, tenant, "scan-summaries", "fp-scan-summaries")
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Builds) != 2 {
		t.Fatalf("build summaries = %#v, want two builds", summary.Builds)
	}
	var buildSummary *store.BuildFindingsSummary
	for _, build := range summary.Builds {
		if build.BuildID == buildA {
			buildSummary = build.Summary
		}
		if build.BuildID == buildB && build.Summary != nil {
			t.Fatalf("unscanned build summary = %#v, want absent", build.Summary)
		}
	}
	if buildSummary == nil {
		t.Fatal("scanned build summary is absent")
	}
	if buildSummary.Findings != 2 || buildSummary.AffectedPackages != 1 || buildSummary.Scanned != 1 {
		t.Fatalf("build summary = %#v, want findings=2 affected=1 scanned=1", buildSummary)
	}

	runB := scanRunFixture(t, db, "summary-run-b", buildB, allocate(), store.ScanRunSucceeded, base.Add(time.Minute), transcript)
	if err := repository.RecordScanRun(ctx, tenant, runB,
		[]scan.Finding{scanFindingFixture(sbomB, "ALPINE-CVE-2022-48174", base)}, transcript); err != nil {
		t.Fatal(err)
	}
	summary, err = repository.GetVersionFindingsSummary(ctx, tenant, "scan-summaries", "fp-scan-summaries")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Version == nil || summary.Version.Findings != 2 ||
		summary.Version.AffectedPackages != 1 || summary.Version.BuildsSummarised != 2 {
		t.Fatalf("version summary = %#v, want two cross-build-deduplicated findings on one package across two builds", summary.Version)
	}

	staleSequence, newerSequence := allocate(), allocate()
	newer := scanRunFixture(t, db, "summary-run-newer", buildA, newerSequence, store.ScanRunSucceeded, base.Add(3*time.Minute), transcript)
	if err := repository.RecordScanRun(ctx, tenant, newer, findingsA, transcript); err != nil {
		t.Fatal(err)
	}
	stale := scanRunFixture(t, db, "summary-run-stale", buildA, staleSequence, store.ScanRunSucceeded, base.Add(2*time.Minute), transcript)
	if err := repository.RecordScanRun(ctx, tenant, stale, nil, transcript); err != nil {
		t.Fatal(err)
	}
	summary, err = repository.GetVersionFindingsSummary(ctx, tenant, "scan-summaries", "fp-scan-summaries")
	if err != nil {
		t.Fatal(err)
	}
	foundBuildA := false
	for _, build := range summary.Builds {
		if build.BuildID == buildA && (build.Summary == nil || build.Summary.RunID != newer.ID || build.Summary.Findings != 2) {
			t.Fatalf("stale completion rewrote build summary = %#v, want current run %s with two findings", build.Summary, newer.ID)
		}
		foundBuildA = foundBuildA || build.BuildID == buildA
	}
	if !foundBuildA {
		t.Fatal("current build missing from version summary")
	}

	tx, err := store.BeginTenant(ctx, db, orgA, projectA, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE build_findings_summary
		SET counts = jsonb_set(counts, '{critical}', '3')
		WHERE build_id = $1`, buildA); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetVersionFindingsSummary(ctx, tenant, "scan-summaries", "fp-scan-summaries"); err == nil || !strings.Contains(err.Error(), "build findings summary") {
		t.Fatalf("tampered summary read error = %v, want fail-closed build findings summary MAC error", err)
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
		run2 := scanRunFixture(t, db, "run-rt-2", buildID, seq2, store.ScanRunSucceeded, base.Add(time.Hour), transcript)
		if err := repo.RecordScanRun(ctx, tenant, run2, []scan.Finding{scanFindingFixture(sbomID, "ALPINE-CVE-2022-48174", base.Add(time.Hour))}, transcript); err != nil {
			t.Fatal(err)
		}
		// The older completion arrives late and must not advance anything.
		run1 := scanRunFixture(t, db, "run-rt-1", buildID, seq1, store.ScanRunSucceeded, base, transcript)
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
		run3 := scanRunFixture(t, db, "run-rt-3", buildID, seq3, store.ScanRunFailed, base.Add(2*time.Hour), transcript)
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
		run4 := scanRunFixture(t, db, "run-rt-4", buildID, seq4, store.ScanRunSucceeded, base.Add(3*time.Hour), transcript)
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
		run := scanRunFixture(t, db, "run-teeth-1", buildID, allocate(), store.ScanRunSucceeded, base, transcript)
		if err := repo.RecordScanRun(ctx, tenant, run, []scan.Finding{scanFindingFixture(sbomID, "ALPINE-CVE-2022-48174", base)}, transcript); err != nil {
			t.Fatal(err)
		}
		tx, err := store.BeginTenant(ctx, db, orgA, projectA, "")
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

		tx, err = store.BeginTenant(ctx, db, orgA, projectA, "")
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
		badRun := scanRunFixture(t, db, "run-teeth-2", buildID, allocate(), store.ScanRunSucceeded, base, transcript)
		bad := scanFindingFixture(sbomID, "ALPINE-CVE-2022-48174", base)
		bad.Package.Version = "no-such-version"
		if err := repo.RecordScanRun(ctx, tenant, badRun, []scan.Finding{bad}, transcript); err == nil {
			t.Fatal("finding with an incomplete package identity was accepted")
		}
		// A finding against another tenant's SBOM fails inside this tenant.
		otherRun := scanRunFixture(t, db, "run-teeth-3", buildID, allocate(), store.ScanRunSucceeded, base, transcript)
		foreign := scanFindingFixture("not-my-sbom", "ALPINE-CVE-2022-48174", base)
		if err := repo.RecordScanRun(ctx, tenant, otherRun, []scan.Finding{foreign}, transcript); err == nil {
			t.Fatal("finding against a foreign sbom id was accepted")
		}
	})

	t.Run("transcript expiry retains digest", func(t *testing.T) {
		buildID, _ := seedScanParents(t, db, orgA, projectA, "expiry")
		transcript := []byte("transcript-expiry")
		run := scanRunFixture(t, db, "run-exp-1", buildID, allocate(), store.ScanRunSucceeded, base, transcript)
		if err := repo.RecordScanRun(ctx, tenant, run, nil, transcript); err != nil {
			t.Fatal(err)
		}
		var objectKey string
		tx, _ := store.BeginTenant(ctx, db, orgA, projectA, "")
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
			scanRunFixture(t, db, "run-ret-1", buildID, seq[0], store.ScanRunSucceeded, base, transcript),
			scanRunFixture(t, db, "run-ret-2", buildID, seq[1], store.ScanRunSucceeded, base.Add(time.Hour), transcript),
			scanRunFixture(t, db, "run-ret-3", buildID, seq[2], store.ScanRunFailed, base.Add(2*time.Hour), transcript),
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
		run := scanRunFixture(t, db, "run-putfail-1", buildID, allocate(), store.ScanRunSucceeded, base, transcript)
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
		run := scanRunFixture(t, db, "run-sealed-1", buildID, allocate(), store.ScanRunSucceeded, base, transcript)
		if err := repo.RecordScanRun(ctx, tenant, run, nil, transcript); err != nil {
			t.Fatal(err)
		}
		var objectKey string
		tx, _ := store.BeginTenant(ctx, db, orgA, projectA, "")
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
		run := scanRunFixture(t, db, "run-digest-1", buildID, allocate(), store.ScanRunSucceeded, base, []byte("declared"))
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
		newer := scanRunFixture(t, db, "run-fs-2", buildID, seq2, store.ScanRunSucceeded, base.Add(time.Hour), transcript)
		if err := repo.RecordScanRun(ctx, tenant, newer, []scan.Finding{scanFindingFixture(sbomID, "ALPINE-CVE-2022-48174", base.Add(time.Hour))}, transcript); err != nil {
			t.Fatal(err)
		}
		older := scanRunFixture(t, db, "run-fs-1", buildID, seq1, store.ScanRunSucceeded, base, transcript)
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
		run := scanRunFixture(t, db, "run-delim-1", buildID, allocate(), store.ScanRunFailed, base, transcript)
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
		run := scanRunFixture(t, db, "run-loc-1", buildID, allocate(), store.ScanRunSucceeded, base, transcript)
		if err := repo.RecordScanRun(ctx, tenant, run, nil, transcript); err != nil {
			t.Fatal(err)
		}
		var victimKey string
		tx, _ := store.BeginTenant(ctx, db, orgA, projectA, "")
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
	run := scanRunFixture(t, db, "run-tamper-1", buildID, sequence, store.ScanRunSucceeded, base, transcript)
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

// Anything that changes a build or its package list leaves its summary
// recomputed or withdrawn, never describing an inventory it no longer has.
func TestFindingsSummariesFollowBuildLifecycle(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	_, objects := openTestObjectStore(t)
	repository := store.NewRepositoryWithObjectStore(db, objects)
	repository.SetKeyring(testRing(t))
	tenant := store.ParseTenant(orgA, projectA)
	ctx := context.Background()
	at := time.Date(2026, 9, 22, 11, 0, 0, 0, time.UTC)
	const bucket, fingerprint = "lifecycle", "fp-lifecycle"

	if _, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID: registry.NewID(at), Name: bucket, Labels: map[string]string{}, CreatedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	version, err := registry.NewVersion(registry.NewID(at.Add(time.Second)), bucket, fingerprint, registry.TemplateHCL2, at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateVersion(ctx, tenant, version); err != nil {
		t.Fatal(err)
	}
	upload := func(build *store.StoredBuild, offset time.Duration) string {
		t.Helper()
		sbom, err := repository.UploadSbom(ctx, tenant, bucket, fingerprint, build.ID.String(), store.Sbom{
			ID: registry.NewID(at.Add(offset)), Name: "manifest", Format: "CYCLONEDX",
			CompressedData: compressIntegrationSBOM(t, `{"bomFormat":"CycloneDX","specVersion":"1.6","components":[
				{"name":"openssl","version":"3.0.11","purl":"pkg:rpm/openssl@3.0.11"}]}`),
			CreatedAt: at.Add(offset),
		})
		if err != nil {
			t.Fatalf("UploadSbom: %v", err)
		}
		return sbom.ID.String()
	}
	setStatus := func(build *store.StoredBuild, status registry.BuildStatus, offset time.Duration) {
		t.Helper()
		build.Status = status
		build.MetadataSeen = true
		if _, err := repository.UpdateBuild(ctx, tenant, bucket, fingerprint, *build, testVersionName, at.Add(offset)); err != nil {
			t.Fatalf("UpdateBuild %s: %v", status, err)
		}
	}
	pendingReasons := func() []string {
		t.Helper()
		tx, err := store.BeginTenant(ctx, db, orgA, projectA, "")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		rows, err := tx.QueryContext(ctx, `SELECT reason FROM pending_scans ORDER BY reason`)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		var reasons []string
		for rows.Next() {
			var reason string
			if err := rows.Scan(&reason); err != nil {
				t.Fatal(err)
			}
			reasons = append(reasons, reason)
		}
		return reasons
	}

	type fixture struct {
		build  *store.StoredBuild
		sbomID string
	}
	var builds []fixture
	for index, component := range []string{"docker.one", "docker.two", "docker.three"} {
		offset := time.Duration(10*(index+1)) * time.Second
		build, err := repository.CreateBuild(ctx, tenant, bucket, fingerprint, registry.TemplateHCL2, store.StoredBuild{
			Build: registry.Build{
				ID: registry.NewID(at.Add(offset)), ComponentType: component,
				Status: registry.BuildRunning, Platform: "linux",
			},
			Labels: map[string]string{}, CreatedAt: at.Add(offset),
		}, testVersionName)
		if err != nil {
			t.Fatalf("CreateBuild %s: %v", component, err)
		}
		builds = append(builds, fixture{build, upload(build, offset+time.Second)})
	}
	setStatus(builds[0].build, registry.BuildDone, 40*time.Second)
	if reasons := pendingReasons(); len(reasons) != 0 {
		t.Fatalf("completion into an incomplete version queued scans %v, want none", reasons)
	}
	setStatus(builds[1].build, registry.BuildDone, 41*time.Second)
	setStatus(builds[2].build, registry.BuildDone, 42*time.Second)

	transcript := []byte("findings-summary-lifecycle")
	for index, advisory := range []string{"CVE-2026-0001", "CVE-2026-0002", "CVE-2026-0003"} {
		sequence, err := repository.AllocateScanRunSequence(ctx, tenant)
		if err != nil {
			t.Fatal(err)
		}
		finding := scanFindingFixture(builds[index].sbomID, advisory, at)
		finding.Package = scan.Package{SBOMID: builds[index].sbomID, Name: "openssl", Version: "3.0.11", Purl: "pkg:rpm/openssl@3.0.11"}
		run := scanRunFixture(t, db, fmt.Sprintf("lifecycle-run-%d", index), builds[index].build.ID.String(), sequence,
			store.ScanRunSucceeded, at.Add(2*time.Minute), transcript)
		if err := repository.RecordScanRun(ctx, tenant, run, []scan.Finding{finding}, transcript); err != nil {
			t.Fatal(err)
		}
	}
	summary := func() *store.VersionFindingsSummaryResult {
		t.Helper()
		result, err := repository.GetVersionFindingsSummary(ctx, tenant, bucket, fingerprint)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	wantVersion := func(step string, findings, summarised int) {
		t.Helper()
		got := summary().Version
		if got == nil || got.Findings != findings || got.BuildsSummarised != summarised {
			t.Fatalf("%s: version summary = %#v, want findings=%d builds_summarised=%d", step, got, findings, summarised)
		}
	}
	wantVersion("all scanned", 3, 3)

	if err := repository.DeleteBuild(ctx, tenant, bucket, fingerprint, builds[2].build.ID.String()); err != nil {
		t.Fatal(err)
	}
	wantVersion("build deleted", 2, 2)

	reopened := builds[0].build
	setStatus(reopened, registry.BuildRunning, 3*time.Minute)
	wantVersion("build reopened", 1, 1)
	for _, build := range summary().Builds {
		if build.BuildID == reopened.ID.String() && build.Summary != nil {
			t.Fatalf("reopened build summary = %#v, want absent", build.Summary)
		}
	}
	reuploaded := upload(reopened, 3*time.Minute+time.Second)
	if state, err := repository.GetBuildScanState(ctx, tenant, reopened.ID.String()); err != nil || state == nil || state.CurrentFindingsRunID != "" {
		t.Fatalf("scan state after withdrawal = %#v, %v; want the current run cleared", state, err)
	}

	tx, err := store.BeginTenant(ctx, db, orgA, projectA, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM pending_scans`); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	setStatus(reopened, registry.BuildDone, 4*time.Minute)
	if reasons := pendingReasons(); len(reasons) != 1 || reasons[0] != "build_completed" {
		t.Fatalf("pending scans after an eligible build completed = %v, want [build_completed]", reasons)
	}

	sequence, err := repository.AllocateScanRunSequence(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	rescan := scanFindingFixture(reuploaded, "CVE-2026-0001", at)
	rescan.Package = scan.Package{SBOMID: reuploaded, Name: "openssl", Version: "3.0.11", Purl: "pkg:rpm/openssl@3.0.11"}
	if err := repository.RecordScanRun(ctx, tenant,
		scanRunFixture(t, db, "lifecycle-rescan", reopened.ID.String(), sequence, store.ScanRunSucceeded, at.Add(5*time.Minute), transcript),
		[]scan.Finding{rescan}, transcript); err != nil {
		t.Fatal(err)
	}
	wantVersion("rescanned", 2, 2)
	// No production caller replaces a done build's SBOM; the repository allows
	// it, and this case pins that the withdrawal fires if one ever does.
	upload(reopened, 6*time.Minute)
	wantVersion("sbom replaced on a done build", 1, 1)

	if err := repository.DeleteBuild(ctx, tenant, bucket, fingerprint, builds[1].build.ID.String()); err != nil {
		t.Fatal(err)
	}
	if got := summary().Version; got != nil {
		t.Fatalf("version summary with no scanned build left = %#v, want absent", got)
	}
}

// TestGetBuildFindings pins the native per-build read: one advisory per
// identifier with a package entry per reporting SBOM, an existing build
// without a scan answering scanned=false rather than not-found, unparseable
// inventories reported as such, MAC failure surfacing, and tenancy.
func TestGetBuildFindings(t *testing.T) {
	db, adminURL, cleanup := openTestDatabase(t)
	defer cleanup()
	_, objects := openTestObjectStore(t)
	repo := store.NewRepositoryWithObjectStore(db, objects)
	repo.SetKeyring(testRing(t))
	tenant := store.ParseTenant(orgA, projectA)
	ctx := context.Background()
	base := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	allocate := func() int64 {
		t.Helper()
		sequence, err := repo.AllocateScanRunSequence(ctx, tenant)
		if err != nil {
			t.Fatal(err)
		}
		return sequence
	}
	buildID, sbomA := seedScanParents(t, db, orgA, projectA, "native")
	sbomB := "scansbom-native-b"
	tx, err := store.BeginTenant(ctx, db, orgA, projectA, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO sboms (organization_id, project_id, id, bucket_id, build_id, name, format, object_key, created_at, parse_status)
			VALUES ($1,$2,$3,'scanbucket-native',$4,'second.spdx.json','SPDX','scan-key-native-b',$5,'parsed')`, []any{orgA, projectA, sbomB, buildID, base}},
		{`INSERT INTO sbom_packages (organization_id, project_id, bucket_id, sbom_id, name, version, purl)
			VALUES ($1,$2,'scanbucket-native',$3,'busybox','1.36.1-r0','pkg:apk/alpine/busybox@1.36.1-r0')`, []any{orgA, projectA, sbomB}},
	} {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	// seedScanParents leaves the SBOM pending, which a read finalises as
	// unparseable; this test wants a parsed inventory until it says otherwise.
	if _, err := tx.ExecContext(ctx, `UPDATE sboms SET parse_status = 'parsed' WHERE id = $1`, sbomA); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	unscanned, err := repo.GetBuildFindings(ctx, tenant, "scan-native", "fp-scan-native", buildID)
	if err != nil {
		t.Fatal(err)
	}
	if unscanned.Scanned || unscanned.Run != nil || len(unscanned.Advisories) != 0 ||
		unscanned.Inventory != "parsed" || unscanned.PackagesTotal != 1 {
		t.Fatalf("unscanned build = %+v, want scanned=false, no run, no advisories, one distinct package", unscanned)
	}

	transcript := []byte("native-findings-transcript")
	run := scanRunFixture(t, db, "run-native-1", buildID, allocate(), store.ScanRunSucceeded, base, transcript)
	findings := []scan.Finding{
		scanFindingFixture(sbomA, "ALPINE-CVE-2022-48174", base),
		scanFindingFixture(sbomB, "ALPINE-CVE-2022-48174", base),
		scanFindingFixture(sbomA, "ALPINE-CVE-2023-42363", base),
	}
	if err := repo.RecordScanRun(ctx, tenant, run, findings, transcript); err != nil {
		t.Fatal(err)
	}
	scanned, err := repo.GetBuildFindings(ctx, tenant, "scan-native", "fp-scan-native", buildID)
	if err != nil {
		t.Fatal(err)
	}
	if !scanned.Scanned || scanned.Run == nil || scanned.Run.ID != "run-native-1" {
		t.Fatalf("scanned build = %+v, want run-native-1 current", scanned)
	}
	if len(scanned.Advisories) != 2 || scanned.PackagesAffected != 1 {
		t.Fatalf("advisories = %+v, affected = %d; want two advisories across one package", scanned.Advisories, scanned.PackagesAffected)
	}
	var shared *store.BuildAdvisory
	for i := range scanned.Advisories {
		if scanned.Advisories[i].Identifier == "ALPINE-CVE-2022-48174" {
			shared = &scanned.Advisories[i]
		}
	}
	if shared == nil || len(shared.Packages) != 2 || shared.Packages[0].SBOMID == shared.Packages[1].SBOMID {
		t.Fatalf("shared advisory = %+v, want one package entry per reporting SBOM", shared)
	}
	if shared.Packages[0].FixedVersion != "1.36.1-r2" || len(shared.FixedVersions) != 1 {
		t.Fatalf("fixed versions = %+v / %+v, want the single fixed version once", shared.Packages, shared.FixedVersions)
	}

	foreign := store.ParseTenant(orgB, projectB)
	if _, err := repo.GetBuildFindings(ctx, foreign, "scan-native", "fp-scan-native", buildID); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("foreign tenant read = %v, want not found", err)
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
	for _, statement := range []string{
		`ALTER TABLE scan_findings DISABLE TRIGGER scan_findings_immutable`,
		`UPDATE scan_findings SET derived_severity = 'negligible' WHERE run_id = 'run-native-1' AND advisory_id = 'ALPINE-CVE-2023-42363'`,
		`ALTER TABLE scan_findings ENABLE TRIGGER scan_findings_immutable`,
	} {
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.GetBuildFindings(ctx, tenant, "scan-native", "fp-scan-native", buildID); err == nil {
		t.Fatal("tampered finding row served as findings")
	}

	if _, err := admin.ExecContext(ctx,
		`UPDATE sboms SET parse_status = 'unparseable' WHERE id = $1`, sbomA); err != nil {
		t.Fatal(err)
	}
	broken, err := repo.GetBuildFindings(ctx, tenant, "scan-native", "fp-scan-native", buildID)
	if broken == nil && err != nil {
		// The tampered row still fails the run read; the inventory state is
		// reported before it, so restore the row to reach the assertion.
		if _, err := admin.ExecContext(ctx, `ALTER TABLE scan_findings DISABLE TRIGGER scan_findings_immutable`); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.ExecContext(ctx,
			`UPDATE scan_findings SET derived_severity = 'critical' WHERE run_id = 'run-native-1' AND advisory_id = 'ALPINE-CVE-2023-42363'`); err != nil {
			t.Fatal(err)
		}
		broken, err = repo.GetBuildFindings(ctx, tenant, "scan-native", "fp-scan-native", buildID)
	}
	if err != nil {
		t.Fatal(err)
	}
	if broken.Inventory != "unparseable" || broken.PackagesTotal != 0 {
		t.Fatalf("unparseable inventory = %+v, want inventory=unparseable with zero packages", broken)
	}
}

// TestScanRunBinding pins that a run becomes current only over the inventory
// it examined, on a build still done at completion.
func TestScanRunBinding(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	_, objects := openTestObjectStore(t)
	repo := store.NewRepositoryWithObjectStore(db, objects)
	repo.SetKeyring(testRing(t))
	tenant := store.ParseTenant(orgA, projectA)
	ctx := context.Background()
	base := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	allocate := func() int64 {
		t.Helper()
		sequence, err := repo.AllocateScanRunSequence(ctx, tenant)
		if err != nil {
			t.Fatal(err)
		}
		return sequence
	}
	exec := func(query string, args ...any) {
		t.Helper()
		tx, err := store.BeginTenant(ctx, db, orgA, projectA, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	transcript := []byte("binding-transcript")

	t.Run("a package added during the scan refuses the run", func(t *testing.T) {
		buildID, sbomID := seedScanParents(t, db, orgA, projectA, "bind-inv")
		run := scanRunFixture(t, db, "run-bind-1", buildID, allocate(), store.ScanRunSucceeded, base, transcript)
		exec(`INSERT INTO sbom_packages (organization_id, project_id, bucket_id, sbom_id, name, version, purl)
			VALUES ($1,$2,'scanbucket-bind-inv',$3,'openssl','3.0.11','pkg:apk/alpine/openssl@3.0.11')`, orgA, projectA, sbomID)
		err := repo.RecordScanRun(ctx, tenant, run, []scan.Finding{scanFindingFixture(sbomID, "ALPINE-CVE-2022-48174", base)}, transcript)
		if !errors.Is(err, store.ErrScanRunRefused) {
			t.Fatalf("RecordScanRun = %v, want ErrScanRunRefused", err)
		}
		recorded, err := repo.GetScanRun(ctx, tenant, "run-bind-1")
		if err != nil {
			t.Fatal(err)
		}
		if recorded.Status != store.ScanRunFailed || recorded.Error != "inventory changed during scan" {
			t.Fatalf("refused run = %+v, want failed with the inventory reason", recorded)
		}
		if findings, err := repo.ListScanFindings(ctx, tenant, "run-bind-1"); err != nil || len(findings) != 0 {
			t.Fatalf("findings = %v, %v; want none recorded for a refused run", findings, err)
		}
		state, err := repo.GetBuildScanState(ctx, tenant, buildID)
		if err != nil {
			t.Fatal(err)
		}
		if state == nil || state.CurrentFindingsRunID != "" || state.LatestAttemptRunID != "run-bind-1" {
			t.Fatalf("state = %+v, want latest attempt recorded and current empty", state)
		}
	})

	t.Run("a build no longer done at completion refuses the run", func(t *testing.T) {
		buildID, sbomID := seedScanParents(t, db, orgA, projectA, "bind-status")
		run := scanRunFixture(t, db, "run-bind-2", buildID, allocate(), store.ScanRunSucceeded, base, transcript)
		exec(`UPDATE builds SET status = 'running' WHERE id = $1`, buildID)
		err := repo.RecordScanRun(ctx, tenant, run, []scan.Finding{scanFindingFixture(sbomID, "ALPINE-CVE-2022-48174", base)}, transcript)
		if !errors.Is(err, store.ErrScanRunRefused) {
			t.Fatalf("RecordScanRun = %v, want ErrScanRunRefused", err)
		}
		recorded, err := repo.GetScanRun(ctx, tenant, "run-bind-2")
		if err != nil {
			t.Fatal(err)
		}
		if recorded.Error != "build not eligible at completion" {
			t.Fatalf("refused run = %+v, want the eligibility reason", recorded)
		}
	})

	t.Run("an unchanged inventory records and advances as before", func(t *testing.T) {
		buildID, sbomID := seedScanParents(t, db, orgA, projectA, "bind-same")
		run := scanRunFixture(t, db, "run-bind-3", buildID, allocate(), store.ScanRunSucceeded, base, transcript)
		if err := repo.RecordScanRun(ctx, tenant, run, []scan.Finding{scanFindingFixture(sbomID, "ALPINE-CVE-2022-48174", base)}, transcript); err != nil {
			t.Fatal(err)
		}
		state, err := repo.GetBuildScanState(ctx, tenant, buildID)
		if err != nil || state == nil || state.CurrentFindingsRunID != "run-bind-3" {
			t.Fatalf("state = %+v, %v; want run-bind-3 current", state, err)
		}
	})

	t.Run("a run without a digest is refused outright", func(t *testing.T) {
		buildID, _ := seedScanParents(t, db, orgA, projectA, "bind-none")
		run := scanRunFixture(t, db, "run-bind-4", buildID, allocate(), store.ScanRunSucceeded, base, transcript)
		run.InventoryDigest = ""
		if err := repo.RecordScanRun(ctx, tenant, run, nil, transcript); err == nil {
			t.Fatal("a run without an inventory digest was recorded")
		}
	})

	t.Run("the digest follows every identity field", func(t *testing.T) {
		one := scan.Inventory{Packages: []scan.Package{{SBOMID: "s", Name: "busybox", Version: "1.36.1-r0", Purl: "pkg:apk/alpine/busybox@1.36.1-r0"}}}
		same := scan.Inventory{Packages: []scan.Package{{SBOMID: "s", Name: "busybox", Version: "1.36.1-r0", Purl: "pkg:apk/alpine/busybox@1.36.1-r0"}}}
		bumped := scan.Inventory{Packages: []scan.Package{{SBOMID: "s", Name: "busybox", Version: "1.36.1-r1", Purl: "pkg:apk/alpine/busybox@1.36.1-r1"}}}
		if store.InventoryDigest(one) != store.InventoryDigest(same) {
			t.Fatal("identical inventories digest differently")
		}
		if store.InventoryDigest(one) == store.InventoryDigest(bumped) {
			t.Fatal("a version change did not change the digest")
		}
	})
}

// TestDeletionAndScanCompletionDoNotDeadlock races a bucket or version
// deletion against a scan completing on one of its builds. Either order is a
// valid history; an error from either side is the defect (duf-2myc).
func TestDeletionAndScanCompletionDoNotDeadlock(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	_, objects := openTestObjectStore(t)
	// seedScannerBuild writes rows without MACs, so this repository runs
	// without a keyring; integrity is covered elsewhere.
	repo := store.NewRepositoryWithObjectStore(db, objects)
	tenant := store.ParseTenant(orgA, projectA)
	ctx := context.Background()
	base := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	transcript := []byte("race-transcript")

	for i := range 30 {
		for _, target := range []string{"bucket", "version"} {
			suffix := fmt.Sprintf("race-%s-%d", target, i)
			seed := seedScannerBuild(t, db, suffix, false, false)
			buildID, sbomID := seed.buildID, seed.sbomID
			sequence, err := repo.AllocateScanRunSequence(ctx, tenant)
			if err != nil {
				t.Fatal(err)
			}
			run := scanRunFixture(t, db, "run-"+suffix, buildID, sequence, store.ScanRunSucceeded, base, transcript)
			findings := []scan.Finding{scanFindingFixture(sbomID, "ALPINE-CVE-2022-48174", base)}

			start := make(chan struct{})
			errs := make(chan error, 2)
			go func() {
				<-start
				errs <- repo.RecordScanRun(ctx, tenant, run, findings, transcript)
			}()
			go func() {
				<-start
				if target == "bucket" {
					errs <- repo.DeleteBucket(ctx, tenant, seed.bucketName)
					return
				}
				errs <- repo.DeleteVersion(ctx, tenant, seed.bucketName, seed.fingerprint, base)
			}()
			close(start)
			for range 2 {
				if err := <-errs; err != nil {
					t.Fatalf("%s: concurrent deletion and completion: %v", suffix, err)
				}
			}
		}
	}
}
