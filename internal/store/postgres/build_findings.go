package postgres

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/benemon/dufflebag/internal/scan"
)

// BuildFindings is one build's package inventory state and current verified
// scan findings.
type BuildFindings struct {
	Inventory        string
	PackagesTotal    int
	Scanned          bool
	Run              *ScanRun
	PackagesAffected int
	Advisories       []BuildAdvisory
}

// BuildAdvisory groups every affected SBOM package report for one advisory.
type BuildAdvisory struct {
	Identifier    string
	Severity      scan.Severity
	Summary       string
	Aliases       []string
	Published     *time.Time
	FixedVersions []string
	Packages      []BuildAdvisoryPackage
}

// BuildAdvisoryPackage is one affected package report in one SBOM.
type BuildAdvisoryPackage struct {
	Name         string
	Version      string
	Purl         string
	SBOMID       string
	FixedVersion string
}

type buildFindingPackageKey struct {
	name, version, purl, sbomID string
}

type buildAdvisoryGroup struct {
	BuildAdvisory
	aliasSeen    map[string]bool
	fixedSeen    map[string]bool
	packageFixed map[buildFindingPackageKey][]string
	packageSeen  map[buildFindingPackageKey]map[string]bool
	packageIDs   map[packageSummaryKey]bool
}

// GetBuildFindings reads a build, inventory state and current findings in one
// tenant transaction. Every run, pointer and finding row is MAC verified.
func (r *Repository) GetBuildFindings(
	ctx context.Context, tenant Tenant, bucketName, fingerprint, buildID string,
) (*BuildFindings, error) {
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	total, unparseable, err := countBuildPackages(
		ctx, tx, q, bucketName, fingerprint, buildID, BuildPackageFilter{},
	)
	if err != nil {
		return nil, err
	}
	result := &BuildFindings{
		Inventory: "parsed", PackagesTotal: total, Advisories: make([]BuildAdvisory, 0),
	}
	if len(unparseable) > 0 {
		result.Inventory = "unparseable"
		result.PackagesTotal = 0
	}

	state, err := readBuildScanState(ctx, tx, r, tenant, buildID, false)
	if err != nil {
		return nil, err
	}
	if state != nil && state.CurrentFindingsRunID != "" {
		result.Run, err = readScanRun(ctx, tx, r, tenant, state.CurrentFindingsRunID)
		if err != nil {
			return nil, err
		}
		if result.Run.BuildID != buildID {
			return nil, fmt.Errorf("scan run %s belongs to build %s, not %s",
				result.Run.ID, result.Run.BuildID, buildID)
		}
		findings, err := queryScanFindings(ctx, tx, r, tenant, result.Run.ID, "")
		if err != nil {
			return nil, err
		}
		result.Scanned = true
		result.Advisories, result.PackagesAffected = groupBuildAdvisories(findings)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit get build findings: %w", err)
	}
	return result, nil
}

func groupBuildAdvisories(findings []StoredFinding) ([]BuildAdvisory, int) {
	affected := make(map[packageSummaryKey]bool)
	for _, finding := range DeduplicateBuildFindings(findings) {
		affected[packageSummaryKey{
			name: finding.Package.Name, version: finding.Package.Version, purl: finding.Package.Purl,
		}] = true
	}

	groups := make(map[string]*buildAdvisoryGroup)
	for _, finding := range findings {
		group := groups[finding.ID]
		if group == nil {
			group = &buildAdvisoryGroup{
				BuildAdvisory: BuildAdvisory{
					Identifier: finding.ID, Severity: finding.Severity, Summary: finding.Summary,
					Aliases: make([]string, 0), FixedVersions: make([]string, 0),
				},
				aliasSeen: make(map[string]bool), fixedSeen: make(map[string]bool),
				packageFixed: make(map[buildFindingPackageKey][]string),
				packageSeen:  make(map[buildFindingPackageKey]map[string]bool),
				packageIDs:   make(map[packageSummaryKey]bool),
			}
			groups[finding.ID] = group
		}
		if scan.Worse(finding.Severity, group.Severity) {
			group.Severity = finding.Severity
		}
		if !finding.Published.IsZero() &&
			(group.Published == nil || finding.Published.After(*group.Published)) {
			published := finding.Published
			group.Published = &published
		}
		for _, alias := range finding.Aliases {
			if !group.aliasSeen[alias] {
				group.aliasSeen[alias] = true
				group.Aliases = append(group.Aliases, alias)
			}
		}
		key := buildFindingPackageKey{
			name: finding.Package.Name, version: finding.Package.Version,
			purl: finding.Package.Purl, sbomID: finding.Package.SBOMID,
		}
		if group.packageSeen[key] == nil {
			group.packageSeen[key] = make(map[string]bool)
		}
		for _, fixed := range finding.FixedVersions {
			if !group.fixedSeen[fixed] {
				group.fixedSeen[fixed] = true
				group.FixedVersions = append(group.FixedVersions, fixed)
			}
			if !group.packageSeen[key][fixed] {
				group.packageSeen[key][fixed] = true
				group.packageFixed[key] = append(group.packageFixed[key], fixed)
			}
		}
		group.packageIDs[packageSummaryKey{key.name, key.version, key.purl}] = true
	}

	advisories := make([]BuildAdvisory, 0, len(groups))
	packageCounts := make(map[string]int, len(groups))
	for identifier, group := range groups {
		keys := make([]buildFindingPackageKey, 0, len(group.packageSeen))
		for key := range group.packageSeen {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			left, right := keys[i], keys[j]
			if left.name != right.name {
				return left.name < right.name
			}
			if left.version != right.version {
				return left.version < right.version
			}
			if left.purl != right.purl {
				return left.purl < right.purl
			}
			return left.sbomID < right.sbomID
		})
		group.Packages = make([]BuildAdvisoryPackage, 0, len(keys))
		for _, key := range keys {
			group.Packages = append(group.Packages, BuildAdvisoryPackage{
				Name: key.name, Version: key.version, Purl: key.purl, SBOMID: key.sbomID,
				FixedVersion: strings.Join(group.packageFixed[key], ", "),
			})
		}
		packageCounts[identifier] = len(group.packageIDs)
		advisories = append(advisories, group.BuildAdvisory)
	}
	sort.Slice(advisories, func(i, j int) bool {
		left, right := advisories[i], advisories[j]
		if left.Severity != right.Severity {
			return scan.Worse(left.Severity, right.Severity)
		}
		if packageCounts[left.Identifier] != packageCounts[right.Identifier] {
			return packageCounts[left.Identifier] > packageCounts[right.Identifier]
		}
		return left.Identifier < right.Identifier
	})
	return advisories, len(affected)
}
