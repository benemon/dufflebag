package pluginimport

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"os"
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
}

// Importer mirrors versions from releases.hashicorp.com.
type Importer struct {
	upstream   *Upstream
	keyring    openpgp.EntityList
	repository Repository
}

// NewImporter verifies every SHA256SUMS against keyring before storing it.
func NewImporter(upstream *Upstream, keyring openpgp.EntityList, repository Repository) *Importer {
	return &Importer{upstream: upstream, keyring: keyring, repository: repository}
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

// ImportVersion mirrors one version's selected platforms. It never returns
// an error: every failure is the version's recorded outcome.
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
	_, existing, err := i.repository.ListPluginVersions(ctx, tenant, name)
	if err != nil && !errors.Is(err, registry.ErrNotFound) {
		return failed("read the registry: %v", err)
	}
	for _, mirrored := range existing {
		if mirrored.Version == version {
			return VersionOutcome{Version: version, Outcome: OutcomeAlreadyMirrored}
		}
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
	var manifest []byte
	if manifestName := plugin.ManifestName(name, version); strings.Contains(string(sums), "  "+manifestName) {
		if manifest, err = i.upstream.fetchSmall(ctx, i.upstream.fileURL(product, version, manifestName), 1<<20); err != nil {
			return failed("%v", err)
		}
	}

	outcome := VersionOutcome{Version: version}
	var uploaded []plugin.UploadedZip
	blobs := map[string]store.PluginBlob{}
	var temporary []*os.File
	defer func() {
		for _, file := range temporary {
			_ = file.Close()
			_ = os.Remove(file.Name())
		}
	}()
	for _, platform := range platforms {
		var found *build
		for index := range files.Builds {
			if files.Builds[index].OS+"_"+files.Builds[index].Arch == platform {
				found = &files.Builds[index]
			}
		}
		if found == nil {
			outcome.Platforms = append(outcome.Platforms, PlatformOutcome{Platform: platform, Outcome: OutcomeFailed, Error: "not published upstream"})
			continue
		}
		file, err := os.CreateTemp("", "dufflebag-import-*")
		if err != nil {
			return failed("spool %s: %v", found.Filename, err)
		}
		temporary = append(temporary, file)
		digest, size, err := i.upstream.download(ctx, i.upstream.fileURL(product, version, found.Filename), file)
		if err != nil {
			outcome.Platforms = append(outcome.Platforms, PlatformOutcome{Platform: platform, Outcome: OutcomeFailed, Error: err.Error()})
			continue
		}
		archive, err := zip.NewReader(file, size)
		if err != nil {
			outcome.Platforms = append(outcome.Platforms, PlatformOutcome{Platform: platform, Outcome: OutcomeFailed, Error: "not a zip archive"})
			continue
		}
		entries := make([]string, 0, len(archive.File))
		for _, entry := range archive.File {
			entries = append(entries, entry.Name)
		}
		uploaded = append(uploaded, plugin.UploadedZip{Filename: found.Filename, SHA256: digest, Entries: entries})
		blobs[found.Filename] = store.PluginBlob{Filename: found.Filename, SHA256: digest, Body: file, Size: size}
		outcome.Platforms = append(outcome.Platforms, PlatformOutcome{Platform: platform, Outcome: OutcomeImported})
	}
	if len(uploaded) == 0 {
		outcome.Outcome, outcome.Error = OutcomeFailed, "none of the selected platforms could be fetched"
		return outcome
	}

	verified, err := plugin.Verify(plugin.Upload{Name: name, Version: version, Sums: sums, Manifest: manifest, Zips: uploaded})
	if err != nil {
		outcome.Outcome, outcome.Error = OutcomeFailed, strings.TrimPrefix(err.Error(), registry.ErrInvalid.Error()+": ")
		return outcome
	}
	input := store.PluginVersionInput{
		Name: name, Version: version, Protocol: verified.Protocol,
		Source: store.PluginSource{Kind: "releases-hashicorp", Repository: product},
		Sums:   newBlob(files.Shasums, sums),
		Signature: func() *store.PluginBlob {
			blob := newBlob(signatureName, signature)
			return &blob
		}(),
	}
	if manifest != nil {
		blob := newBlob(plugin.ManifestName(name, version), manifest)
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
