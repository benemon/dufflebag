package postgres

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/benemon/dufflebag/internal/store/postgres/postgresdb"
	"github.com/google/uuid"
)

// Row integrity MACs (ADR-0024, encrypted deployments only). Each provenance
// and identity row carries an HMAC over a canonical message of its
// authority-bearing fields, bound to tenant and row identity like any other
// AAD. An altered row fails verification; a valid row cannot be replayed into
// another context; a row this application did not write has no MAC and fails
// too. There is deliberately no hash chain — the fail-closed audit trail is
// the deletion detector.
//
// Messages carry linkage and authority fields, not display fields (labels,
// names, descriptions) and not timestamps — with one exception: the channel
// assignment timestamp orders history and thereby decides which assignment is
// current, so it is authority. It is truncated to the microsecond precision
// Postgres stores, at the write site, so the message survives the round trip.
const macMessageVersion = "dfbg-mac-1"

func macMessage(parts ...string) []byte {
	return []byte(macMessageVersion + "|" + strings.Join(parts, "|"))
}

func versionMACMessage(row postgresdb.Version) []byte {
	sequence := ""
	if row.Sequence.Valid {
		sequence = strconv.Itoa(int(row.Sequence.Int32))
	}
	// Revocation is authority — it decides whether consumers may use the
	// version — so its effect time, author and ancestor linkage are bound. The
	// message and the denormalized ancestor names are display. The timestamp is
	// truncated to the microseconds Postgres stores, like the assignment MAC.
	revokeAt := ""
	if row.RevokeAt.Valid {
		revokeAt = strconv.FormatInt(row.RevokeAt.Time.Truncate(time.Microsecond).UnixMicro(), 10)
	}
	return macMessage(
		row.OrganizationID.String(), row.ProjectID.String(), "version", row.ID,
		row.BucketID, row.Fingerprint, row.TemplateType, row.AuthorID,
		strconv.FormatBool(row.Complete), sequence,
		revokeAt, row.RevocationAuthor.String, row.RevocationInheritedFromID.String,
	)
}

func buildMACMessage(row postgresdb.Build) []byte {
	metadata := sha256.Sum256(row.Metadata)
	return macMessage(
		row.OrganizationID.String(), row.ProjectID.String(), "build", row.ID,
		row.VersionID, row.ComponentType, row.Status, row.Platform,
		row.PackerRunUuid, row.SourceExternalIdentifier,
		row.ParentVersionID.String, row.ParentChannelID.String,
		hex.EncodeToString(metadata[:]),
	)
}

func artifactMACMessage(row postgresdb.Artifact) []byte {
	return macMessage(
		row.OrganizationID.String(), row.ProjectID.String(), "artifact", row.ID,
		row.BuildID, row.ExternalIdentifier, row.Region,
	)
}

func assignmentMACMessage(tenant Tenant, id, channelID, versionID, authorID string, assignedAt time.Time) []byte {
	return macMessage(
		tenant.OrganizationID.String(), tenant.ProjectID.String(), "assignment", id,
		channelID, versionID, authorID,
		assignedAt.UTC().Format(time.RFC3339Nano),
	)
}

func principalMACMessage(id, clientID string, organizationID, projectID uuid.NullUUID, role string) []byte {
	organization, project := "", ""
	if organizationID.Valid {
		organization = organizationID.UUID.String()
	}
	if projectID.Valid {
		project = projectID.UUID.String()
	}
	return macMessage("principal", id, clientID, organization, project, role)
}

func principalSecretMACMessage(id, principalID, encodedHash string, expiresAt sql.NullTime) []byte {
	// Expiry is authority — it decides whether the credential grants anything —
	// so a psql edit stretching it must fail verification. Micro-truncated like
	// the assignment timestamp, so the message survives the round trip.
	expiry := ""
	if expiresAt.Valid {
		expiry = strconv.FormatInt(expiresAt.Time.Truncate(time.Microsecond).UnixMicro(), 10)
	}
	return macMessage("principal_secret", id, principalID, encodedHash, expiry)
}

// rowMAC is nil on unencrypted deployments, where the column stays NULL.
func (r *Repository) rowMAC(message []byte) []byte {
	if r.ring == nil {
		return nil
	}
	return r.ring.MAC(message)
}

// verifyRowMAC is a no-op on unencrypted deployments.
func (r *Repository) verifyRowMAC(kind string, mac, message []byte) error {
	if r.ring == nil {
		return nil
	}
	if err := r.ring.VerifyMAC(mac, message); err != nil {
		return fmt.Errorf("%s: %w", kind, err)
	}
	return nil
}

// assignmentWriteTime truncates to the microsecond precision Postgres stores,
// so the MAC message equals what a later read recomputes.
func assignmentWriteTime(at time.Time) time.Time {
	return at.UTC().Truncate(time.Microsecond)
}

// scanWriteTime truncates like assignmentWriteTime: scan ordering and
// first-seen provenance are authority, and the message must survive the
// Postgres round trip.
func scanWriteTime(at time.Time) time.Time {
	return at.UTC().Truncate(time.Microsecond)
}

// canonicalJSONDigest hex-digests the canonical Go marshalling of a value.
// jsonb does not preserve the bytes it was given, so MAC messages carry the
// digest of the parsed value re-marshalled by the same Go types on both the
// write and read paths.
func canonicalJSONDigest(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// Marshalling repository-owned types cannot fail; a zero digest
		// would silently authenticate, so fail loudly.
		panic(fmt.Sprintf("canonical digest: %v", err))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// scanEscape makes the message separator unambiguous for the scan rows, whose
// fields carry provider-influenced free text. Without it a run with
// error="timeout|osv" and adapter="official" produces the same message as
// error="timeout" with adapter="osv|official", so a tampered row verifies
// against the original MAC.
func scanEscape(value string) string {
	return strings.NewReplacer(`\`, `\\`, "|", `\|`).Replace(value)
}

func scanMACMessage(parts ...string) []byte {
	escaped := make([]string, len(parts))
	for i, p := range parts {
		escaped[i] = scanEscape(p)
	}
	return macMessage(escaped...)
}

func scanRunMACMessage(tenant Tenant, run ScanRun) []byte {
	return scanMACMessage(
		tenant.OrganizationID.String(), tenant.ProjectID.String(),
		"scan_run", run.ID, run.BuildID,
		strconv.FormatInt(run.RunSequence, 10),
		run.Status, run.Error,
		run.Adapter, run.Engine, run.DatabaseRevision,
		run.ObservedAt.Format(time.RFC3339Nano),
		// created_at decides retention eligibility, so it is authority.
		run.CreatedAt.Format(time.RFC3339Nano),
		run.TranscriptDigest,
		canonicalJSONDigest(run.Coverage),
	)
}

// scanTranscriptMACMessage authenticates the locator: expiry deletes the
// object this row names, so an unauthenticated object_key lets a database
// attacker aim one tenant's expiry at another tenant's object.
func scanTranscriptMACMessage(tenant Tenant, runID, objectKey string, expiresAt time.Time) []byte {
	return scanMACMessage(
		tenant.OrganizationID.String(), tenant.ProjectID.String(),
		"scan_transcript", runID, objectKey,
		expiresAt.Format(time.RFC3339Nano),
	)
}

func scanFindingMACMessage(tenant Tenant, runID string, f StoredFinding) []byte {
	return scanMACMessage(
		tenant.OrganizationID.String(), tenant.ProjectID.String(),
		"scan_finding", runID,
		f.Package.SBOMID, f.Package.Name, f.Package.Version, f.Package.Purl,
		f.ID, f.Summary, string(f.Severity),
		f.Published.UTC().Format(time.RFC3339Nano),
		f.Modified.UTC().Format(time.RFC3339Nano),
		f.Withdrawn.UTC().Format(time.RFC3339Nano),
		f.FirstSeenAt.Format(time.RFC3339Nano),
		canonicalJSONDigest(f.Aliases),
		canonicalJSONDigest(f.Related),
		canonicalJSONDigest(f.FixedVersions),
		canonicalJSONDigest(f.Severities),
	)
}

func buildScanStateMACMessage(tenant Tenant, buildID, currentRunID, latestAttemptRunID string) []byte {
	return scanMACMessage(
		tenant.OrganizationID.String(), tenant.ProjectID.String(),
		"build_scan_state", buildID, currentRunID, latestAttemptRunID,
	)
}
