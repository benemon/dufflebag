package scan

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"time"
)

// Scanner is the seam between the reconciler and an external scanner service.
// Scan is all-or-nothing: any partial provider failure returns an error, and
// Findings are usable only when the error is nil. Result always carries the
// attribution and every provider response received, even alongside an error,
// so a failed run still leaves an auditable transcript.
type Scanner interface {
	Scan(ctx context.Context, inv Inventory) (Result, error)
	Probe(ctx context.Context) (Health, error)
}

// Package is the full sbom_packages identity a finding attaches to.
type Package struct {
	SBOMID  string
	Name    string
	Version string
	Purl    string
}

type Inventory struct {
	Packages []Package
}

// Coverage reports what happened to every inventory package before any wire
// traffic: only Submitted packages were queried. Unsupported is a first-class
// state because the provider's empty answer is indistinguishable from
// supported-with-zero-findings (spike duf-o0ou.1).
type Coverage struct {
	Submitted   int
	Empty       int
	Invalid     int
	Unversioned int
	Unsupported int
}

// SeverityValue is one provider-supplied severity, verbatim.
type SeverityValue struct {
	// Source records where in the provider payload the value came from:
	// "osv" (severity[]), "osv:database_specific", "osv:ecosystem_specific".
	Source string
	// Type is the provider's type label: CVSS_V2/CVSS_V3/CVSS_V4 for vectors,
	// "label" for qualitative labels, "urgency" for Debian urgency.
	Type  string
	Value string
}

// Severity is the derived fixed display scale, computed for rollups only.
type Severity string

const (
	SeverityUnknown    Severity = "unknown"
	SeverityNegligible Severity = "negligible"
	SeverityLow        Severity = "low"
	SeverityMedium     Severity = "medium"
	SeverityHigh       Severity = "high"
	SeverityCritical   Severity = "critical"
)

type Finding struct {
	Package   Package
	ID        string
	Summary   string
	Aliases   []string
	Related   []string
	Published time.Time
	Modified  time.Time
	// Withdrawn is zero unless the provider withdrew the advisory.
	Withdrawn time.Time
	// FixedVersions holds every distinct fixed version the matching ranges
	// supply, sorted lexicographically; empty means no fix is available.
	FixedVersions []string
	Severities    []SeverityValue
	Severity      Severity
}

type Attribution struct {
	Adapter          string
	Engine           string
	DatabaseRevision string
	ObservedAt       time.Time
}

// Transcript holds provider response bodies in the adapter's canonical order.
// Encode length-prefixes each record with an 8-byte big-endian byte count;
// Digest is SHA-256 over exactly that encoding.
type Transcript struct {
	Records [][]byte
}

func (t Transcript) Encode() []byte {
	size := 0
	for _, r := range t.Records {
		size += 8 + len(r)
	}
	out := make([]byte, 0, size)
	var prefix [8]byte
	for _, r := range t.Records {
		binary.BigEndian.PutUint64(prefix[:], uint64(len(r)))
		out = append(out, prefix[:]...)
		out = append(out, r...)
	}
	return out
}

func (t Transcript) Digest() string {
	sum := sha256.Sum256(t.Encode())
	return hex.EncodeToString(sum[:])
}

type Result struct {
	Attribution Attribution
	Coverage    Coverage
	Findings    []Finding
	Transcript  Transcript
}

type Health struct {
	OK         bool
	Latency    time.Duration
	Detail     string
	ObservedAt time.Time
}
