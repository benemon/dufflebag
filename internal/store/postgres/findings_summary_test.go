package postgres

import (
	"testing"

	"github.com/benemon/dufflebag/internal/scan"
)

func TestFindingsSummaryDeduplicatesAcrossBuilds(t *testing.T) {
	packageFor := func(sbomID string) scan.Package {
		return scan.Package{
			SBOMID: sbomID, Name: "openssl", Version: "3.0.0", Purl: "pkg:apk/alpine/openssl@3.0.0",
		}
	}
	buildA := []StoredFinding{
		{Finding: scan.Finding{Package: packageFor("sbom-a"), ID: "CVE-2026-0001", Severity: scan.SeverityLow}},
		{Finding: scan.Finding{Package: packageFor("sbom-z"), ID: "CVE-2026-0001", Severity: scan.SeverityCritical}},
	}
	buildB := []StoredFinding{
		{Finding: scan.Finding{Package: packageFor("sbom-b"), ID: "CVE-2026-0001", Severity: scan.SeverityHigh}},
	}

	first := summarizeBuildFindings(buildA)
	if first.Findings != 1 || first.AffectedPackages != 1 || first.Worst != scan.SeverityLow {
		t.Fatalf("first build summary = %#v, want one low finding on one package from the lowest SBOM id", first)
	}
	if first.Counts != (SeverityCounts{Low: 1}) {
		t.Fatalf("first build counts = %#v, want one low finding and zeroes elsewhere", first.Counts)
	}
	second := summarizeBuildFindings(buildB)
	if second.Findings != 1 || second.AffectedPackages != 1 || second.Worst != scan.SeverityHigh {
		t.Fatalf("second build summary = %#v, want one high finding on one package", second)
	}
	if second.Counts != (SeverityCounts{High: 1}) {
		t.Fatalf("second build counts = %#v, want one high finding and zeroes elsewhere", second.Counts)
	}

	version := summarizeVersionFindings([][]StoredFinding{buildA, buildB})
	if version.Findings != 1 {
		t.Fatalf("version findings = %d, want 1 after cross-build deduplication", version.Findings)
	}
	if version.AffectedPackages != 1 {
		t.Fatalf("version affected packages = %d, want 1 distinct package identity", version.AffectedPackages)
	}
	if version.Worst != scan.SeverityHigh {
		t.Fatalf("version worst = %q, want high", version.Worst)
	}
	if version.Counts != (SeverityCounts{High: 1}) {
		t.Fatalf("version counts = %#v, want one high finding and zeroes elsewhere", version.Counts)
	}
}
