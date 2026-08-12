package hcp2023

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/compat/hcp2023/models"
	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/go-openapi/strfmt"
)

const (
	basePath = "/packer/2023-01-01/organizations/{organization}/projects/{project}"
	// DefaultMaxRequestBodyBytes is the request-body limit for the Packer compatibility plane.
	DefaultMaxRequestBodyBytes = 4 << 20
)

// Repository is the domain-facing storage contract used by the adapter.
type Repository interface {
	CreateBucket(context.Context, store.Tenant, store.Bucket) (*store.Bucket, error)
	GetBucket(context.Context, store.Tenant, string) (*store.Bucket, error)
	GetBucketWithLatestVersion(context.Context, store.Tenant, string) (*store.Bucket, error)
	ListBuckets(context.Context, store.Tenant) ([]store.Bucket, error)
	UpdateBucket(context.Context, store.Tenant, string, string, map[string]string, time.Time) (*store.Bucket, error)
	DeleteBucket(context.Context, store.Tenant, string) error
	CreateChannel(context.Context, store.Tenant, store.Channel, string, string) (*store.Channel, error)
	GetChannel(context.Context, store.Tenant, string, string) (*store.Channel, error)
	ListChannels(context.Context, store.Tenant, string) ([]store.Channel, error)
	UpdateChannel(context.Context, store.Tenant, string, string, bool, bool, bool, string, string, time.Time) (*store.Channel, error)
	AssignChannelVersion(context.Context, store.Tenant, string, string, string, string, time.Time) (*store.Channel, *store.Channel, error)
	DeleteChannel(context.Context, store.Tenant, string, string) error
	ListChannelAssignmentHistory(context.Context, store.Tenant, string, string) ([]store.ChannelAssignment, error)
	ListBucketAncestry(context.Context, store.Tenant, string, string, string, string) ([]store.BucketAncestry, error)
	CreateVersion(context.Context, store.Tenant, *registry.Version) (*registry.Version, error)
	GetVersion(context.Context, store.Tenant, string, string) (*registry.Version, error)
	ListVersions(context.Context, store.Tenant, string) ([]*registry.Version, error)
	CreateBuild(context.Context, store.Tenant, string, string, registry.TemplateType, store.StoredBuild) (*store.StoredBuild, error)
	ListBuilds(context.Context, store.Tenant, string, string) ([]store.StoredBuild, error)
	GetBuild(context.Context, store.Tenant, string, string, string) (*store.StoredBuild, error)
	UpdateBuild(context.Context, store.Tenant, string, string, store.StoredBuild, time.Time) (*store.StoredBuild, error)
	RevokeVersion(context.Context, store.Tenant, string, string, store.RevocationRequest, func(*registry.Version) string, time.Time) (*registry.Version, error)
	RestoreRevokedVersion(context.Context, store.Tenant, string, string, time.Time) (*registry.Version, error)
	UploadSbom(context.Context, store.Tenant, string, string, string, store.Sbom) (*store.Sbom, error)
	ListSboms(context.Context, store.Tenant, string, string, string) ([]store.Sbom, error)
	GetSbom(context.Context, store.Tenant, string, string, string, string) (*store.Sbom, error)
	DownloadSbom(context.Context, store.Tenant, string, string, string, string) ([]byte, error)
	ListBuildPackages(context.Context, store.Tenant, string, string, string) ([]store.ReportedPackage, []string, error)
	GetBuildScanState(context.Context, store.Tenant, string) (*store.BuildScanState, error)
	GetScanRun(context.Context, store.Tenant, string) (*store.ScanRun, error)
	ListScanFindings(context.Context, store.Tenant, string) ([]store.StoredFinding, error)
}

type handler struct {
	repository   Repository
	principals   Principals
	logger       *slog.Logger
	now          func() time.Time
	maxBodyBytes int64
}

// route is one compatibility endpoint and the authority it demands.
//
// Declared as data rather than as a list of HandleFunc calls so tests can
// iterate the SAME table the server registers from. A route added without a
// role case is then a test failure rather than a silent gap — which is how six
// write routes came to be downgradeable to reader with every suite still green
// (review finding 18b).
type route struct {
	method        string
	path          string
	notFoundCode  int32
	required      identity.Role
	operation     identity.AuditOperation
	targetType    string
	targetIDParam string
	handle        func(*handler, http.ResponseWriter, *http.Request)
}

func routes() []route {
	return []route{
		{method: http.MethodGet, path: "/registry", notFoundCode: 5, required: identity.RoleReader, operation: "registry.read", targetType: "registry", handle: (*handler).getRegistry},
		{method: http.MethodGet, path: "/enforced_blocks/bucket/{bucket}", notFoundCode: 5, required: identity.RoleReader, operation: "enforced_block.list", targetType: "bucket", targetIDParam: "bucket", handle: (*handler).getEnforcedBlocksByBucket},
		{method: http.MethodGet, path: "/buckets", notFoundCode: 5, required: identity.RoleReader, operation: "bucket.list", targetType: "bucket_collection", handle: (*handler).listBuckets},
		{method: http.MethodGet, path: "/buckets/{bucket}", notFoundCode: 5, required: identity.RoleReader, operation: "bucket.read", targetType: "bucket", targetIDParam: "bucket", handle: (*handler).getBucket},
		{method: http.MethodPut, path: "/buckets", notFoundCode: 5, required: identity.RoleBuilder, operation: "bucket.create", targetType: "bucket", handle: (*handler).createBucket},
		{method: http.MethodPatch, path: "/buckets/{bucket}", notFoundCode: 5, required: identity.RoleBuilder, operation: "bucket.update", targetType: "bucket", targetIDParam: "bucket", handle: (*handler).updateBucket},
		// Publisher, like DeleteChannel: destroying registry data sits with the
		// authority that blesses it, and the Terraform principal that applied a
		// bucket (ADR-0013) must be able to destroy it without a second identity.
		{method: http.MethodDelete, path: "/buckets/{bucket}", notFoundCode: 5, required: identity.RolePublisher, operation: "bucket.delete", targetType: "bucket", targetIDParam: "bucket", handle: (*handler).deleteBucket},
		{method: http.MethodGet, path: "/buckets/{bucket}/ancestry", notFoundCode: 5, required: identity.RoleReader, operation: "bucket_ancestry.list", targetType: "bucket", targetIDParam: "bucket", handle: (*handler).listBucketAncestry},
		{method: http.MethodGet, path: "/buckets/{bucket}/channels", notFoundCode: 5, required: identity.RoleReader, operation: "channel.list", targetType: "channel_collection", handle: (*handler).listChannels},
		{method: http.MethodPost, path: "/buckets/{bucket}/channels", notFoundCode: 5, required: identity.RolePublisher, operation: "channel.create", targetType: "channel", handle: (*handler).createChannel},
		{method: http.MethodPost, path: "/buckets/{bucket}/channels/assign", notFoundCode: 5, required: identity.RolePublisher, operation: "channel.assign", targetType: "channel", handle: (*handler).assignChannelVersion},
		{method: http.MethodGet, path: "/buckets/{bucket}/channels/{channel}", notFoundCode: 5, required: identity.RoleReader, operation: "channel.read", targetType: "channel", targetIDParam: "channel", handle: (*handler).getChannel},
		{method: http.MethodPatch, path: "/buckets/{bucket}/channels/{channel}", notFoundCode: 5, required: identity.RolePublisher, operation: "channel.update", targetType: "channel", targetIDParam: "channel", handle: (*handler).updateChannel},
		{method: http.MethodDelete, path: "/buckets/{bucket}/channels/{channel}", notFoundCode: 5, required: identity.RolePublisher, operation: "channel.delete", targetType: "channel", targetIDParam: "channel", handle: (*handler).deleteChannel},
		{method: http.MethodGet, path: "/buckets/{bucket}/channels/{channel}/history", notFoundCode: 5, required: identity.RoleReader, operation: "channel_history.list", targetType: "channel", targetIDParam: "channel", handle: (*handler).listChannelAssignmentHistory},
		{method: http.MethodGet, path: "/buckets/{bucket}/packages/vulnerability-summary", notFoundCode: 5, required: identity.RoleReader, operation: "vulnerability.summary", targetType: "bucket", targetIDParam: "bucket", handle: (*handler).listBucketPackagesVulnerabilitySummary},
		{method: http.MethodGet, path: "/buckets/{bucket}/packages/with-vulnerabilities", notFoundCode: 5, required: identity.RoleReader, operation: "vulnerability.package_list", targetType: "bucket", targetIDParam: "bucket", handle: (*handler).listBucketPackagesWithVulnerabilities},
		{method: http.MethodGet, path: "/buckets/{bucket}/vulnerabilities", notFoundCode: 5, required: identity.RoleReader, operation: "vulnerability.list", targetType: "bucket", targetIDParam: "bucket", handle: (*handler).listBucketVulnerabilities},
		{method: http.MethodGet, path: "/buckets/{bucket}/versions", notFoundCode: 5, required: identity.RoleReader, operation: "version.list", targetType: "version_collection", handle: (*handler).listVersions},
		{method: http.MethodGet, path: "/buckets/{bucket}/versions/{fingerprint}", notFoundCode: 10, required: identity.RoleReader, operation: "version.read", targetType: "version", targetIDParam: "fingerprint", handle: (*handler).getVersion},
		{method: http.MethodPost, path: "/buckets/{bucket}/versions", notFoundCode: 5, required: identity.RoleBuilder, operation: "version.create", targetType: "version", handle: (*handler).createVersion},
		// Publisher, like channel writes: revocation changes what consumers may
		// use, which is the same authority that blesses a version onto a channel.
		{method: http.MethodPatch, path: "/buckets/{bucket}/versions/{fingerprint}", notFoundCode: 10, required: identity.RolePublisher, operation: "version.update", targetType: "version", targetIDParam: "fingerprint", handle: (*handler).updateVersion},
		{method: http.MethodPost, path: "/buckets/{bucket}/versions/{fingerprint}/builds", notFoundCode: 10, required: identity.RoleBuilder, operation: "build.create", targetType: "build", handle: (*handler).createBuild},
		{method: http.MethodGet, path: "/buckets/{bucket}/versions/{fingerprint}/builds", notFoundCode: 10, required: identity.RoleReader, operation: "build.list", targetType: "build_collection", handle: (*handler).listBuilds},
		{method: http.MethodPatch, path: "/buckets/{bucket}/versions/{fingerprint}/builds/{build}", notFoundCode: 10, required: identity.RoleBuilder, operation: "build.update", targetType: "build", targetIDParam: "build", handle: (*handler).updateBuild},
		{method: http.MethodGet, path: "/buckets/{bucket}/versions/{fingerprint}/builds/{build}/packages", notFoundCode: 10, required: identity.RoleReader, operation: "package.list", targetType: "build", targetIDParam: "build", handle: (*handler).listBuildPackages},
		{method: http.MethodGet, path: "/buckets/{bucket}/versions/{fingerprint}/builds/{build}/sboms", notFoundCode: 10, required: identity.RoleReader, operation: "sbom.list", targetType: "build", targetIDParam: "build", handle: (*handler).listSboms},
		{method: http.MethodPut, path: "/buckets/{bucket}/versions/{fingerprint}/builds/{build}/sboms", notFoundCode: 10, required: identity.RoleBuilder, operation: "sbom.upload", targetType: "build", targetIDParam: "build", handle: (*handler).uploadSbom},
		{method: http.MethodGet, path: "/buckets/{bucket}/versions/{fingerprint}/builds/{build}/sboms/{sbom}", notFoundCode: 10, required: identity.RoleReader, operation: "sbom.read", targetType: "sbom", targetIDParam: "sbom", handle: (*handler).getSbom},
		{method: http.MethodGet, path: "/buckets/{bucket}/versions/{fingerprint}/builds/{build}/sboms/{sbom}/download", notFoundCode: 10, required: identity.RoleReader, operation: "sbom.download", targetType: "sbom", targetIDParam: "sbom", handle: (*handler).downloadSbom},
	}
}

type resolvedHandler struct {
	http.Handler
	descriptors *http.ServeMux
}

type describedRoute struct{ descriptor audit.Descriptor }

func (h describedRoute) ServeHTTP(http.ResponseWriter, *http.Request) {}

func (h *resolvedHandler) Resolve(r *http.Request) audit.Descriptor {
	handler, pattern := h.descriptors.Handler(r)
	if described, ok := handler.(describedRoute); ok {
		descriptor := described.descriptor
		if descriptor.TargetIDParam != "" {
			descriptor.TargetID = audit.PathValue(pattern, r.URL.Path, descriptor.TargetIDParam)
		}
		return descriptor
	}
	return audit.Descriptor{
		Operation: "request.unimplemented", TargetType: "request",
		HandlerlessReason: "unimplemented",
	}
}

// NewHandler serves the Packer 2023 compatibility endpoints.
//
// The authenticator is required rather than optional: an unauthenticated
// registry has no tenancy boundary at all, so there is no configuration in which
// omitting it is correct.
func NewHandler(
	repository *store.Repository, auth Authenticator, logger *slog.Logger, maxBodyBytes int64,
) http.Handler {
	return newHandlerWithMaxBody(repository, repository, auth, logger, time.Now, maxBodyBytes)
}

// NewHandlerWithRepository supports contract tests and alternate storage
// implementations without exposing wire models beyond this package.
func NewHandlerWithRepository(
	repository Repository, principals Principals, auth Authenticator, logger *slog.Logger,
) http.Handler {
	return newHandler(repository, principals, auth, logger, time.Now)
}

func newHandler(
	repository Repository, principals Principals, auth Authenticator,
	logger *slog.Logger, now func() time.Time,
) http.Handler {
	return newHandlerWithMaxBody(
		repository, principals, auth, logger, now, DefaultMaxRequestBodyBytes,
	)
}

func newHandlerWithMaxBody(
	repository Repository, principals Principals, auth Authenticator,
	logger *slog.Logger, now func() time.Time, maxBodyBytes int64,
) http.Handler {
	h := &handler{
		repository:   repository,
		principals:   principals,
		logger:       logger,
		now:          now,
		maxBodyBytes: maxBodyBytes,
	}
	mux := http.NewServeMux()
	descriptors := http.NewServeMux()
	for _, route := range routes() {
		mux.HandleFunc(
			route.method+" "+basePath+route.path,
			h.scoped(route, func(w http.ResponseWriter, r *http.Request) {
				route.handle(h, w, r)
			}),
		)
		descriptors.Handle(route.method+" "+basePath+route.path, describedRoute{descriptor: audit.Descriptor{
			RouteID: string(route.operation), Operation: route.operation,
			TargetType: route.targetType, TargetIDParam: route.targetIDParam,
		}})
	}
	// Whatever no route serves must still answer a google.rpc.Status body:
	// Packer regex-matches the error TEXT for a code, and http.ServeMux's
	// text/plain "404 page not found" matches nothing, turning "unimplemented"
	// into "undiagnosable" (review finding 7). Code 12 Unimplemented — the
	// plane refuses what it does not serve, and 12 is a code clients already
	// tolerate where they tolerate anything (dossier §6, GetEnforcedBlocks).
	// This also catches method mismatches, which would otherwise be ServeMux's
	// text/plain 405.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		audit.FromContext(r.Context()).Enrich(audit.Enrichment{
			Outcome: identity.AuditOutcomeRefused, Reason: "unimplemented",
		})
		writeRPCError(w, http.StatusNotImplemented, 12, "unimplemented")
	})
	descriptors.Handle("/", describedRoute{descriptor: audit.Descriptor{
		RouteID: "request.unimplemented", Operation: "request.unimplemented",
		TargetType: "request", HandlerlessReason: "unimplemented",
	}})
	// Authentication wraps every route, so a route added later is protected
	// without anyone remembering to protect it.
	return &resolvedHandler{Handler: authenticate(auth, mux), descriptors: descriptors}
}

func (h *handler) listBuckets(w http.ResponseWriter, r *http.Request) {
	buckets, err := h.repository.ListBuckets(r.Context(), tenant(r))
	if err != nil {
		h.writeInternal(w, r, "compat request failed", err)
		return
	}
	wireBuckets := make([]*models.HashicorpCloudPacker20230101Bucket, 0, len(buckets))
	for i := range buckets {
		wire, err := renderBucket(tenant(r), &buckets[i], h.now().UTC())
		if err != nil {
			h.writeInternal(w, r, "render bucket", err)
			return
		}
		wireBuckets = append(wireBuckets, wire)
	}
	writeJSON(w, http.StatusOK, &models.HashicorpCloudPacker20230101ListBucketsResponse{
		Buckets:    wireBuckets,
		Pagination: &models.HashicorpCloudCommonPaginationResponse{},
	})
}

func (h *handler) getBucket(w http.ResponseWriter, r *http.Request) {
	bucket, err := h.repository.GetBucketWithLatestVersion(
		r.Context(), tenant(r), r.PathValue("bucket"),
	)
	if errors.Is(err, registry.ErrNotFound) {
		writeBucketNotFound(w, r.PathValue("bucket"))
		return
	}
	if err != nil {
		h.writeInternal(w, r, "compat request failed", err)
		return
	}
	wire, err := renderBucket(tenant(r), bucket, h.now().UTC())
	if err != nil {
		h.writeInternal(w, r, "render bucket", err)
		return
	}
	writeJSON(w, http.StatusOK, &models.HashicorpCloudPacker20230101GetBucketResponse{Bucket: wire})
}

func (h *handler) getChannel(w http.ResponseWriter, r *http.Request) {
	channel, err := h.repository.GetChannel(
		r.Context(), tenant(r), r.PathValue("bucket"), r.PathValue("channel"),
	)
	if !h.writeChannelError(w, r, r.PathValue("channel"), err) {
		return
	}
	wire, err := renderChannel(tenant(r), channel, h.now().UTC())
	if err != nil {
		h.writeInternal(w, r, "render channel", err)
		return
	}
	writeJSON(w, http.StatusOK, &models.HashicorpCloudPacker20230101GetChannelResponse{
		Channel: wire,
	})
}

func (h *handler) listChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := h.repository.ListChannels(r.Context(), tenant(r), r.PathValue("bucket"))
	if errors.Is(err, registry.ErrNotFound) {
		writeBucketNotFound(w, r.PathValue("bucket"))
		return
	}
	if err != nil {
		h.writeInternal(w, r, "compat request failed", err)
		return
	}
	wireChannels := make([]*models.HashicorpCloudPacker20230101Channel, 0, len(channels))
	for i := range channels {
		wire, err := renderChannel(tenant(r), &channels[i], h.now().UTC())
		if err != nil {
			h.writeInternal(w, r, "render channel", err)
			return
		}
		wireChannels = append(wireChannels, wire)
	}
	writeJSON(w, http.StatusOK, &models.HashicorpCloudPacker20230101ListChannelsResponse{
		Channels: wireChannels,
	})
}

func (h *handler) createChannel(w http.ResponseWriter, r *http.Request) {
	var body models.HashicorpCloudPacker20230101CreateChannelBody
	if !h.decodeBody(w, r, &body) {
		return
	}
	if body.Name == "" {
		writeRPCError(w, http.StatusBadRequest, 3, "channel name is required")
		return
	}
	if _, err := h.repository.GetBucket(r.Context(), tenant(r), r.PathValue("bucket")); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			writeBucketNotFound(w, r.PathValue("bucket"))
		} else {
			h.writeInternal(w, r, "compat request failed", err)
		}
		return
	}
	if body.VersionFingerprint != "" &&
		!h.requireVersion(w, r, body.VersionFingerprint) {
		return
	}
	at := h.now().UTC()
	channel, err := h.repository.CreateChannel(r.Context(), tenant(r), store.Channel{
		ID:         registry.NewID(at),
		BucketName: r.PathValue("bucket"),
		Name:       body.Name,
		Restricted: body.Restricted,
		CreatedAt:  at,
	}, body.VersionFingerprint, principalID(r))
	if errors.Is(err, store.ErrChannelExists) {
		// Live probe 38 settles the provider adoption branch's typed error:
		// duplicate channels are HTTP 409 / AlreadyExists code 6.
		writeRPCError(w, http.StatusConflict, 6, fmt.Sprintf(
			"Error: The channel with identifier %s already exists.", body.Name,
		))
		return
	}
	if errors.Is(err, registry.ErrConflict) {
		writeRPCError(w, http.StatusBadRequest, 9, err.Error())
		return
	}
	if err != nil {
		h.writeInternal(w, r, "compat request failed", err)
		return
	}
	wire, err := renderChannel(tenant(r), channel, h.now().UTC())
	if err != nil {
		h.writeInternal(w, r, "render channel", err)
		return
	}
	writeJSON(w, http.StatusOK, &models.HashicorpCloudPacker20230101CreateChannelResponse{
		Channel: wire,
	})
}

func (h *handler) updateChannel(w http.ResponseWriter, r *http.Request) {
	var body models.HashicorpCloudPacker20230101UpdateChannelBody
	if !h.decodeBody(w, r, &body) {
		return
	}
	// The one place live HCP is strict where the spec is silent: UpdateChannel
	// requires update_mask, refused before the channel is even looked up — the
	// probe's only observed error with non-empty details (Appendix A probe 15,
	// dossier §4a; duf-7cy).
	if body.UpdateMask == "" {
		writeUpdateMaskRequired(w)
		return
	}
	updateRestricted, updateVersion, ok := channelUpdateFields(body.UpdateMask)
	if !ok {
		writeRPCError(w, http.StatusBadRequest, 3, "update_mask contains an unknown channel field")
		return
	}
	channel, err := h.repository.GetChannel(
		r.Context(), tenant(r), r.PathValue("bucket"), r.PathValue("channel"),
	)
	if !h.writeChannelError(w, r, r.PathValue("channel"), err) {
		return
	}
	// Managed channels are the service's own to move; live refuses the update
	// with 400 code 9 in this message structure (Appendix A probe 19). The
	// closing words deviate deliberately — live says "managed by HCP Packer",
	// a trademark this server must not emit as its own prose; status, code and
	// structure stay byte-faithful, and no client matches the text (§5.1).
	if channel.Managed {
		writeRPCError(w, http.StatusBadRequest, 9, fmt.Sprintf(
			"Can't update channel assignment on channel %q. This channel is managed by Dufflebag",
			channel.Name,
		))
		return
	}
	// An empty fingerprint under the versionFingerprint mask means CLEAR the
	// assignment, not a malformed request. The provider's destroy path calls
	// UpdatePackerChannelAssignment with an empty fingerprint, and its
	// internal/clients/packerv2/channel.go always includes versionFingerprint in
	// the mask — so refusing this broke every terraform destroy of an
	// hcp_packer_channel_assignment (duf-8em).
	if updateVersion && body.VersionFingerprint != "" {
		if !h.requireVersion(w, r, body.VersionFingerprint) {
			return
		}
	}
	channel, err = h.repository.UpdateChannel(
		r.Context(),
		tenant(r),
		r.PathValue("bucket"),
		r.PathValue("channel"),
		updateRestricted,
		body.Restricted,
		updateVersion,
		body.VersionFingerprint,
		principalID(r),
		h.now().UTC(),
	)
	if !h.writeChannelError(w, r, r.PathValue("channel"), err) {
		return
	}
	wire, err := renderChannel(tenant(r), channel, h.now().UTC())
	if err != nil {
		h.writeInternal(w, r, "render channel", err)
		return
	}
	writeJSON(w, http.StatusOK, &models.HashicorpCloudPacker20230101UpdateChannelResponse{
		Channel: wire,
	})
}

func (h *handler) assignChannelVersion(w http.ResponseWriter, r *http.Request) {
	var body models.HashicorpCloudPacker20230101AssignChannelVersionBody
	if !h.decodeBody(w, r, &body) {
		return
	}
	if body.SourceChannel == "" || body.TargetChannel == "" {
		writeRPCError(w, http.StatusBadRequest, 3, "source and target channels are required")
		return
	}
	var target *store.Channel
	for _, name := range []string{body.SourceChannel, body.TargetChannel} {
		channel, err := h.repository.GetChannel(
			r.Context(), tenant(r), r.PathValue("bucket"), name,
		)
		if !h.writeChannelError(w, r, name, err) {
			return
		}
		if name == body.TargetChannel {
			target = channel
		}
	}
	// Defence in depth with the repository. Probe 40 observed this distinct
	// assign endpoint directly; unlike UpdateChannel's longer refusal, its exact
	// live prose is "Cannot assign to managed channel 'latest'".
	if target.Managed {
		writeManagedAssignmentRefusal(w, target.Name)
		return
	}
	source, target, err := h.repository.AssignChannelVersion(
		r.Context(),
		tenant(r),
		r.PathValue("bucket"),
		body.SourceChannel,
		body.TargetChannel,
		principalID(r),
		h.now().UTC(),
	)
	if !h.writeChannelError(w, r, body.TargetChannel, err) {
		return
	}
	wireSource, err := renderChannel(tenant(r), source, h.now().UTC())
	if err != nil {
		h.writeInternal(w, r, "render source channel", err)
		return
	}
	wireTarget, err := renderChannel(tenant(r), target, h.now().UTC())
	if err != nil {
		h.writeInternal(w, r, "render target channel", err)
		return
	}
	wireVersion, err := renderVersion(tenant(r), target.Version, nil, h.now().UTC())
	if err != nil {
		h.writeInternal(w, r, "render version", err)
		return
	}
	writeJSON(w, http.StatusOK, &models.HashicorpCloudPacker20230101AssignChannelVersionResponse{
		Fingerprint:   target.Version.Fingerprint,
		SourceChannel: wireSource,
		TargetChannel: wireTarget,
		Version:       wireVersion,
	})
}

func (h *handler) deleteChannel(w http.ResponseWriter, r *http.Request) {
	channel, err := h.repository.GetChannel(
		r.Context(), tenant(r), r.PathValue("bucket"), r.PathValue("channel"),
	)
	if !h.writeChannelError(w, r, r.PathValue("channel"), err) {
		return
	}
	// Live refuses deleting a managed channel with 400 code 3 — not the code 9
	// its update refusal carries; the asymmetry is real and captured verbatim
	// (Appendix A probe 17). "controlled by Dufflebag" deviates deliberately
	// from the captured "controlled by HCP Packer" — trademark, verified inert
	// (§5.1, dossier §7 deviation note); status, code and structure unchanged.
	if channel.Managed {
		writeRPCError(w, http.StatusBadRequest, 3, fmt.Sprintf(
			"Can't delete managed channel %s, it's controlled by Dufflebag", channel.Name,
		))
		return
	}
	err = h.repository.DeleteChannel(
		r.Context(), tenant(r), r.PathValue("bucket"), r.PathValue("channel"),
	)
	if !h.writeChannelError(w, r, r.PathValue("channel"), err) {
		return
	}
	var response models.HashicorpCloudPacker20230101DeleteChannelResponse = struct{}{}
	writeJSON(w, http.StatusOK, response)
}

func (h *handler) listChannelAssignmentHistory(w http.ResponseWriter, r *http.Request) {
	_, err := h.repository.GetChannel(
		r.Context(), tenant(r), r.PathValue("bucket"), r.PathValue("channel"),
	)
	if !h.writeChannelError(w, r, r.PathValue("channel"), err) {
		return
	}
	history, err := h.repository.ListChannelAssignmentHistory(
		r.Context(), tenant(r), r.PathValue("bucket"), r.PathValue("channel"),
	)
	if !h.writeChannelError(w, r, r.PathValue("channel"), err) {
		return
	}
	start, end, pagination, err := paginationPage(r, len(history))
	if err != nil {
		writeRPCError(w, http.StatusBadRequest, 3, err.Error())
		return
	}
	wireHistory := make([]*models.HashicorpCloudPacker20230101ChannelAssignment, 0, end-start)
	for i := start; i < end; i++ {
		version, err := renderVersion(tenant(r), history[i].Version, nil, h.now().UTC())
		if err != nil {
			h.writeInternal(w, r, "render version", err)
			return
		}
		wireHistory = append(wireHistory, &models.HashicorpCloudPacker20230101ChannelAssignment{
			AssignedAt: strfmt.DateTime(history[i].AssignedAt),
			AuthorID:   history[i].AuthorID,
			Version:    version,
		})
	}
	writeJSON(w, http.StatusOK, &models.HashicorpCloudPacker20230101ListChannelAssignmentHistoryResponse{
		Count:      int32(len(history)),
		History:    wireHistory,
		Pagination: pagination,
	})
}

func (h *handler) listBucketAncestry(w http.ResponseWriter, r *http.Request) {
	ancestryType := r.URL.Query().Get("type")
	if ancestryType == "" {
		ancestryType = string(models.HashicorpCloudPacker20230101BucketAncestryTypeANCESTRYTYPEUNSET)
	}
	switch models.HashicorpCloudPacker20230101BucketAncestryType(ancestryType) {
	case models.HashicorpCloudPacker20230101BucketAncestryTypeANCESTRYTYPEUNSET,
		models.HashicorpCloudPacker20230101BucketAncestryTypeANCESTRYTYPEPARENTS,
		models.HashicorpCloudPacker20230101BucketAncestryTypeANCESTRYTYPECHILDREN:
	default:
		writeRPCError(w, http.StatusBadRequest, 3, "invalid ancestry type")
		return
	}
	relations, err := h.repository.ListBucketAncestry(
		r.Context(), tenant(r), r.PathValue("bucket"), ancestryType,
		r.URL.Query().Get("channel_name"), r.URL.Query().Get("version_fingerprint"),
	)
	if errors.Is(err, registry.ErrNotFound) {
		writeBucketNotFound(w, r.PathValue("bucket"))
		return
	}
	if err != nil {
		h.writeInternal(w, r, "list bucket ancestry", err)
		return
	}
	start, end, pagination, err := paginationPage(r, len(relations))
	if err != nil {
		writeRPCError(w, http.StatusBadRequest, 3, err.Error())
		return
	}
	wireRelations := make([]*models.HashicorpCloudPacker20230101BucketAncestry, 0, end-start)
	for i := start; i < end; i++ {
		wireRelations = append(wireRelations, renderBucketAncestry(&relations[i]))
	}
	writeJSON(w, http.StatusOK, &models.HashicorpCloudPacker20230101ListBucketAncestryResponse{
		Relations:  wireRelations,
		TotalCount: int32(len(relations)),
		Pagination: pagination,
	})
}

func (h *handler) createBucket(w http.ResponseWriter, r *http.Request) {
	var body models.HashicorpCloudPacker20230101CreateBucketBody
	if !h.decodeBody(w, r, &body) {
		return
	}
	if body.Name == "" {
		writeRPCError(w, http.StatusBadRequest, 3, "bucket name is required")
		return
	}
	at := h.now().UTC()
	bucket, err := h.repository.CreateBucket(r.Context(), tenant(r), store.Bucket{
		ID:          registry.NewID(at),
		Name:        body.Name,
		Description: body.Description,
		Labels:      body.Labels,
		CreatedAt:   at,
	})
	if errors.Is(err, registry.ErrConflict) {
		writeRPCError(w, http.StatusConflict, 6, fmt.Sprintf(
			"Error: The bucket with identifier %s already exists.", body.Name,
		))
		return
	}
	if err != nil {
		// Anything else is a failure, not a conflict. Reporting an outage as
		// AlreadyExists is worse than reporting it as an error, because Packer's
		// upsert TOLERATES AlreadyExists — so a database outage would be
		// misclassified as success-adjacent at the opening step of every build.
		h.writeInternal(w, r, "create bucket", err)
		return
	}
	wire, err := renderBucket(tenant(r), bucket, h.now().UTC())
	if err != nil {
		h.writeInternal(w, r, "render bucket", err)
		return
	}
	writeJSON(w, http.StatusOK, &models.HashicorpCloudPacker20230101CreateBucketResponse{Bucket: wire})
}

func (h *handler) updateBucket(w http.ResponseWriter, r *http.Request) {
	var body models.HashicorpCloudPacker20230101UpdateBucketBody
	if !h.decodeBody(w, r, &body) {
		return
	}
	bucket, err := h.repository.UpdateBucket(
		r.Context(),
		tenant(r),
		r.PathValue("bucket"),
		body.Description,
		body.Labels,
		h.now().UTC(),
	)
	if errors.Is(err, registry.ErrNotFound) {
		writeBucketNotFound(w, r.PathValue("bucket"))
		return
	}
	if err != nil {
		h.writeInternal(w, r, "compat request failed", err)
		return
	}
	wire, err := renderBucket(tenant(r), bucket, h.now().UTC())
	if err != nil {
		h.writeInternal(w, r, "render bucket", err)
		return
	}
	writeJSON(w, http.StatusOK, &models.HashicorpCloudPacker20230101UpdateBucketResponse{Bucket: wire})
}

func (h *handler) deleteBucket(w http.ResponseWriter, r *http.Request) {
	err := h.repository.DeleteBucket(r.Context(), tenant(r), r.PathValue("bucket"))
	if errors.Is(err, registry.ErrNotFound) {
		// The provider's destroy path tolerates exactly this: a 404 removes the
		// resource from state; any other error fails the destroy (dossier §9).
		writeBucketNotFound(w, r.PathValue("bucket"))
		return
	}
	if err != nil {
		h.writeInternal(w, r, "delete bucket", err)
		return
	}
	var response models.HashicorpCloudPacker20230101DeleteBucketResponse = struct{}{}
	writeJSON(w, http.StatusOK, response)
}

func (h *handler) getVersion(w http.ResponseWriter, r *http.Request) {
	version, err := h.repository.GetVersion(
		r.Context(), tenant(r), r.PathValue("bucket"), r.PathValue("fingerprint"),
	)
	if errors.Is(err, registry.ErrNotFound) {
		audit.FromContext(r.Context()).Enrich(audit.Enrichment{
			Outcome: identity.AuditOutcomeRefused, Reason: "version_not_found",
		})
		writeVersionNotFound(w, r.PathValue("fingerprint"))
		return
	}
	if err != nil {
		h.writeInternal(w, r, "compat request failed", err)
		return
	}
	builds, err := h.repository.ListBuilds(
		r.Context(), tenant(r), r.PathValue("bucket"), r.PathValue("fingerprint"),
	)
	if err != nil {
		h.writeInternal(w, r, "compat request failed", err)
		return
	}
	wire, err := renderVersion(tenant(r), version, builds, h.now().UTC())
	if err != nil {
		h.writeInternal(w, r, "render version", err)
		return
	}
	writeJSON(w, http.StatusOK, &models.HashicorpCloudPacker20230101GetVersionResponse{Version: wire})
}

func (h *handler) createVersion(w http.ResponseWriter, r *http.Request) {
	var body models.HashicorpCloudPacker20230101CreateVersionBody
	if !h.decodeBody(w, r, &body) {
		return
	}
	if body.TemplateType == nil ||
		*body.TemplateType == models.HashicorpCloudPacker20230101TemplateTypeTEMPLATETYPEUNSET {
		writeRPCError(w, http.StatusBadRequest, 3, "template type must be set")
		return
	}
	at := h.now().UTC()
	version, err := registry.NewVersion(
		registry.NewID(at),
		r.PathValue("bucket"),
		body.Fingerprint,
		registry.TemplateType(*body.TemplateType),
		at,
	)
	if err != nil {
		writeRPCError(w, http.StatusBadRequest, 3, err.Error())
		return
	}
	version.AuthorID = principalID(r)
	version, err = h.repository.CreateVersion(r.Context(), tenant(r), version)
	if errors.Is(err, registry.ErrNotFound) {
		writeBucketNotFound(w, r.PathValue("bucket"))
		return
	}
	if errors.Is(err, registry.ErrConflict) {
		// Code 9 pairs with HTTP 400 on live HCP, not 409 (dossier §5.1; duf-xwx).
		writeRPCError(w, http.StatusBadRequest, 9, err.Error())
		return
	}
	if err != nil {
		h.writeInternal(w, r, "compat request failed", err)
		return
	}
	builds, err := h.repository.ListBuilds(
		r.Context(), tenant(r), r.PathValue("bucket"), body.Fingerprint,
	)
	if err != nil {
		h.writeInternal(w, r, "compat request failed", err)
		return
	}
	wire, err := renderVersion(tenant(r), version, builds, h.now().UTC())
	if err != nil {
		h.writeInternal(w, r, "render version", err)
		return
	}
	writeJSON(w, http.StatusOK, &models.HashicorpCloudPacker20230101CreateVersionResponse{Version: wire})
}

// updateVersion serves the revocation capabilities of PackerService_UpdateVersion.
//
// Completion is refused loudly rather than silently ignored because it is
// derived from builds here.
func (h *handler) updateVersion(w http.ResponseWriter, r *http.Request) {
	var body models.HashicorpCloudPacker20230101UpdateVersionBody
	if !h.decodeBody(w, r, &body) {
		return
	}
	switch {
	case body.Complete:
		writeRPCError(w, http.StatusBadRequest, 3, "completion is derived from builds; the complete field is not supported")
		return
	}
	hasAt := !time.Time(body.RevokeAt).IsZero()
	at := h.now().UTC()
	var version *registry.Version
	var err error
	if body.Restore {
		if hasAt || body.RevokeIn != "" {
			writeRPCError(w, http.StatusBadRequest, 3, "restore is mutually exclusive with revoke_at and revoke_in")
			return
		}
		version, err = h.repository.RestoreRevokedVersion(
			r.Context(), tenant(r), r.PathValue("bucket"), r.PathValue("fingerprint"), at,
		)
	} else {
		if hasAt == (body.RevokeIn != "") {
			writeRPCError(w, http.StatusBadRequest, 3, "exactly one of revoke_at and revoke_in must be set")
			return
		}
		effectAt := time.Time(body.RevokeAt).UTC()
		if !hasAt {
			wait, parseErr := parseRevokeIn(body.RevokeIn)
			if parseErr != nil {
				writeRPCError(w, http.StatusBadRequest, 3, parseErr.Error())
				return
			}
			effectAt = at.Add(wait)
		}
		version, err = h.repository.RevokeVersion(
			r.Context(), tenant(r), r.PathValue("bucket"), r.PathValue("fingerprint"),
			store.RevocationRequest{
				RevokeAt:                effectAt,
				Message:                 body.RevocationMessage,
				Author:                  principalName(r),
				SkipDescendants:         body.SkipDescendantsRevocation,
				DisableRollbackChannels: body.DisableRollbackChannels,
			},
			versionName, at,
		)
	}
	if errors.Is(err, registry.ErrNotFound) {
		audit.FromContext(r.Context()).Enrich(audit.Enrichment{
			Outcome: identity.AuditOutcomeRefused, Reason: "version_not_found",
		})
		writeVersionNotFound(w, r.PathValue("fingerprint"))
		return
	}
	if errors.Is(err, registry.ErrRestoreInherited) {
		audit.FromContext(r.Context()).Enrich(audit.Enrichment{
			Outcome: identity.AuditOutcomeRefused, Reason: "version_revocation_inherited",
		})
		writeRPCError(w, http.StatusBadRequest, 9,
			"Directly restoring this version does not apply. The revocation status is inherited from an ancestor version. To restore this version, the revoked ancestor should be restored.")
		return
	}
	if errors.Is(err, registry.ErrConflict) {
		// Code 9 pairs with HTTP 400 on live HCP, not 409 (dossier §5.1).
		if body.Restore {
			audit.FromContext(r.Context()).Enrich(audit.Enrichment{
				Outcome: identity.AuditOutcomeRefused, Reason: "version_not_revoked",
			})
			writeRPCError(w, http.StatusBadRequest, 9,
				"Restoring does not apply. This version is valid and it is not scheduled to be revoked. ")
			return
		}
		writeRPCError(w, http.StatusBadRequest, 9, err.Error())
		return
	}
	if errors.Is(err, registry.ErrInvalid) {
		writeRPCError(w, http.StatusBadRequest, 3, err.Error())
		return
	}
	if err != nil {
		h.writeInternal(w, r, "compat request failed", err)
		return
	}
	builds, err := h.repository.ListBuilds(
		r.Context(), tenant(r), r.PathValue("bucket"), r.PathValue("fingerprint"),
	)
	if err != nil {
		h.writeInternal(w, r, "compat request failed", err)
		return
	}
	wire, err := renderVersion(tenant(r), version, builds, at)
	if err != nil {
		h.writeInternal(w, r, "render version", err)
		return
	}
	writeJSON(w, http.StatusOK, &models.HashicorpCloudPacker20230101UpdateVersionResponse{Version: wire})
}

// revokeInPattern is the wire's documented shape: decimal numbers with s, m, h
// or d unit suffixes, like "30d" or "2h45m".
var revokeInPattern = regexp.MustCompile(`^([0-9]+[smhd])+$`)

func parseRevokeIn(value string) (time.Duration, error) {
	if !revokeInPattern.MatchString(value) {
		return 0, fmt.Errorf(`revoke_in must be a duration in s, m, h or d units, like "30d" or "2h45m"`)
	}
	// time.ParseDuration does not know days; expand them into hours first. A
	// day count that would overflow the multiplication is left unexpanded, so
	// ParseDuration rejects the 'd' unit instead of accepting a wrapped value.
	expanded := regexp.MustCompile(`([0-9]+)d`).ReplaceAllStringFunc(value, func(match string) string {
		days, err := strconv.Atoi(strings.TrimSuffix(match, "d"))
		if err != nil || days > math.MaxInt64/24 {
			return match
		}
		return strconv.Itoa(days*24) + "h"
	})
	wait, err := time.ParseDuration(expanded)
	if err != nil {
		return 0, fmt.Errorf("revoke_in %q is not a valid duration", value)
	}
	return wait, nil
}

func (h *handler) createBuild(w http.ResponseWriter, r *http.Request) {
	var body models.HashicorpCloudPacker20230101CreateBuildBody
	if !h.decodeBody(w, r, &body) {
		return
	}
	if body.ComponentType == "" {
		writeRPCError(w, http.StatusBadRequest, 3, "component type is required")
		return
	}
	version, err := h.repository.GetVersion(
		r.Context(), tenant(r), r.PathValue("bucket"), r.PathValue("fingerprint"),
	)
	if errors.Is(err, registry.ErrNotFound) {
		writeVersionNotFound(w, r.PathValue("fingerprint"))
		return
	}
	if err != nil {
		h.writeInternal(w, r, "compat request failed", err)
		return
	}
	at := h.now().UTC()
	build, err := h.repository.CreateBuild(
		r.Context(),
		tenant(r),
		r.PathValue("bucket"),
		r.PathValue("fingerprint"),
		version.TemplateType,
		store.StoredBuild{
			Build: registry.Build{
				ID:            registry.NewID(at),
				ComponentType: body.ComponentType,
				Status:        domainBuildStatus(body.Status),
				Platform:      body.Platform,
			},
			PackerRunUUID:            body.PackerRunUUID,
			Labels:                   body.Labels,
			SourceExternalIdentifier: body.SourceExternalIdentifier,
			ParentVersionID:          body.ParentVersionID,
			ParentChannelID:          body.ParentChannelID,
			Artifacts:                createArtifacts(body.Artifacts, at),
			CreatedAt:                at,
		},
	)
	if errors.Is(err, registry.ErrConflict) {
		// Code 9 pairs with HTTP 400 on live HCP, not 409 (dossier §5.1; duf-xwx).
		writeRPCError(w, http.StatusBadRequest, 9, err.Error())
		return
	}
	if err != nil {
		h.writeInternal(w, r, "compat request failed", err)
		return
	}
	wire, err := renderBuild(build)
	if err != nil {
		h.writeInternal(w, r, "render build", err)
		return
	}
	writeJSON(w, http.StatusOK, &models.HashicorpCloudPacker20230101CreateBuildResponse{Build: wire})
}

func (h *handler) listBuilds(w http.ResponseWriter, r *http.Request) {
	builds, err := h.repository.ListBuilds(
		r.Context(), tenant(r), r.PathValue("bucket"), r.PathValue("fingerprint"),
	)
	if errors.Is(err, registry.ErrNotFound) {
		writeVersionNotFound(w, r.PathValue("fingerprint"))
		return
	}
	if err != nil {
		h.writeInternal(w, r, "compat request failed", err)
		return
	}
	wireBuilds := make([]*models.HashicorpCloudPacker20230101Build, 0, len(builds))
	for i := range builds {
		wire, err := renderBuild(&builds[i])
		if err != nil {
			h.writeInternal(w, r, "render build", err)
			return
		}
		wireBuilds = append(wireBuilds, wire)
	}
	writeJSON(w, http.StatusOK, &models.HashicorpCloudPacker20230101ListBuildsResponse{
		Builds: wireBuilds,
	})
}

func (h *handler) updateBuild(w http.ResponseWriter, r *http.Request) {
	var body models.HashicorpCloudPacker20230101UpdateBuildBody
	if !h.decodeBody(w, r, &body) {
		return
	}
	build, err := h.repository.GetBuild(
		r.Context(),
		tenant(r),
		r.PathValue("bucket"),
		r.PathValue("fingerprint"),
		r.PathValue("build"),
	)
	if errors.Is(err, registry.ErrNotFound) {
		writeRPCError(w, http.StatusNotFound, 5, "build not found")
		return
	}
	if err != nil {
		h.writeInternal(w, r, "compat request failed", err)
		return
	}
	at := h.now().UTC()
	if err := patchBuild(build, &body, at); err != nil {
		h.writeInternal(w, r, "encode build metadata", err)
		return
	}
	build, err = h.repository.UpdateBuild(
		r.Context(),
		tenant(r),
		r.PathValue("bucket"),
		r.PathValue("fingerprint"),
		*build,
		at,
	)
	if err != nil {
		h.writeInternal(w, r, "compat request failed", err)
		return
	}
	response, err := renderBuild(build)
	if err != nil {
		h.writeInternal(w, r, "render build", err)
		return
	}
	writeJSON(w, http.StatusOK, &models.HashicorpCloudPacker20230101UpdateBuildResponse{
		Build: response,
	})
}

func (h *handler) uploadSbom(w http.ResponseWriter, r *http.Request) {
	var body models.HashicorpCloudPacker20230101UploadSbomBody
	if !h.decodeBody(w, r, &body) {
		return
	}
	build, err := h.repository.GetBuild(
		r.Context(),
		tenant(r),
		r.PathValue("bucket"),
		r.PathValue("fingerprint"),
		r.PathValue("build"),
	)
	if errors.Is(err, registry.ErrNotFound) {
		writeRPCError(w, http.StatusNotFound, 5, "build not found")
		return
	}
	if err != nil {
		h.writeInternal(w, r, "compat request failed", err)
		return
	}
	// Appendix A.11 captured this refusal verbatim from live HCP twice.
	if build.Status != registry.BuildRunning {
		writeJSON(w, http.StatusBadRequest, &struct {
			Code    int32                       `json:"code"`
			Message string                      `json:"message"`
			Details []*models.GoogleProtobufAny `json:"details"`
		}{
			Code:    3,
			Message: "This build's status isn't Running, so sboms can not be uploaded",
			Details: []*models.GoogleProtobufAny{},
		})
		return
	}
	// An omitted name is the server's to fill, and the fingerprint is the
	// documented default: "If omitted, HCP Packer uses the build fingerprint as
	// the file name" (dossier §5.6). No other validation — the spec declares
	// none, and inventing constraints breaks clients real HCP accepts (§4a).
	name := body.Name
	if name == "" {
		name = r.PathValue("fingerprint")
	}
	format := ""
	if body.Format != nil {
		format = string(*body.Format)
	}
	at := h.now().UTC()
	sbom, err := h.repository.UploadSbom(
		r.Context(),
		tenant(r),
		r.PathValue("bucket"),
		r.PathValue("fingerprint"),
		r.PathValue("build"),
		store.Sbom{
			ID:             registry.NewID(at),
			Name:           name,
			Format:         format,
			CompressedData: body.CompressedSbom,
			CreatedAt:      at,
		},
	)
	if errors.Is(err, registry.ErrNotFound) {
		writeRPCError(w, http.StatusNotFound, 5, "build not found")
		return
	}
	if errors.Is(err, store.ErrObjectStorageNotConfigured) ||
		errors.Is(err, store.ErrObjectStorageUnavailable) {
		reason := "object_storage_unavailable"
		if errors.Is(err, store.ErrObjectStorageNotConfigured) {
			reason = "object_storage_unconfigured"
		}
		audit.FromContext(r.Context()).Enrich(audit.Enrichment{Reason: reason})
		correlation := audit.CorrelationID(r.Context())
		h.logger.Error("upload sbom",
			"error", err,
			"correlation_id", correlation,
			"method", r.Method,
			"path", r.URL.Path,
		)
		writeRPCError(w, http.StatusServiceUnavailable, 14,
			"object storage is unavailable; correlation id "+correlation)
		return
	}
	if err != nil {
		// Any error here fails the whole packer build before it can be marked
		// complete (dossier §5.6), so nothing is misreported as tolerable.
		h.writeInternal(w, r, "upload sbom", err)
		return
	}
	writeJSON(w, http.StatusOK, &models.HashicorpCloudPacker20230101UploadSbomResponse{
		Sbom: renderSbom(*sbom),
	})
}

func (h *handler) listSboms(w http.ResponseWriter, r *http.Request) {
	sboms, err := h.repository.ListSboms(
		r.Context(), tenant(r), r.PathValue("bucket"), r.PathValue("fingerprint"),
		r.PathValue("build"),
	)
	if errors.Is(err, registry.ErrNotFound) {
		writeRPCError(w, http.StatusNotFound, 5, "build not found")
		return
	}
	if err != nil {
		h.writeInternal(w, r, "list SBOMs", err)
		return
	}
	wire := make([]*models.HashicorpCloudPacker20230101Sbom, 0, len(sboms))
	for _, sbom := range sboms {
		wire = append(wire, renderSbom(sbom))
	}
	writeJSON(w, http.StatusOK, &models.HashicorpCloudPacker20230101ListSbomsResponse{Sboms: wire})
}

func (h *handler) getSbom(w http.ResponseWriter, r *http.Request) {
	if _, err := h.repository.GetSbom(
		r.Context(), tenant(r), r.PathValue("bucket"), r.PathValue("fingerprint"),
		r.PathValue("build"), r.PathValue("sbom"),
	); errors.Is(err, registry.ErrNotFound) {
		writeRPCError(w, http.StatusNotFound, 5, "SBOM not found")
		return
	} else if err != nil {
		h.writeInternal(w, r, "get SBOM", err)
		return
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	download := url.URL{Scheme: scheme, Host: r.Host, Path: r.URL.Path + "/download"}
	writeJSON(w, http.StatusOK, &models.HashicorpCloudPacker20230101GetSbomResponse{
		DownloadURL: download.String(),
	})
}

func (h *handler) downloadSbom(w http.ResponseWriter, r *http.Request) {
	data, err := h.repository.DownloadSbom(
		r.Context(), tenant(r), r.PathValue("bucket"), r.PathValue("fingerprint"),
		r.PathValue("build"), r.PathValue("sbom"),
	)
	if errors.Is(err, registry.ErrNotFound) {
		writeRPCError(w, http.StatusNotFound, 5, "SBOM not found")
		return
	}
	// The same truth upload tells: a record whose bytes live in unreachable
	// object storage is temporarily unavailable, not an internal fault.
	if errors.Is(err, store.ErrObjectStorageNotConfigured) ||
		errors.Is(err, store.ErrObjectStorageUnavailable) {
		reason := "object_storage_unavailable"
		if errors.Is(err, store.ErrObjectStorageNotConfigured) {
			reason = "object_storage_unconfigured"
		}
		audit.FromContext(r.Context()).Enrich(audit.Enrichment{Reason: reason})
		correlation := audit.CorrelationID(r.Context())
		h.logger.Error("download sbom",
			"error", err,
			"correlation_id", correlation,
			"method", r.Method,
			"path", r.URL.Path,
		)
		writeRPCError(w, http.StatusServiceUnavailable, 14,
			"object storage is unavailable; correlation id "+correlation)
		return
	}
	if err != nil {
		h.writeInternal(w, r, "download SBOM", err)
		return
	}
	// The observed wire (live HCP, probed 2026-08-08): the download is the
	// JSON document under "<name>.json", whatever the format — no format
	// suffix, no compression. (HCP's presign literally says Content-Type
	// "json"; the valid MIME type is deliberate, nothing parses that header.)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
		"filename": r.PathValue("sbom") + ".json",
	}))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// reportedPackageResponse keeps vuln_details optional even though the generated
// model's tag does not. A nil value must remain absent when the build has no
// current successful scan; null or [] would falsely report a clean scan.
type reportedPackageResponse struct {
	Name        string                                                     `json:"name,omitempty"`
	Version     string                                                     `json:"version,omitempty"`
	Purl        string                                                     `json:"purl,omitempty"`
	Sboms       []*models.HashicorpCloudPacker20230101Sbom                 `json:"sboms"`
	VulnDetails []*models.HashicorpCloudPacker20230101VulnerabilityDetails `json:"vuln_details,omitempty"`
}

type listBuildPackagesResponse struct {
	Packages   []*reportedPackageResponse                     `json:"packages"`
	Pagination *models.HashicorpCloudCommonPaginationResponse `json:"pagination,omitempty"`
}

func (h *handler) listBuildPackages(w http.ResponseWriter, r *http.Request) {
	packages, unparseable, err := h.repository.ListBuildPackages(
		r.Context(), tenant(r), r.PathValue("bucket"), r.PathValue("fingerprint"),
		r.PathValue("build"),
	)
	if errors.Is(err, registry.ErrNotFound) {
		writeRPCError(w, http.StatusNotFound, 5, "build not found")
		return
	}
	if err != nil {
		h.writeInternal(w, r, "list build packages", err)
		return
	}
	if len(unparseable) > 0 {
		names, _ := json.Marshal(unparseable)
		writeRPCError(w, http.StatusUnprocessableEntity, 9,
			"package inventory is unparseable for SBOMs "+string(names))
		return
	}

	var run *store.ScanRun
	findingsByPackage := map[packageIdentity][]store.StoredFinding{}
	state, err := h.repository.GetBuildScanState(r.Context(), tenant(r), r.PathValue("build"))
	if err != nil {
		h.writeInternal(w, r, "read build scan state", err)
		return
	}
	if state != nil && state.CurrentFindingsRunID != "" {
		run, err = h.repository.GetScanRun(r.Context(), tenant(r), state.CurrentFindingsRunID)
		if err != nil {
			h.writeInternal(w, r, "read current scan run", err)
			return
		}
		findings, err := h.repository.ListScanFindings(r.Context(), tenant(r), state.CurrentFindingsRunID)
		if err != nil {
			h.writeInternal(w, r, "read current scan findings", err)
			return
		}
		for _, finding := range deduplicateBuildFindings(r.PathValue("build"), findings) {
			key := packageIdentity{
				name: finding.Package.Name, version: finding.Package.Version, purl: finding.Package.Purl,
			}
			findingsByPackage[key] = append(findingsByPackage[key], finding)
		}
	}

	query := r.URL.Query()
	filtered := make([]store.ReportedPackage, 0, len(packages))
	for _, pkg := range packages {
		if name := query.Get("package_name"); name != "" && pkg.Name != name {
			continue
		}
		if prefix := query.Get("package_name_starts_with"); prefix != "" &&
			!strings.HasPrefix(pkg.Name, prefix) {
			continue
		}
		if version := query.Get("package_version"); version != "" && pkg.Version != version {
			continue
		}
		filtered = append(filtered, pkg)
	}
	start, end, pagination, err := paginationPage(r, len(filtered))
	if err != nil {
		writeRPCError(w, http.StatusBadRequest, 3, err.Error())
		return
	}
	wire := make([]*reportedPackageResponse, 0, end-start)
	for _, pkg := range filtered[start:end] {
		sources := make([]*models.HashicorpCloudPacker20230101Sbom, 0, len(pkg.Sboms))
		for _, sbom := range pkg.Sboms {
			sources = append(sources, renderSbom(sbom))
		}
		wire = append(wire, &reportedPackageResponse{
			Name: pkg.Name, Version: pkg.Version, Purl: pkg.Purl, Sboms: sources,
		})
		if run != nil {
			wire[len(wire)-1].VulnDetails = []*models.HashicorpCloudPacker20230101VulnerabilityDetails{{
				LastScannedAt: strfmt.DateTime(run.ObservedAt),
				Vulnerabilities: renderVulnerabilities(findingsByPackage[packageIdentity{
					name: pkg.Name, version: pkg.Version, purl: pkg.Purl,
				}]),
			}}
		}
	}
	if run != nil {
		writeScanHeaders(w.Header(), run)
	}
	writeJSON(w, http.StatusOK, &listBuildPackagesResponse{
		Packages: wire, Pagination: pagination,
	})
}

func renderSbom(sbom store.Sbom) *models.HashicorpCloudPacker20230101Sbom {
	wire := &models.HashicorpCloudPacker20230101Sbom{ID: sbom.ID.String(), Name: sbom.Name}
	if sbom.Format != "" {
		format := models.HashicorpCloudPacker20230101SbomFormat(sbom.Format)
		wire.Format = &format
	}
	return wire
}

func patchBuild(
	build *store.StoredBuild,
	body *models.HashicorpCloudPacker20230101UpdateBuildBody,
	at time.Time,
) error {
	if body.Status != nil {
		build.Status = domainBuildStatus(body.Status)
	}
	if body.Platform != "" {
		build.Platform = body.Platform
	}
	if body.PackerRunUUID != "" {
		build.PackerRunUUID = body.PackerRunUUID
	}
	if body.Labels != nil {
		build.Labels = body.Labels
	}
	if body.SourceExternalIdentifier != "" {
		build.SourceExternalIdentifier = body.SourceExternalIdentifier
	}
	if body.ParentVersionID != "" {
		build.ParentVersionID = body.ParentVersionID
	}
	if body.ParentChannelID != "" {
		build.ParentChannelID = body.ParentChannelID
	}
	if body.Metadata != nil {
		metadata, err := json.Marshal(body.Metadata)
		if err != nil {
			return fmt.Errorf("marshal build metadata: %w", err)
		}
		build.Metadata = metadata
		build.MetadataSeen = true
	}
	build.Artifacts = append(build.Artifacts, createArtifacts(body.Artifacts, at)...)
	return nil
}

func createArtifacts(
	body []*models.HashicorpCloudPacker20230101ArtifactCreateBody,
	at time.Time,
) []store.Artifact {
	artifacts := make([]store.Artifact, 0, len(body))
	for _, artifact := range body {
		if artifact == nil {
			continue
		}
		artifacts = append(artifacts, store.Artifact{
			ID:                 registry.NewID(at),
			ExternalIdentifier: artifact.ExternalIdentifier,
			Region:             artifact.Region,
			CreatedAt:          at,
		})
	}
	return artifacts
}

func renderBucket(
	tenant store.Tenant,
	bucket *store.Bucket,
	now time.Time,
) (*models.HashicorpCloudPacker20230101Bucket, error) {
	// The spec documents resource_name as
	// `packer/project/<project-id>/bucket/<bucket-name>`, and the provider's
	// bucket Read stores it in Terraform state — leaving it empty causes
	// perpetual state drift on hcp_packer_bucket.
	platforms := bucket.Platforms
	if platforms == nil {
		platforms = []string{}
	}
	wire := &models.HashicorpCloudPacker20230101Bucket{
		ID:           bucket.ID.String(),
		Name:         bucket.Name,
		ResourceName: "packer/project/" + tenant.ProjectID.String() + "/bucket/" + bucket.Name,
		Description:  bucket.Description,
		Labels:       bucket.Labels,
		Platforms:    platforms,
		Location: &models.HashicorpCloudLocationLocation{
			OrganizationID: tenant.OrganizationID.String(),
			ProjectID:      tenant.ProjectID.String(),
		},
		CreatedAt: strfmt.DateTime(bucket.CreatedAt),
		UpdatedAt: strfmt.DateTime(bucket.UpdatedAt),
	}
	if bucket.LatestVersion != nil {
		version, err := renderVersion(tenant, bucket.LatestVersion, bucket.LatestVersionBuilds, now)
		if err != nil {
			return nil, err
		}
		wire.LatestVersion = version
		if bucket.LatestVersion.Parents != nil {
			status := wireAncestryStatus(bucket.LatestVersion.Parents.Status)
			wire.Parents = &models.HashicorpCloudPacker20230101BucketParents{
				Href: ancestryHref(tenant, bucket.Name,
					models.HashicorpCloudPacker20230101BucketAncestryTypeANCESTRYTYPEPARENTS, ""),
				Status: &status,
			}
		}
		if bucket.ChildrenStatus != nil {
			status := wireAncestryStatus(*bucket.ChildrenStatus)
			wire.Children = &models.HashicorpCloudPacker20230101BucketChildren{
				Href: ancestryHref(tenant, bucket.Name,
					models.HashicorpCloudPacker20230101BucketAncestryTypeANCESTRYTYPECHILDREN, ""),
				Status: &status,
			}
		}
	}
	return wire, nil
}

// managedChannelAuthor is the author stamped on managed channels and their
// channel documents. Live stamps "HCP Packer" here (Appendix A probe 04);
// we deliberately deviate to "Dufflebag" because a HashiCorp
// trademark must not appear in content this server originates. Verified inert:
// Packer matches errors only by code number (§5.1 errCodeRegex) and the
// provider keys managed handling off the `managed` flag, not this string —
// see the dossier §7 deviation note. Assignment history renders its persisted
// per-row author instead (probes 41 and 45).
const managedChannelAuthor = "Dufflebag"

func renderChannel(
	tenant store.Tenant,
	channel *store.Channel,
	now time.Time,
) (*models.HashicorpCloudPacker20230101Channel, error) {
	wire := &models.HashicorpCloudPacker20230101Channel{
		ID:         channel.ID.String(),
		AuthorID:   channel.AssignmentAuthorID,
		BucketName: channel.BucketName,
		Name:       channel.Name,
		Restricted: channel.Restricted,
		Managed:    channel.Managed,
		CreatedAt:  strfmt.DateTime(channel.CreatedAt),
		UpdatedAt:  strfmt.DateTime(channel.UpdatedAt),
	}
	if channel.Managed && channel.Version == nil && wire.AuthorID == "" {
		wire.AuthorID = managedChannelAuthor
	}
	if channel.Version != nil {
		version, err := renderVersion(tenant, channel.Version, nil, now)
		if err != nil {
			return nil, err
		}
		wire.Version = version
	}
	return wire, nil
}

// versionName is the wire's collapse of completion into a single string: "v0"
// until the version completes, "v<n>" after. The one place the sentinel is made.
func versionName(version *registry.Version) string {
	if sequence, complete := version.Sequence(); complete {
		return "v" + strconv.Itoa(sequence)
	}
	return "v0"
}

func renderVersion(
	tenant store.Tenant,
	version *registry.Version,
	builds []store.StoredBuild,
	now time.Time,
) (*models.HashicorpCloudPacker20230101Version, error) {
	name := versionName(version)
	status := models.HashicorpCloudPacker20230101VersionStatusVERSIONRUNNING
	if _, complete := version.Sequence(); complete {
		status = models.HashicorpCloudPacker20230101VersionStatusVERSIONACTIVE
	}
	templateType := models.HashicorpCloudPacker20230101TemplateType(version.TemplateType)
	wireBuilds := make([]*models.HashicorpCloudPacker20230101Build, 0, len(builds))
	for i := range builds {
		wire, err := renderBuild(&builds[i])
		if err != nil {
			return nil, err
		}
		wireBuilds = append(wireBuilds, wire)
	}
	// RevokeAt stays nil for a never-revoked version so the non-omitted pointer
	// renders null, matching the Appendix A.7 verbatim capture and S3a proof.
	wire := &models.HashicorpCloudPacker20230101Version{
		ID:             version.ID.String(),
		Name:           name,
		BucketName:     version.BucketName,
		Fingerprint:    version.Fingerprint,
		AuthorID:       version.AuthorID,
		HasDescendants: version.HasDescendants,
		TemplateType:   &templateType,
		Status:         &status,
		Builds:         wireBuilds,
		CreatedAt:      strfmt.DateTime(version.CreatedAt),
		UpdatedAt:      strfmt.DateTime(version.UpdatedAt),
	}
	if version.Parents != nil {
		parentsStatus := wireAncestryStatus(version.Parents.Status)
		wire.Parents = &models.HashicorpCloudPacker20230101VersionParents{
			Href: ancestryHref(tenant, version.BucketName,
				models.HashicorpCloudPacker20230101BucketAncestryTypeANCESTRYTYPEPARENTS,
				version.Fingerprint),
			Status: &parentsStatus,
		}
	}
	if rev := version.Revocation(); rev != nil {
		// Revocation takes effect AT revoke_at; the status is derived per read,
		// no job flips it. Packer's data sources refuse only VERSION_REVOKED
		// (hcp-packer-version data.go:129), so a scheduled revocation stays
		// consumable — matching the deprecated clients' revoke_at<now rule.
		status := models.HashicorpCloudPacker20230101VersionStatusVERSIONREVOCATIONSCHEDULED
		if !rev.RevokeAt.After(now) {
			status = models.HashicorpCloudPacker20230101VersionStatusVERSIONREVOKED
		}
		wire.Status = &status
		revokeAt := strfmt.DateTime(rev.RevokeAt)
		wire.RevokeAt = &revokeAt
		wire.RevocationMessage = rev.Message
		wire.RevocationAuthor = rev.Author
		revocationType := models.HashicorpCloudPacker20230101RevocationTypeMANUAL
		if a := rev.InheritedFrom; a != nil {
			revocationType = models.HashicorpCloudPacker20230101RevocationTypeINHERITED
			wire.RevocationInheritedFrom = &models.HashicorpCloudPacker20230101RevokedAncestor{
				BucketName:         a.BucketName,
				Href:               versionHref(tenant, a.BucketName, a.Fingerprint),
				VersionFingerprint: a.Fingerprint,
				VersionID:          a.VersionID.String(),
				VersionName:        a.VersionName,
			}
		}
		wire.RevocationType = &revocationType
	}
	return wire, nil
}

func versionHref(tenant store.Tenant, bucketName, fingerprint string) string {
	return fmt.Sprintf(
		"/packer/2023-01-01/organizations/%s/projects/%s/buckets/%s/versions/%s",
		tenant.OrganizationID, tenant.ProjectID,
		url.PathEscape(bucketName), url.PathEscape(fingerprint),
	)
}

func ancestryHref(
	tenant store.Tenant,
	bucketName string,
	ancestryType models.HashicorpCloudPacker20230101BucketAncestryType,
	versionFingerprint string,
) string {
	query := url.Values{"type": {string(ancestryType)}}
	if versionFingerprint != "" {
		query.Set("version_fingerprint", versionFingerprint)
	}
	return fmt.Sprintf(
		"/packer/2023-01-01/organizations/%s/projects/%s/buckets/%s/ancestry?%s",
		tenant.OrganizationID, tenant.ProjectID, url.PathEscape(bucketName), query.Encode(),
	)
}

func renderBuild(build *store.StoredBuild) (*models.HashicorpCloudPacker20230101Build, error) {
	status := wireBuildStatus(build.Status)
	artifacts := make([]*models.HashicorpCloudPacker20230101Artifact, 0, len(build.Artifacts))
	for _, artifact := range build.Artifacts {
		artifacts = append(artifacts, &models.HashicorpCloudPacker20230101Artifact{
			ID:                 artifact.ID.String(),
			ExternalIdentifier: artifact.ExternalIdentifier,
			Region:             artifact.Region,
			CreatedAt:          strfmt.DateTime(artifact.CreatedAt),
		})
	}
	wire := &models.HashicorpCloudPacker20230101Build{
		ID:                       build.ID.String(),
		VersionID:                build.VersionID.String(),
		ComponentType:            build.ComponentType,
		Status:                   &status,
		Platform:                 build.Platform,
		PackerRunUUID:            build.PackerRunUUID,
		Labels:                   build.Labels,
		SourceExternalIdentifier: build.SourceExternalIdentifier,
		Artifacts:                artifacts,
		CreatedAt:                strfmt.DateTime(build.CreatedAt),
		UpdatedAt:                strfmt.DateTime(build.UpdatedAt),
	}
	if build.MetadataSeen {
		var metadata models.HashicorpCloudPacker20230101BuildMetadata
		if err := json.Unmarshal(build.Metadata, &metadata); err != nil {
			return nil, fmt.Errorf("unmarshal stored build metadata: %w", err)
		}
		wire.Metadata = &metadata
	}
	return wire, nil
}

func wireAncestryStatus(status registry.AncestryStatus) models.HashicorpCloudPacker20230101AncestryStatus {
	switch status {
	case registry.AncestryUpToDate:
		return models.HashicorpCloudPacker20230101AncestryStatusUPTODATE
	case registry.AncestryOutOfDate:
		return models.HashicorpCloudPacker20230101AncestryStatusOUTOFDATE
	default:
		return models.HashicorpCloudPacker20230101AncestryStatusUNDETERMINED
	}
}

func renderBucketAncestry(
	relation *store.BucketAncestry,
) *models.HashicorpCloudPacker20230101BucketAncestry {
	status := models.HashicorpCloudPacker20230101AncestryStatusUNDETERMINED
	var channelVersion *models.HashicorpCloudPacker20230101ChannelVersion
	if relation.ParentChannelVersion != nil {
		if relation.ParentChannelVersion.ID == relation.Parent.ID {
			status = models.HashicorpCloudPacker20230101AncestryStatusUPTODATE
		} else {
			status = models.HashicorpCloudPacker20230101AncestryStatusOUTOFDATE
		}
		channelVersion = &models.HashicorpCloudPacker20230101ChannelVersion{
			ID:          relation.ParentChannelVersion.ID.String(),
			Name:        ancestryVersionName(relation.ParentChannelVersion.Sequence),
			Fingerprint: relation.ParentChannelVersion.Fingerprint,
		}
	}
	return &models.HashicorpCloudPacker20230101BucketAncestry{
		Status: &status,
		Parent: &models.HashicorpCloudPacker20230101Parent{
			BucketName:         relation.Parent.BucketName,
			VersionName:        ancestryVersionName(relation.Parent.Sequence),
			VersionID:          relation.Parent.ID.String(),
			VersionFingerprint: relation.Parent.Fingerprint,
			ChannelName:        relation.ParentChannelName,
			ChannelVersion:     channelVersion,
		},
		Child: &models.HashicorpCloudPacker20230101Child{
			BucketName:         relation.Child.BucketName,
			VersionName:        ancestryVersionName(relation.Child.Sequence),
			VersionID:          relation.Child.ID.String(),
			VersionFingerprint: relation.Child.Fingerprint,
		},
	}
}

func ancestryVersionName(sequence int) string {
	if sequence == 0 {
		return "v0"
	}
	return "v" + strconv.Itoa(sequence)
}

func domainBuildStatus(status *models.HashicorpCloudPacker20230101BuildStatus) registry.BuildStatus {
	if status == nil {
		return registry.BuildPending
	}
	switch *status {
	case models.HashicorpCloudPacker20230101BuildStatusBUILDRUNNING:
		return registry.BuildRunning
	case models.HashicorpCloudPacker20230101BuildStatusBUILDDONE:
		return registry.BuildDone
	case models.HashicorpCloudPacker20230101BuildStatusBUILDCANCELLED:
		return registry.BuildCancelled
	case models.HashicorpCloudPacker20230101BuildStatusBUILDFAILED:
		return registry.BuildFailed
	default:
		return registry.BuildPending
	}
}

func wireBuildStatus(status registry.BuildStatus) models.HashicorpCloudPacker20230101BuildStatus {
	switch status {
	case registry.BuildRunning:
		return models.HashicorpCloudPacker20230101BuildStatusBUILDRUNNING
	case registry.BuildDone:
		return models.HashicorpCloudPacker20230101BuildStatusBUILDDONE
	case registry.BuildCancelled:
		return models.HashicorpCloudPacker20230101BuildStatusBUILDCANCELLED
	case registry.BuildFailed:
		return models.HashicorpCloudPacker20230101BuildStatusBUILDFAILED
	default:
		return models.HashicorpCloudPacker20230101BuildStatusBUILDUNSET
	}
}

// channelUpdateFields parses a non-empty update_mask; the handler refuses an
// empty one before calling (Appendix A probe 15).
func channelUpdateFields(mask string) (bool, bool, bool) {
	var restricted, version bool
	for _, field := range strings.Split(mask, ",") {
		switch strings.TrimSpace(field) {
		case "restricted":
			restricted = true
		case "versionFingerprint":
			version = true
		default:
			return false, false, false
		}
	}
	return restricted, version, true
}

func (h *handler) requireVersion(w http.ResponseWriter, r *http.Request, fingerprint string) bool {
	_, err := h.repository.GetVersion(
		r.Context(), tenant(r), r.PathValue("bucket"), fingerprint,
	)
	if errors.Is(err, registry.ErrNotFound) {
		writeVersionNotFound(w, fingerprint)
		return false
	}
	if err != nil {
		h.writeInternal(w, r, "compat request failed", err)
		return false
	}
	return true
}

func (h *handler) writeChannelError(
	w http.ResponseWriter, r *http.Request, channelName string, err error,
) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, registry.ErrNotFound) {
		writeRPCError(w, http.StatusNotFound, 5, fmt.Sprintf(
			"Error: The channel with identifier %s does not exist.", channelName,
		))
		return false
	}
	if errors.Is(err, store.ErrManagedChannel) {
		writeManagedAssignmentRefusal(w, channelName)
		return false
	}
	if errors.Is(err, registry.ErrConflict) {
		// A domain conflict is the caller's to fix, so it says what it is —
		// unlike an internal failure, whose detail stays server-side. Code 9
		// pairs with HTTP 400 on live HCP, not 409 (Appendix A probe 19,
		// dossier §5.1; duf-xwx).
		writeRPCError(w, http.StatusBadRequest, 9, err.Error())
		return false
	}
	h.writeInternal(w, r, "channel operation failed", err)
	return false
}

func writeBucketNotFound(w http.ResponseWriter, bucketName string) {
	writeRPCError(w, http.StatusNotFound, 5, fmt.Sprintf(
		"Error: The bucket with identifier %s does not exist.", bucketName,
	))
}

func writeVersionNotFound(w http.ResponseWriter, fingerprint string) {
	writeRPCError(w, http.StatusConflict, 10, fmt.Sprintf(
		"Version with fingerprint %s not found", fingerprint,
	))
}

func writeManagedAssignmentRefusal(w http.ResponseWriter, channelName string) {
	writeRPCError(w, http.StatusBadRequest, 9, fmt.Sprintf(
		"Cannot assign to managed channel '%s'", channelName,
	))
}

// decodeBody reads a request body, silently ignoring unknown fields.
//
// Live HCP ignores fields it does not know: UpdateBucket with a bogus field
// answered 200 applying the known fields, and an UpdateBuild wrapped whole in
// an unknown envelope answered 200 as a no-op (Appendix A probes 20 and 11,
// dossier §4a). DisallowUnknownFields here would 400 a future Packer or
// provider release that works against real HCP — the exact divergence §4a
// forbids (duf-7cy).
func (h *handler) decodeBody(w http.ResponseWriter, r *http.Request, body any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(body); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			audit.FromContext(r.Context()).Enrich(audit.Enrichment{
				Outcome: identity.AuditOutcomeRefused, Reason: "body_too_large",
			})
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusGatewayTimeout)
			_, _ = w.Write([]byte("Gateway Timeout"))
			return false
		}
		writeRPCError(w, http.StatusBadRequest, 3, fmt.Sprintf("invalid request body: %v", err))
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeInternal records the failure server-side and returns only a correlation
// id.
//
// Raw errors carry table, column and constraint names straight from pgx.
// Returning them hands schema and infrastructure detail to any authenticated
// caller, which contradicts the server-side-detail, client-side-silence posture
// ADR-0017 takes for refusals — an internal failure should not be chattier than
// a deliberate denial.
func (h *handler) writeInternal(w http.ResponseWriter, r *http.Request, action string, err error) {
	correlation := audit.CorrelationID(r.Context())
	h.logger.Error(action,
		"error", err,
		"correlation_id", correlation,
		"method", r.Method,
		"path", r.URL.Path,
	)
	writeRPCError(w, http.StatusInternalServerError, 13,
		"internal error; correlation id "+correlation)
}

func writeRPCError(w http.ResponseWriter, status int, code int32, message string) {
	writeJSON(w, status, &models.GoogleRPCStatus{
		Code:    code,
		Message: message,
		Details: []*models.GoogleProtobufAny{},
	})
}

// writeUpdateMaskRequired reproduces the one error live HCP answers with
// non-empty details: a google.rpc.BadRequest naming body.update_mask, captured
// verbatim in Appendix A probe 15. The violation carries no field named "code",
// so Packer's errCodeRegex still finds exactly the envelope's code 3.
func writeUpdateMaskRequired(w http.ResponseWriter) {
	// Field order matches the capture; a struct rather than a map keeps it
	// deterministic.
	type fieldViolation struct {
		Field            string `json:"field"`
		Description      string `json:"description"`
		Reason           string `json:"reason"`
		LocalizedMessage any    `json:"localized_message"`
	}
	writeJSON(w, http.StatusBadRequest, &models.GoogleRPCStatus{
		Code:    3,
		Message: "body: (update_mask: field mask: must be set.).",
		Details: []*models.GoogleProtobufAny{{
			AtType: "type.googleapis.com/google.rpc.BadRequest",
			GoogleProtobufAny: map[string]any{
				"field_violations": []fieldViolation{{
					Field:       "body.update_mask",
					Description: "field mask: must be set",
				}},
			},
		}},
	})
}
