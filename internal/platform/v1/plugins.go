package v1

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/plugin"
	"github.com/benemon/dufflebag/internal/domain/registry"
	store "github.com/benemon/dufflebag/internal/store/postgres"
)

// DefaultPluginUploadBytes bounds one plugin version upload (ADR-0027 D12).
const DefaultPluginUploadBytes int64 = 512 << 20

// The server's read and write timeouts are sized for API calls; one upload
// may carry hundreds of megabytes over a slow link.
const pluginUploadDeadline = 30 * time.Minute

// Small files are read into memory for verification; a sums file, signature
// or manifest larger than this is not a real one.
const pluginMetadataBytes = 1 << 20

var pluginUploadPath = regexp.MustCompile(`^/api/v1/organizations/[^/]+/plugin-registry/plugins/[^/]+/versions/[^/]+$`)

type requestHostKey struct{}

// withPluginUpload bounds the upload route's body, extends its deadlines, and
// records the host the template stanza must name. The strict layer takes the
// body before any strict middleware runs, so this wraps the router instead.
func withPluginUpload(limit int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && pluginUploadPath.MatchString(r.URL.Path) {
			controller := http.NewResponseController(w)
			deadline := time.Now().Add(pluginUploadDeadline)
			_ = controller.SetReadDeadline(deadline)
			_ = controller.SetWriteDeadline(deadline)
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			r = r.WithContext(context.WithValue(r.Context(), requestHostKey{}, r.Host))
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) ListPlugins(
	ctx context.Context, request ListPluginsRequestObject,
) (ListPluginsResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	organizationID := request.OrganizationId.String()
	refused, err := s.admitPluginRead(ctx, audited, request.OrganizationId)
	if err != nil {
		return nil, err
	}
	if refused != permitted {
		return newRefusal(refused), nil
	}
	plugins, err := s.repository.ListPlugins(ctx, store.ParseOrganizationTenant(organizationID))
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(organizationID, "")
	response := ListPlugins200JSONResponse{Plugins: make([]Plugin, 0, len(plugins))}
	for _, summary := range plugins {
		rendered := Plugin{
			Name: summary.Name, Source: renderPluginSource(summary.Source),
			PublishedVersions: len(summary.PublishedVersions),
		}
		if newest := plugin.Newest(summary.PublishedVersions); newest != "" {
			rendered.NewestVersion = &newest
		}
		response.Plugins = append(response.Plugins, rendered)
	}
	return response, nil
}

func (s *server) ListPluginVersions(
	ctx context.Context, request ListPluginVersionsRequestObject,
) (ListPluginVersionsResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	organizationID := request.OrganizationId.String()
	refused, err := s.admitPluginRead(ctx, audited, request.OrganizationId)
	if err != nil {
		return nil, err
	}
	if refused != permitted {
		return newRefusal(refused), nil
	}
	source, versions, err := s.repository.ListPluginVersions(ctx, store.ParseOrganizationTenant(organizationID), request.PluginName)
	if errors.Is(err, registry.ErrNotFound) {
		audited.failed("not_found")
		return ListPluginVersions404JSONResponse{NotFoundJSONResponse: NotFoundJSONResponse{Message: "plugin not found"}}, nil
	}
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(organizationID, "")
	response := ListPluginVersions200JSONResponse{
		Name: request.PluginName, Source: renderPluginSource(source),
		Versions: make([]PluginVersion, 0, len(versions)),
	}
	slices.SortFunc(versions, func(a, b store.PluginVersionSummary) int { return plugin.Compare(b.Version, a.Version) })
	for _, version := range versions {
		response.Versions = append(response.Versions, renderPluginVersion(version))
	}
	return response, nil
}

// admitPluginRead mirrors GetPluginRegistry: reader visibility, and an
// absent organization answers like an invisible one.
func (s *server) admitPluginRead(
	ctx context.Context, audited *lifecycleAudit, organizationID OrganizationId,
) (refusal, error) {
	caller, refused := authorizeOrganizationVisibility(ctx, identity.RoleReader, organizationID)
	if refused != permitted {
		audited.refused(refused.reason())
		return refused, nil
	}
	if _, err := s.repository.GetOrganization(ctx, organizationID.String()); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			audited.refused(refusedTenancy.reason())
			return refusedTenancy, nil
		}
		audited.failed("storage_failed")
		return permitted, err
	}
	audited.actor(caller)
	return permitted, nil
}

func (s *server) PublishPluginVersion(
	ctx context.Context, request PublishPluginVersionRequestObject,
) (PublishPluginVersionResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	organizationID := request.OrganizationId.String()
	caller, refused, err := s.admitPluginRegistryMutation(ctx, identity.RolePublisher, organizationID)
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	if refused != permitted {
		audited.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audited.actor(caller)
	invalid := func(err error) PublishPluginVersionResponseObject {
		audited.failed("invalid_request")
		return PublishPluginVersion400JSONResponse{Message: strings.TrimPrefix(err.Error(), registry.ErrInvalid.Error()+": ")}
	}

	upload, err := spoolPluginUpload(request.Body)
	defer upload.remove()
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		audited.failed("body_too_large")
		return PublishPluginVersion413JSONResponse{Message: fmt.Sprintf("upload exceeds %d bytes", tooLarge.Limit)}, nil
	case errors.Is(err, registry.ErrInvalid):
		return invalid(err), nil
	case err != nil:
		audited.failed("storage_failed")
		return nil, err
	}

	verified, err := plugin.Verify(plugin.Upload{
		Name: request.PluginName, Version: request.Version,
		Sums: upload.sums.data, Manifest: upload.manifestData(), Zips: upload.uploadedZips(),
	})
	if err != nil {
		return invalid(err), nil
	}

	input := store.PluginVersionInput{
		Name: request.PluginName, Version: request.Version,
		Source: store.PluginSource{Kind: "upload"}, Protocol: verified.Protocol,
		Sums: upload.sums.blob(), Signature: upload.signature.optionalBlob(), Manifest: upload.manifest.optionalBlob(),
	}
	for _, platform := range verified.Listed {
		input.Listed = append(input.Listed, platform.OS+"_"+platform.Arch)
	}
	for _, zip := range verified.Zips {
		input.Zips = append(input.Zips, store.PluginZip{PluginBlob: upload.zips[zip.Filename].blob(), OS: zip.Platform.OS, Arch: zip.Platform.Arch})
	}
	err = s.repository.PublishPluginVersion(ctx, store.ParseOrganizationTenant(organizationID), input)
	var held store.HeldSourceError
	switch {
	case errors.Is(err, store.ErrPluginRegistryNotEnabled):
		audited.failed("not_enabled")
		return PublishPluginVersion409JSONResponse{Message: "plugin registry is not enabled"}, nil
	case errors.Is(err, store.ErrPluginVersionExists):
		audited.failed("version_exists")
		return PublishPluginVersion409JSONResponse{Message: fmt.Sprintf("%s %s already exists; versions are immutable", request.PluginName, request.Version)}, nil
	case errors.As(err, &held):
		audited.failed("source_held")
		return PublishPluginVersion409JSONResponse{Message: fmt.Sprintf(
			"%s is held by another source (%s); a name belongs to one source per organization and is freed when every version is removed",
			request.PluginName, describePluginSource(held.Source),
		)}, nil
	case errors.Is(err, store.ErrObjectStorageNotConfigured), errors.Is(err, store.ErrObjectStorageUnavailable):
		audited.failed("object_storage_unavailable")
		return PublishPluginVersion503JSONResponse{Message: "object storage is not configured or not reachable"}, nil
	case err != nil:
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(organizationID, "published")

	stored := make([]string, 0, len(input.Zips))
	for _, zip := range input.Zips {
		stored = append(stored, zip.OS+"_"+zip.Arch)
	}
	organization, err := s.repository.GetOrganization(ctx, organizationID)
	if err != nil {
		return nil, err
	}
	host, _ := ctx.Value(requestHostKey{}).(string)
	return PublishPluginVersion201JSONResponse{
		Version: renderPluginVersion(store.PluginVersionSummary{
			Version: request.Version, Listed: input.Listed, Stored: stored, CreatedAt: s.now(),
		}),
		Stanza: renderTemplateStanza(host, organization.Name, request.PluginName, request.Version),
	}, nil
}

// A source address cannot carry a port: Packer always dials 443.
func renderTemplateStanza(host, organization, name, version string) TemplateStanza {
	if hostname, _, err := net.SplitHostPort(host); err == nil {
		host = hostname
	}
	source := path.Join(host, "plugins", organization, name)
	return TemplateStanza{
		Source: source, Version: version,
		Hcl: fmt.Sprintf("packer {\n  required_plugins {\n    %s = {\n      source  = %q\n      version = %q\n    }\n  }\n}\n", name, source, version),
	}
}

func renderPluginSource(source store.PluginSource) PluginSource {
	rendered := PluginSource{Kind: PluginSourceKind(source.Kind)}
	if source.Repository != "" {
		rendered.Repository = &source.Repository
	}
	return rendered
}

func describePluginSource(source store.PluginSource) string {
	if source.Repository != "" {
		return source.Kind + " " + source.Repository
	}
	return source.Kind
}

func renderPluginVersion(version store.PluginVersionSummary) PluginVersion {
	platforms := func(names []string) []PluginPlatform {
		rendered := make([]PluginPlatform, 0, len(names))
		for _, name := range names {
			os, arch, _ := strings.Cut(name, "_")
			rendered = append(rendered, PluginPlatform{Os: os, Arch: arch})
		}
		return rendered
	}
	return PluginVersion{
		Version: version.Version, Revoked: version.Revoked, CreatedAt: version.CreatedAt,
		ListedPlatforms: platforms(version.Listed), StoredPlatforms: platforms(version.Stored),
	}
}

// spooledFile is one uploaded part written to disk while its digest is taken.
type spooledFile struct {
	filename string
	file     *os.File
	digest   string
	size     int64
	data     []byte
}

func (f *spooledFile) blob() store.PluginBlob {
	return store.PluginBlob{Filename: f.filename, SHA256: f.digest, Body: f.file, Size: f.size}
}

func (f *spooledFile) optionalBlob() *store.PluginBlob {
	if f == nil {
		return nil
	}
	blob := f.blob()
	return &blob
}

type pluginUpload struct {
	sums      *spooledFile
	signature *spooledFile
	manifest  *spooledFile
	zips      map[string]*spooledFile
	order     []string
	entries   map[string][]string
	files     []*os.File
}

func (u *pluginUpload) remove() {
	for _, file := range u.files {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}
}

func (u *pluginUpload) manifestData() []byte {
	if u.manifest == nil {
		return nil
	}
	return u.manifest.data
}

func (u *pluginUpload) uploadedZips() []plugin.UploadedZip {
	zips := make([]plugin.UploadedZip, 0, len(u.order))
	for _, name := range u.order {
		zips = append(zips, plugin.UploadedZip{Filename: name, SHA256: u.zips[name].digest, Entries: u.entries[name]})
	}
	return zips
}

func uploadRefusal(file, format string, args ...any) error {
	return fmt.Errorf("%w: %s: %s", registry.ErrInvalid, file, fmt.Sprintf(format, args...))
}

// spoolPluginUpload writes each part to a temporary file, hashing as it goes.
// The form fields are the closed allowlist of ADR-0027 D11.
func spoolPluginUpload(body *multipart.Reader) (*pluginUpload, error) {
	upload := &pluginUpload{zips: map[string]*spooledFile{}, entries: map[string][]string{}}
	if body == nil {
		return upload, uploadRefusal("request", "a multipart body is required")
	}
	for {
		part, err := body.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return upload, err
		}
		field := part.FormName()
		spooled, err := upload.spool(part)
		_ = part.Close()
		if err != nil {
			return upload, err
		}
		switch field {
		case "sha256sums", "sha256sums_sig", "manifest":
			target := map[string]**spooledFile{"sha256sums": &upload.sums, "sha256sums_sig": &upload.signature, "manifest": &upload.manifest}[field]
			if *target != nil {
				return upload, uploadRefusal(field, "is uploaded twice")
			}
			if spooled.size > pluginMetadataBytes {
				return upload, uploadRefusal(field, "exceeds %d bytes", pluginMetadataBytes)
			}
			if spooled.data, err = io.ReadAll(io.NewSectionReader(spooled.file, 0, spooled.size)); err != nil {
				return upload, err
			}
			*target = spooled
		case "zips":
			if spooled.filename == "" {
				return upload, uploadRefusal("zips", "every zip needs a filename")
			}
			if _, duplicate := upload.zips[spooled.filename]; duplicate {
				return upload, uploadRefusal(spooled.filename, "is uploaded twice")
			}
			archive, err := zip.NewReader(spooled.file, spooled.size)
			if err != nil {
				return upload, uploadRefusal(spooled.filename, "is not a zip archive")
			}
			for _, entry := range archive.File {
				upload.entries[spooled.filename] = append(upload.entries[spooled.filename], entry.Name)
			}
			upload.zips[spooled.filename] = spooled
			upload.order = append(upload.order, spooled.filename)
		default:
			return upload, uploadRefusal(field, "is not one of sha256sums, sha256sums_sig, manifest or zips")
		}
	}
	if upload.sums == nil {
		return upload, uploadRefusal("sha256sums", "is required")
	}
	return upload, nil
}

func (u *pluginUpload) spool(part *multipart.Part) (*spooledFile, error) {
	file, err := os.CreateTemp("", "dufflebag-plugin-*")
	if err != nil {
		return nil, err
	}
	u.files = append(u.files, file)
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, hash), part)
	if err != nil {
		return nil, err
	}
	return &spooledFile{filename: part.FileName(), file: file, digest: hex.EncodeToString(hash.Sum(nil)), size: size}, nil
}
