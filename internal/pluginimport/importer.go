package pluginimport

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/benemon/dufflebag/internal/domain/plugin"
	"github.com/benemon/dufflebag/internal/domain/registry"
	store "github.com/benemon/dufflebag/internal/store/postgres"
)

// Outcomes recorded per version and per platform of an import.
const (
	OutcomeImported        = "imported"
	OutcomeAlreadyMirrored = "already_mirrored"
	OutcomeRevoked         = "revoked"
	OutcomeRestored        = "restored"
	OutcomeFailed          = "failed"
)

// PlatformOutcome is one requested platform of one version.
type PlatformOutcome struct {
	Platform string `json:"platform"`
	Outcome  string `json:"outcome"`
	Error    string `json:"error,omitempty"`
}

// VersionOutcome is one requested version of an import.
type VersionOutcome struct {
	Version   string            `json:"version"`
	Outcome   string            `json:"outcome"`
	Error     string            `json:"error,omitempty"`
	Platforms []PlatformOutcome `json:"platforms,omitempty"`
}

// Repository is what an import writes through.
type Repository interface {
	PublishPluginVersion(context.Context, store.OrganizationTenant, store.PluginVersionInput) error
	ListPluginVersions(context.Context, store.OrganizationTenant, string) (store.PluginSource, []store.PluginVersionSummary, error)
	GetStoredPluginVersion(ctx context.Context, tenant store.OrganizationTenant, name, version string) (store.StoredPluginVersion, error)
	AddPluginZips(ctx context.Context, tenant store.OrganizationTenant, name, version string, zips []store.PluginZip) error
	SetPluginVersionRevoked(ctx context.Context, tenant store.OrganizationTenant, name, version string, revoked bool) error
}

// Importer mirrors versions from releases.hashicorp.com and GitHub releases.
type Importer struct {
	upstream   *Upstream
	github     *GitHub
	keyring    openpgp.EntityList
	repository Repository
}

// NewImporter verifies every releases.hashicorp.com SHA256SUMS against
// keyring before storing it; GitHub releases carry no key to check.
func NewImporter(upstream *Upstream, github *GitHub, keyring openpgp.EntityList, repository Repository) *Importer {
	return &Importer{upstream: upstream, github: github, keyring: keyring, repository: repository}
}

// The signature to verify is the one named for the pinned key's short ID;
// releases.hashicorp.com also publishes an unkeyed copy.
func signatureFor(keyring openpgp.EntityList, names []string) string {
	for _, entity := range keyring {
		short := fmt.Sprintf(".%08X.sig", uint32(entity.PrimaryKey.KeyId))
		for _, name := range names {
			if strings.HasSuffix(name, short) {
				return name
			}
		}
	}
	return ""
}

// ImportVersion mirrors one releases.hashicorp.com version's selected
// platforms. It never returns an error: every failure is the version's
// recorded outcome.
func (i *Importer) ImportVersion(
	ctx context.Context, tenant store.OrganizationTenant, product, version string, platforms []string,
) VersionOutcome {
	failed := func(format string, args ...any) VersionOutcome {
		return VersionOutcome{Version: version, Outcome: OutcomeFailed, Error: fmt.Sprintf(format, args...)}
	}
	name := strings.TrimPrefix(product, "packer-plugin-")
	if !ValidProduct(product) {
		return failed("%s is not a Packer plugin product", product)
	}
	if mirrored, err := i.mirrored(ctx, tenant, name, version); err != nil {
		return failed("read the registry: %v", err)
	} else if mirrored {
		return VersionOutcome{Version: version, Outcome: OutcomeAlreadyMirrored}
	}

	files, err := i.upstream.release(ctx, product, version)
	if err != nil {
		return failed("%v", err)
	}
	sums, err := i.upstream.fetchSmall(ctx, i.upstream.fileURL(product, version, files.Shasums), 1<<20)
	if err != nil {
		return failed("%v", err)
	}
	signatureName := signatureFor(i.keyring, files.Signatures)
	if signatureName == "" {
		return failed("the release has no signature by the pinned HashiCorp key")
	}
	signature, err := i.upstream.fetchSmall(ctx, i.upstream.fileURL(product, version, signatureName), 1<<20)
	if err != nil {
		return failed("%v", err)
	}
	if err := verifySignature(i.keyring, sums, signature); err != nil {
		return failed("%v", err)
	}
	listed, err := plugin.ListsManifest(name, version, sums)
	if err != nil {
		return failed("%v", err)
	}
	var manifest []byte
	if listed {
		if manifest, err = i.upstream.fetchSmall(ctx, i.upstream.fileURL(product, version, plugin.ManifestName(name, version)), 1<<20); err != nil {
			return failed("%v", err)
		}
	}

	zips := map[string]asset{}
	for _, b := range files.Builds {
		zips[b.OS+"_"+b.Arch] = asset{name: b.Filename, url: i.upstream.fileURL(product, version, b.Filename)}
	}
	return i.publish(ctx, tenant, fetched{
		name: name, version: version, source: store.PluginSource{Kind: "releases-hashicorp", Repository: product},
		sums: sums, sumsName: files.Shasums, signature: signature, signatureName: signatureName, manifest: manifest,
		zips: zips, platforms: platforms,
	})
}

// ImportGitHubVersion mirrors one public GitHub release's selected platforms.
// Its signature, if any, is stored verbatim and never verified (ADR-0027 D11).
func (i *Importer) ImportGitHubVersion(
	ctx context.Context, tenant store.OrganizationTenant, repository, tag string, platforms []string,
) VersionOutcome {
	failed := func(format string, args ...any) VersionOutcome {
		return VersionOutcome{Version: strings.TrimPrefix(tag, "v"), Outcome: OutcomeFailed, Error: fmt.Sprintf(format, args...)}
	}
	if !ValidRepository(repository) {
		return failed("%s is not an owner/packer-plugin-<name> repository", repository)
	}
	release, err := i.github.Release(ctx, repository, tag)
	if err != nil {
		return failed("%v", err)
	}
	if mirrored, err := i.mirrored(ctx, tenant, release.Name, release.Version); err != nil {
		return failed("read the registry: %v", err)
	} else if mirrored {
		return VersionOutcome{Version: release.Version, Outcome: OutcomeAlreadyMirrored}
	}
	var sumsName, signatureName string
	for name := range release.assets {
		switch {
		case strings.HasSuffix(name, "_SHA256SUMS"):
			if sumsName != "" {
				return failed("the release has more than one SHA256SUMS asset")
			}
			sumsName = name
		case strings.HasSuffix(name, "_SHA256SUMS.sig"):
			signatureName = name
		}
	}
	if sumsName == "" {
		return failed("the release has no SHA256SUMS asset, so it cannot be verified or served")
	}
	sums, err := i.upstream.fetchSmall(ctx, release.assets[sumsName], 1<<20)
	if err != nil {
		return failed("%v", err)
	}
	var signature []byte
	if signatureName != "" {
		if signature, err = i.upstream.fetchSmall(ctx, release.assets[signatureName], 1<<20); err != nil {
			return failed("%v", err)
		}
	}
	listed, err := plugin.ListsManifest(release.Name, release.Version, sums)
	if err != nil {
		return failed("%v", err)
	}
	var manifest []byte
	if manifestName := plugin.ManifestName(release.Name, release.Version); listed {
		url, ok := release.assets[manifestName]
		if !ok {
			return failed("SHA256SUMS lists %s, but the release has no such asset", manifestName)
		}
		if manifest, err = i.upstream.fetchSmall(ctx, url, 1<<20); err != nil {
			return failed("%v", err)
		}
	}
	zips := map[string]asset{}
	for name, url := range release.assets {
		if m := zipPlatform.FindStringSubmatch(name); m != nil {
			zips[m[1]+"_"+m[2]] = asset{name: name, url: url}
		}
	}
	return i.publish(ctx, tenant, fetched{
		name: release.Name, version: release.Version, source: store.PluginSource{Kind: "github", Repository: repository},
		sums: sums, sumsName: sumsName, signature: signature, signatureName: signatureName, manifest: manifest,
		zips: zips, platforms: platforms,
	})
}

func (i *Importer) mirrored(ctx context.Context, tenant store.OrganizationTenant, name, version string) (bool, error) {
	_, existing, err := i.repository.ListPluginVersions(ctx, tenant, name)
	if errors.Is(err, registry.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, mirrored := range existing {
		if mirrored.Version == version {
			return true, nil
		}
	}
	return false, nil
}

type asset struct{ name, url string }

type fetched struct {
	name, version string
	source        store.PluginSource
	sums          []byte
	sumsName      string
	signature     []byte
	signatureName string
	manifest      []byte
	zips          map[string]asset
	platforms     []string
}

type spool struct {
	uploaded  []plugin.UploadedZip
	blobs     map[string]store.PluginBlob
	platforms []PlatformOutcome
	files     []*os.File
}

func (s *spool) close() {
	for _, file := range s.files {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}
}

// fetchZips downloads each selected platform's zip. A platform that cannot
// be fetched is recorded and skipped; only a local spooling failure errors.
func (i *Importer) fetchZips(ctx context.Context, zips map[string]asset, platforms []string) (*spool, error) {
	spooled := &spool{blobs: map[string]store.PluginBlob{}}
	skip := func(platform, reason string) {
		spooled.platforms = append(spooled.platforms, PlatformOutcome{Platform: platform, Outcome: OutcomeFailed, Error: reason})
	}
	for _, platform := range platforms {
		found, ok := zips[platform]
		if !ok {
			skip(platform, "not published upstream")
			continue
		}
		file, err := os.CreateTemp("", "dufflebag-import-*")
		if err != nil {
			spooled.close()
			return nil, fmt.Errorf("spool %s: %w", found.name, err)
		}
		spooled.files = append(spooled.files, file)
		digest, size, err := i.upstream.download(ctx, found.url, file)
		if err != nil {
			skip(platform, err.Error())
			continue
		}
		archive, err := zip.NewReader(file, size)
		if err != nil {
			skip(platform, "not a zip archive")
			continue
		}
		entries := make([]string, 0, len(archive.File))
		for _, entry := range archive.File {
			entries = append(entries, entry.Name)
		}
		spooled.uploaded = append(spooled.uploaded, plugin.UploadedZip{Filename: found.name, SHA256: digest, Entries: entries})
		spooled.blobs[found.name] = store.PluginBlob{Filename: found.name, SHA256: digest, Body: file, Size: size}
		spooled.platforms = append(spooled.platforms, PlatformOutcome{Platform: platform, Outcome: OutcomeImported})
	}
	return spooled, nil
}

func (i *Importer) publish(ctx context.Context, tenant store.OrganizationTenant, release fetched) VersionOutcome {
	name, version := release.name, release.version
	spooled, err := i.fetchZips(ctx, release.zips, release.platforms)
	if err != nil {
		return VersionOutcome{Version: version, Outcome: OutcomeFailed, Error: err.Error()}
	}
	defer spooled.close()
	outcome := VersionOutcome{Version: version, Platforms: spooled.platforms}
	uploaded, blobs := spooled.uploaded, spooled.blobs
	if len(uploaded) == 0 {
		outcome.Outcome, outcome.Error = OutcomeFailed, "none of the selected platforms could be fetched"
		return outcome
	}

	verified, err := plugin.Verify(plugin.Upload{Name: name, Version: version, Sums: release.sums, Manifest: release.manifest, Zips: uploaded})
	if err != nil {
		outcome.Outcome, outcome.Error = OutcomeFailed, strings.TrimPrefix(err.Error(), registry.ErrInvalid.Error()+": ")
		return outcome
	}
	input := store.PluginVersionInput{
		Name: name, Version: version, Protocol: verified.Protocol, Source: release.source,
		Sums: newBlob(release.sumsName, release.sums),
	}
	if release.signature != nil {
		blob := newBlob(release.signatureName, release.signature)
		input.Signature = &blob
	}
	if release.manifest != nil {
		blob := newBlob(plugin.ManifestName(name, version), release.manifest)
		input.Manifest = &blob
	}
	for _, listed := range verified.Listed {
		input.Listed = append(input.Listed, listed.OS+"_"+listed.Arch)
	}
	for _, zipFile := range verified.Zips {
		input.Zips = append(input.Zips, store.PluginZip{PluginBlob: blobs[zipFile.Filename], OS: zipFile.Platform.OS, Arch: zipFile.Platform.Arch})
	}
	err = i.repository.PublishPluginVersion(ctx, tenant, input)
	var held store.HeldSourceError
	switch {
	case errors.Is(err, store.ErrPluginVersionExists):
		return VersionOutcome{Version: version, Outcome: OutcomeAlreadyMirrored}
	case errors.As(err, &held):
		outcome.Outcome, outcome.Error = OutcomeFailed, fmt.Sprintf(
			"%s is held by another source (%s); the name is freed once every %s version is removed; revoked versions still count",
			name, held.Source.Kind, name)
	case err != nil:
		outcome.Outcome, outcome.Error = OutcomeFailed, err.Error()
	default:
		outcome.Outcome = OutcomeImported
	}
	return outcome
}

// Sync applies one change a publisher made from a plugin's detail page
// (ADR-0027 A7): add platforms to a version or mirror a new one, or revoke
// or restore a version.
func (i *Importer) Sync(ctx context.Context, tenant store.OrganizationTenant, name string, change store.PluginChange) VersionOutcome {
	failed := func(format string, args ...any) VersionOutcome {
		return VersionOutcome{Version: change.Version, Outcome: OutcomeFailed, Error: fmt.Sprintf(format, args...)}
	}
	switch change.Action {
	case "revoke", "restore":
		revoke := change.Action == "revoke"
		err := i.repository.SetPluginVersionRevoked(ctx, tenant, name, change.Version, revoke)
		if err != nil && !errors.Is(err, store.ErrPluginVersionRevoked) && !errors.Is(err, store.ErrPluginVersionNotRevoked) {
			return failed("%v", err)
		}
		if revoke {
			return VersionOutcome{Version: change.Version, Outcome: OutcomeRevoked}
		}
		return VersionOutcome{Version: change.Version, Outcome: OutcomeRestored}
	case "add":
	default:
		return failed("unknown change %q", change.Action)
	}
	source, versions, err := i.repository.ListPluginVersions(ctx, tenant, name)
	if err != nil {
		return failed("read the registry: %v", err)
	}
	for _, mirrored := range versions {
		if mirrored.Version == change.Version {
			return i.addPlatforms(ctx, tenant, source, name, change.Version, change.Platforms)
		}
	}
	if source.Kind != "releases-hashicorp" {
		return failed("new versions of a %s plugin are added by %s", source.Kind, map[string]string{"github": "importing their release link", "upload": "upload"}[source.Kind])
	}
	return i.ImportVersion(ctx, tenant, source.Repository, change.Version, change.Platforms)
}

// addPlatforms admits a zip only if the version's stored SHA256SUMS lists it
// with the digest just downloaded, so a release changed upstream since the
// first import cannot add to it (ADR-0027 A7).
func (i *Importer) addPlatforms(
	ctx context.Context, tenant store.OrganizationTenant, source store.PluginSource, name, version string, platforms []string,
) VersionOutcome {
	failed := func(format string, args ...any) VersionOutcome {
		return VersionOutcome{Version: version, Outcome: OutcomeFailed, Error: fmt.Sprintf(format, args...)}
	}
	stored, err := i.repository.GetStoredPluginVersion(ctx, tenant, name, version)
	if err != nil {
		return failed("read the registry: %v", err)
	}
	var wanted []string
	for _, platform := range platforms {
		if !slices.Contains(stored.Stored, platform) {
			wanted = append(wanted, platform)
		}
	}
	if len(wanted) == 0 {
		return VersionOutcome{Version: version, Outcome: OutcomeAlreadyMirrored}
	}
	var zips map[string]asset
	switch source.Kind {
	case "releases-hashicorp":
		files, err := i.upstream.release(ctx, source.Repository, version)
		if err != nil {
			return failed("%v", err)
		}
		zips = map[string]asset{}
		for _, b := range files.Builds {
			zips[b.OS+"_"+b.Arch] = asset{name: b.Filename, url: i.upstream.fileURL(source.Repository, version, b.Filename)}
		}
	case "github":
		release, err := i.github.Release(ctx, source.Repository, "v"+version)
		if errors.Is(err, ErrUpstreamNotFound) {
			release, err = i.github.Release(ctx, source.Repository, version)
		}
		if err != nil {
			return failed("%v", err)
		}
		zips = map[string]asset{}
		for filename, url := range release.assets {
			if m := zipPlatform.FindStringSubmatch(filename); m != nil {
				zips[m[1]+"_"+m[2]] = asset{name: filename, url: url}
			}
		}
	default:
		return failed("platforms of an uploaded plugin are added by upload")
	}
	spooled, err := i.fetchZips(ctx, zips, wanted)
	if err != nil {
		return failed("%v", err)
	}
	defer spooled.close()
	outcome := VersionOutcome{Version: version, Platforms: spooled.platforms}
	if len(spooled.uploaded) == 0 {
		outcome.Outcome, outcome.Error = OutcomeFailed, "none of the selected platforms could be fetched"
		return outcome
	}
	verified, err := plugin.Verify(plugin.Upload{Name: name, Version: version, Sums: stored.Sums, Manifest: stored.Manifest, Zips: spooled.uploaded})
	if err != nil {
		outcome.Outcome, outcome.Error = OutcomeFailed, strings.TrimPrefix(err.Error(), registry.ErrInvalid.Error()+": ")
		return outcome
	}
	if verified.Protocol != "" && stored.Protocol != "" && verified.Protocol != stored.Protocol {
		outcome.Outcome, outcome.Error = OutcomeFailed, fmt.Sprintf("the new zips speak plugin protocol %s; the version's existing zips speak %s", verified.Protocol, stored.Protocol)
		return outcome
	}
	var zipFiles []store.PluginZip
	for _, zipFile := range verified.Zips {
		zipFiles = append(zipFiles, store.PluginZip{PluginBlob: spooled.blobs[zipFile.Filename], OS: zipFile.Platform.OS, Arch: zipFile.Platform.Arch})
	}
	if err := i.repository.AddPluginZips(ctx, tenant, name, version, zipFiles); err != nil {
		outcome.Outcome, outcome.Error = OutcomeFailed, err.Error()
		return outcome
	}
	outcome.Outcome = OutcomeImported
	return outcome
}
