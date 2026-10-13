package pluginimport

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/plugin"
	"github.com/benemon/dufflebag/internal/domain/registry"
	store "github.com/benemon/dufflebag/internal/store/postgres"
)

// The outcomes of one bundled version (ADR-0029 D4, D5).
const (
	BundleOutcomeImported = "imported"
	BundleOutcomeSkipped  = "skipped"
	BundleOutcomeMismatch = "mismatch"
	BundleOutcomeRefused  = "refused"
)

// BundleVersionOutcome is one bundled version's result, with where the
// bundle said it came from.
type BundleVersionOutcome struct {
	Plugin           string `json:"plugin"`
	Version          string `json:"version"`
	OriginKind       string `json:"origin_kind"`
	OriginRepository string `json:"origin_repository,omitempty"`
	Outcome          string `json:"outcome"`
	Error            string `json:"error,omitempty"`
}

// BundleImports is where the importer finds uploaded bundles and publishes
// their versions.
type BundleImports interface {
	ClaimPluginBundleImport(context.Context, time.Duration) (*store.ClaimedPluginBundleImport, error)
	RecordPluginBundleImport(context.Context, store.OrganizationTenant, string, store.PluginBundleOrigin, any, string) error
	OpenPluginObject(context.Context, string) (io.ReadCloser, error)
	DeletePluginBundleObject(context.Context, store.OrganizationTenant, string) error
	PluginVersionSumsDigest(context.Context, store.OrganizationTenant, string, string) (string, error)
	PublishPluginVersion(context.Context, store.OrganizationTenant, store.PluginVersionInput) error
}

// BundleImporter ingests uploaded plugin bundles.
type BundleImporter struct {
	imports  BundleImports
	keyring  openpgp.EntityList
	audit    *audit.SystemEmitter
	interval time.Duration
	logger   *slog.Logger
}

// NewBundleImporter polls queued bundle imports.
func NewBundleImporter(imports BundleImports, keyring openpgp.EntityList, emitter *audit.SystemEmitter, interval time.Duration, logger *slog.Logger) *BundleImporter {
	return &BundleImporter{imports: imports, keyring: keyring, audit: emitter, interval: interval, logger: logger}
}

// Run works queued imports until ctx ends.
func (b *BundleImporter) Run(ctx context.Context) {
	for {
		worked, err := b.RunOnce(ctx)
		if err != nil {
			b.logger.Error("plugin bundle import failed", "error", err)
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(b.interval):
		}
	}
}

// RunOnce ingests at most one queued bundle.
func (b *BundleImporter) RunOnce(ctx context.Context) (bool, error) {
	claimed, err := b.imports.ClaimPluginBundleImport(ctx, staleAfter)
	if err != nil || claimed == nil {
		return false, err
	}
	return true, b.ingest(ctx, claimed)
}

func (b *BundleImporter) ingest(ctx context.Context, claimed *store.ClaimedPluginBundleImport) (runErr error) {
	event := audit.SystemEvent{
		Operation: identity.AuditOperationPluginRegistryBundleIngest, TargetType: "plugin_registry", TargetID: claimed.Tenant.OrganizationID.String(),
		Scope: identity.AuditScopeOrganization, OrganizationID: claimed.Tenant.OrganizationID.String(),
		Detail: claimed.Upload.Filename,
	}
	correlationID := ""
	if b.audit != nil {
		id, err := b.audit.Request(event)
		if err != nil {
			return fmt.Errorf("audit plugin bundle import request: %w", err)
		}
		correlationID = id
		defer func() {
			outcome, reason := identity.AuditOutcomeSuccess, "ingested"
			if runErr != nil {
				outcome, reason, event.Detail = identity.AuditOutcomeFailure, "ingest_failed", runErr.Error()
			}
			if err := b.audit.Response(correlationID, event, outcome, reason); err != nil && runErr == nil {
				runErr = fmt.Errorf("audit plugin bundle import response: %w", err)
			}
		}()
	}
	outcomes := []BundleVersionOutcome{}
	origin := store.PluginBundleOrigin{}
	record := func(state, problem string) error {
		return b.imports.RecordPluginBundleImport(ctx, claimed.Tenant, state, origin, outcomes, problem)
	}
	defer func() {
		if err := b.imports.DeletePluginBundleObject(ctx, claimed.Tenant, claimed.Upload.ObjectKey); err != nil {
			b.logger.Warn("uploaded plugin bundle not removed after import", "object_key", claimed.Upload.ObjectKey, "error", err)
		}
	}()
	body, err := b.imports.OpenPluginObject(ctx, claimed.Upload.ObjectKey)
	if err != nil {
		return errors.Join(err, record(StateFailed, err.Error()))
	}
	defer func() { _ = body.Close() }()
	manifest, entries, err := openBundle(body)
	if err != nil {
		return errors.Join(err, record(StateFailed, err.Error()))
	}
	origin = store.PluginBundleOrigin{
		Host: manifest.Instance.Host, Organization: manifest.Instance.Organization, Version: manifest.DufflebagVersion,
		PluginCount: len(manifest.Plugins),
	}
	exportedAt := manifest.ExportedAt
	origin.ExportedAt = &exportedAt
	for _, bundled := range manifest.Plugins {
		origin.VersionCount += len(bundled.Versions)
	}
	if err := record(StateRunning, ""); err != nil {
		return err
	}
	// Entries arrive in manifest order, one version's files together; a
	// version is published once its last listed file has been read.
	for _, bundled := range manifest.Plugins {
		for _, version := range bundled.Versions {
			outcome := b.ingestVersion(ctx, claimed.Tenant, bundled, version, entries)
			outcomes = append(outcomes, outcome)
			if err := record(StateRunning, ""); err != nil {
				return err
			}
		}
	}
	if err := record(bundleFinalState(outcomes), ""); err != nil {
		return err
	}
	event.Detail = fmt.Sprintf("%s: %d plugins, %d versions, %s", claimed.Upload.Filename, origin.PluginCount, origin.VersionCount, summarise(outcomes))
	return nil
}

// bundleEntries reads a bundle's file entries one at a time, in archive order.
type bundleEntries struct {
	reader *tar.Reader
	next   *tar.Header
}

// openBundle reads the manifest, which must be the first entry, and leaves
// the reader on the file that follows.
func openBundle(body io.Reader) (plugin.BundleManifest, *bundleEntries, error) {
	gz, err := gzip.NewReader(body)
	if err != nil {
		return plugin.BundleManifest{}, nil, fmt.Errorf("the bundle is not a gzip archive: %w", err)
	}
	tr := tar.NewReader(gz)
	header, err := tr.Next()
	if err != nil {
		return plugin.BundleManifest{}, nil, fmt.Errorf("the bundle is not a tar archive: %w", err)
	}
	if header.Name != "manifest.json" {
		return plugin.BundleManifest{}, nil, fmt.Errorf("the bundle's first entry is %q, want manifest.json", header.Name)
	}
	var manifest plugin.BundleManifest
	if err := json.NewDecoder(io.LimitReader(tr, 64<<20)).Decode(&manifest); err != nil {
		return plugin.BundleManifest{}, nil, fmt.Errorf("the bundle manifest is not readable: %w", err)
	}
	if manifest.Format != 1 {
		return plugin.BundleManifest{}, nil, fmt.Errorf("the bundle manifest is format %d; this dufflebag reads format 1", manifest.Format)
	}
	entries := &bundleEntries{reader: tr}
	if err := entries.advance(); err != nil {
		return plugin.BundleManifest{}, nil, err
	}
	return manifest, entries, nil
}

func (e *bundleEntries) advance() error {
	header, err := e.reader.Next()
	if errors.Is(err, io.EOF) {
		e.next = nil
		return nil
	}
	if err != nil {
		return fmt.Errorf("the bundle is not readable: %w", err)
	}
	e.next = header
	return nil
}

type bundledFile struct {
	file   *os.File
	digest string
	size   int64
}

// ingestVersion reads one version's files from the archive and publishes
// them as an upload. Every failure is the version's recorded outcome; only
// an unreadable archive stops the import.
func (b *BundleImporter) ingestVersion(
	ctx context.Context, tenant store.OrganizationTenant, bundled plugin.BundlePlugin, version plugin.BundleVersion, entries *bundleEntries,
) BundleVersionOutcome {
	outcome := BundleVersionOutcome{
		Plugin: bundled.Name, Version: version.Version, OriginKind: bundled.Origin.Kind, OriginRepository: bundled.Origin.Repository,
	}
	refuse := func(format string, args ...any) BundleVersionOutcome {
		outcome.Outcome, outcome.Error = BundleOutcomeRefused, fmt.Sprintf(format, args...)
		return outcome
	}
	directory := "plugins/" + bundled.Name + "/" + version.Version + "/"
	files := map[string]bundledFile{}
	defer func() {
		for _, spooled := range files {
			_ = spooled.file.Close()
			_ = os.Remove(spooled.file.Name())
		}
	}()
	// Every listed file is read even after one fails, so the next version's
	// entries are still where the manifest says.
	altered := ""
	for _, listed := range version.Files {
		if entries.next == nil || entries.next.Name != directory+listed.Name {
			got := "the end of the archive"
			if entries.next != nil {
				got = entries.next.Name
			}
			return refuse("%s is listed in the manifest but the archive holds %s", directory+listed.Name, got)
		}
		spooled, err := spoolEntry(entries.reader)
		if err != nil {
			return refuse("%s: %v", listed.Name, err)
		}
		files[listed.Name] = spooled
		if altered == "" && (spooled.digest != listed.SHA256 || spooled.size != listed.Size) {
			altered = fmt.Sprintf("%s does not match the manifest (%d bytes, sha256 %s)", listed.Name, spooled.size, spooled.digest)
		}
		if err := entries.advance(); err != nil {
			return refuse("%v", err)
		}
	}
	if altered != "" {
		return refuse("%s", altered)
	}
	var sums, signature, manifest *bundledFile
	var sumsName, signatureName string
	uploaded := make([]plugin.UploadedZip, 0, len(files))
	blobs := map[string]store.PluginBlob{}
	for name, spooled := range files {
		spooled := spooled
		switch {
		case strings.HasSuffix(name, "_SHA256SUMS.sig"):
			signature, signatureName = &spooled, name
		case strings.HasSuffix(name, "_SHA256SUMS"):
			sums, sumsName = &spooled, name
		case strings.HasSuffix(name, "_manifest.json"):
			manifest = &spooled
		case strings.HasSuffix(name, ".zip"):
			archive, err := zip.NewReader(spooled.file, spooled.size)
			if err != nil {
				return refuse("%s is not a zip archive", name)
			}
			zipEntries := make([]string, 0, len(archive.File))
			for _, entry := range archive.File {
				zipEntries = append(zipEntries, entry.Name)
			}
			uploaded = append(uploaded, plugin.UploadedZip{Filename: name, SHA256: spooled.digest, Entries: zipEntries})
			blobs[name] = store.PluginBlob{Filename: name, SHA256: spooled.digest, Body: spooled.file, Size: spooled.size}
		default:
			return refuse("%s is not a release file", name)
		}
	}
	if sums == nil {
		return refuse("the version has no SHA256SUMS")
	}
	sumsData, err := readSpooled(sums)
	if err != nil {
		return refuse("%s: %v", sumsName, err)
	}
	// HashiCorp provenance is checked offline from the bundled signature (D5).
	if bundled.Origin.Kind == "releases-hashicorp" {
		if signature == nil {
			return refuse("%s came from releases.hashicorp.com but the bundle carries no signature", version.Version)
		}
		signatureData, err := readSpooled(signature)
		if err != nil {
			return refuse("%s: %v", signatureName, err)
		}
		if err := verifySignature(b.keyring, sumsData, signatureData); err != nil {
			return refuse("%v", err)
		}
	}
	held, err := b.imports.PluginVersionSumsDigest(ctx, tenant, bundled.Name, version.Version)
	if err != nil {
		return refuse("read the registry: %v", err)
	}
	if held != "" {
		if held != sums.digest {
			outcome.Outcome, outcome.Error = BundleOutcomeMismatch, fmt.Sprintf("%s %s is present with different checksums", bundled.Name, version.Version)
			return outcome
		}
		outcome.Outcome = BundleOutcomeSkipped
		return outcome
	}
	var manifestData []byte
	if manifest != nil {
		if manifestData, err = readSpooled(manifest); err != nil {
			return refuse("manifest: %v", err)
		}
	}
	verified, err := plugin.Verify(plugin.Upload{Name: bundled.Name, Version: version.Version, Sums: sumsData, Manifest: manifestData, Zips: uploaded})
	if err != nil {
		return refuse("%s", strings.TrimPrefix(err.Error(), registry.ErrInvalid.Error()+": "))
	}
	input := store.PluginVersionInput{
		Name: bundled.Name, Version: version.Version, Protocol: verified.Protocol, Source: store.PluginSource{Kind: "upload"},
		Sums: store.PluginBlob{Filename: sumsName, SHA256: sums.digest, Body: sums.file, Size: sums.size},
	}
	if signature != nil {
		input.Signature = &store.PluginBlob{Filename: signatureName, SHA256: signature.digest, Body: signature.file, Size: signature.size}
	}
	if manifest != nil {
		input.Manifest = &store.PluginBlob{Filename: plugin.ManifestName(bundled.Name, version.Version), SHA256: manifest.digest, Body: manifest.file, Size: manifest.size}
	}
	for _, listed := range verified.Listed {
		input.Listed = append(input.Listed, listed.OS+"_"+listed.Arch)
	}
	for _, zipFile := range verified.Zips {
		input.Zips = append(input.Zips, store.PluginZip{PluginBlob: blobs[zipFile.Filename], OS: zipFile.Platform.OS, Arch: zipFile.Platform.Arch})
	}
	err = b.imports.PublishPluginVersion(ctx, tenant, input)
	var heldBy store.HeldSourceError
	switch {
	case errors.Is(err, store.ErrPluginVersionExists):
		outcome.Outcome = BundleOutcomeSkipped
	case errors.As(err, &heldBy):
		return refuse("%s is held by another source (%s); the name is freed once every %s version is removed; revoked versions still count",
			bundled.Name, heldBy.Source.Kind, bundled.Name)
	case err != nil:
		return refuse("%v", err)
	default:
		outcome.Outcome = BundleOutcomeImported
	}
	return outcome
}

func spoolEntry(reader io.Reader) (bundledFile, error) {
	file, err := os.CreateTemp("", "dufflebag-bundle-*")
	if err != nil {
		return bundledFile{}, err
	}
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, hash), reader)
	if err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return bundledFile{}, err
	}
	return bundledFile{file: file, digest: hex.EncodeToString(hash.Sum(nil)), size: size}, nil
}

func readSpooled(spooled *bundledFile) ([]byte, error) {
	if spooled.size > 1<<20 {
		return nil, fmt.Errorf("exceeds %d bytes", 1<<20)
	}
	return io.ReadAll(io.NewSectionReader(spooled.file, 0, spooled.size))
}

func bundleFinalState(outcomes []BundleVersionOutcome) string {
	refused, taken := 0, 0
	for _, outcome := range outcomes {
		switch outcome.Outcome {
		case BundleOutcomeRefused, BundleOutcomeMismatch:
			refused++
		default:
			taken++
		}
	}
	switch {
	case refused > 0 && taken == 0:
		return StateFailed
	case refused > 0:
		return StatePartiallySucceeded
	}
	return StateSucceeded
}

func summarise(outcomes []BundleVersionOutcome) string {
	counts := map[string]int{}
	for _, outcome := range outcomes {
		counts[outcome.Outcome]++
	}
	return fmt.Sprintf("%d imported, %d skipped, %d mismatched, %d refused",
		counts[BundleOutcomeImported], counts[BundleOutcomeSkipped], counts[BundleOutcomeMismatch], counts[BundleOutcomeRefused])
}
