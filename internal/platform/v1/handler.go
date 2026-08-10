package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/bagdrop"
	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/keyring"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/google/uuid"
)

// PlatformRepository is the storage contract for tenancy and principal lifecycle.
//
// The two listings take the whole principal rather than a bare scope, because
// Scope's zero value is platform scope — the most privileged input — and a
// principal cannot be constructed holding it without root (duf-ueq).
type PlatformRepository interface {
	ListOrganizationsForPrincipal(context.Context, *identity.Principal) ([]store.Organization, error)
	CreateOrganization(context.Context, store.Organization) (*store.Organization, error)
	GetOrganization(context.Context, string) (*store.Organization, error)
	DeleteOrganization(context.Context, string) error
	ListProjectsForPrincipal(context.Context, *identity.Principal, uuid.UUID) ([]store.Project, error)
	CreateProject(context.Context, store.Project) (*store.Project, error)
	GetProject(context.Context, string, string) (*store.Project, error)
	DeleteProject(context.Context, string, string) error
	ListPins(context.Context, store.Tenant) ([]store.Pin, error)
	SetPin(context.Context, store.Tenant, string, string, time.Time) (*store.Pin, error)
	DeletePin(context.Context, store.Tenant, string) error
	// ListPrincipals lists the principals bound to EXACTLY the selected scope.
	// The caller authorizes, the selection filters; the store re-asserts the
	// caller may see the selection rather than trusting it (duf-4qr).
	ListPrincipals(context.Context, *identity.Principal, identity.Scope) ([]*identity.Principal, error)
	CreatePrincipal(context.Context, *identity.Principal) error
	GetPrincipalByID(context.Context, string) (*identity.Principal, error)
	DeletePrincipal(context.Context, string) error
	IssuePrincipalSecret(context.Context, string, string, *time.Time, time.Time) (string, identity.Secret, error)
	RevokePrincipalSecret(context.Context, string, string, time.Time) error
	// ObjectStorageState reports whether SBOM storage is configured and, if so,
	// what the last operation to touch it saw. The health probe is
	// unauthenticated and polled hard, so it reads a remembered answer rather
	// than opening a connection of its own.
	ObjectStorageState() string
}

// InstanceRepository claims an uninitialized instance.
type InstanceRepository interface {
	// InitializeInstance claims the instance and stores the recovery verifier
	// in the same transaction: an instance is never claimed without one.
	InitializeInstance(context.Context, *identity.Principal, []byte, int) error
	// InstanceStatus reports claimed-ness, its recorded time, and database
	// reachability. Errors stay server-side on the unauthenticated health path.
	InstanceStatus(context.Context) (initialized bool, initializedAt *time.Time, database bool, err error)
	// RecoveryVerifier answers identity.ErrNotFound both for an unclaimed
	// instance and one initialized before recovery existed.
	RecoveryVerifier(context.Context) (digest []byte, threshold int, err error)
}

// BuildInfo is supplied by the executable from its link-time values and mounted
// route table. The platform package does not maintain a second copy of either.
type BuildInfo struct {
	Version     string
	Commit      string
	APIVersions []string
}

type AuditTargetRepository interface {
	ListAuditTargets(context.Context) ([]identity.AuditTarget, error)
	CreateAuditTarget(context.Context, string, string, time.Time) (identity.AuditTarget, error)
	DeleteAuditTarget(context.Context, string) error
}

type AuditTargetBroker interface {
	Add(audit.Target) error
	Remove(string) error
	Health() []audit.SinkHealth
}

type EncryptionService interface {
	State() string
	Entries(context.Context) ([]keyring.Entry, error)
	Rewrap(context.Context) ([]keyring.Entry, error)
	Rotate(context.Context) ([]keyring.Entry, error)
}

type BagDropService interface {
	Get(context.Context, string, string) (*bagdrop.Config, error)
	Put(context.Context, string, string, bagdrop.Write) (*bagdrop.Config, *bagdrop.VerificationResult, error)
	Delete(context.Context, string, string) error
	Verify(context.Context, string, string) (bagdrop.VerificationResult, error)
	Enable(context.Context, string, string) (*bagdrop.Config, *bagdrop.VerificationResult, error)
	Disable(context.Context, string, string) (*bagdrop.Config, error)
	ListAssociations(context.Context, string, string) ([]bagdrop.Association, error)
	Associate(context.Context, string, string, string) (*bagdrop.Association, error)
	Unassociate(context.Context, string, string, string) (bagdrop.RemovalOutcome, error)
	Status(context.Context, string, string) (*bagdrop.Status, error)
}

type auditTargetSink interface {
	audit.Sink
}

type server struct {
	repository PlatformRepository
	instance   InstanceRepository
	// auth verifies the token the session endpoints carry themselves: those
	// routes are exempt from the authentication middleware because their
	// credential arrives as a cookie or is being minted into one.
	auth Authenticator
	// principals resolves the session actor's name for audit attribution: the
	// session routes are middleware-exempt, so nothing upstream resolves it
	// (duf-9dq).
	principals   Principals
	logger       *slog.Logger
	now          func() time.Time
	auditTargets AuditTargetRepository
	auditBroker  AuditTargetBroker
	encryption   EncryptionService
	bagDrop      BagDropService
	// scanner is nil on deployments with no adapter configured, which is the
	// ordinary posture rather than a fault.
	scanner       Scanner
	build         BuildInfo
	openAuditSink func(string) (auditTargetSink, error)
	configMu      sync.Mutex
}

// NewHandler serves the platform API.
func NewHandler(
	repository PlatformRepository, instance InstanceRepository,
	auth Authenticator, principals Principals, logger *slog.Logger,
	auditTargets AuditTargetRepository, auditBroker AuditTargetBroker,
	encryption EncryptionService, scanner Scanner, bagDrop BagDropService, build BuildInfo,
) http.Handler {
	return newHandlerWithServices(
		repository, instance, auth, principals, logger,
		auditTargets, auditBroker, encryption, scanner, bagDrop, build, time.Now,
	)
}

func newHandler(
	repository PlatformRepository,
	instance InstanceRepository,
	auth Authenticator,
	principals Principals,
	logger *slog.Logger,
	now func() time.Time,
) http.Handler {
	return newHandlerWithBuildAndAudit(
		repository, instance, auth, principals, logger, nil, nil, nil, nil, BuildInfo{}, now,
	)
}

func newHandlerWithAudit(
	repository PlatformRepository,
	instance InstanceRepository,
	auth Authenticator,
	principals Principals,
	logger *slog.Logger,
	auditTargets AuditTargetRepository,
	auditBroker AuditTargetBroker,
	now func() time.Time,
) http.Handler {
	return newHandlerWithBuildAndAudit(
		repository, instance, auth, principals, logger, auditTargets, auditBroker, nil, nil, BuildInfo{}, now,
	)
}

func newHandlerWithBuildAndAudit(
	repository PlatformRepository,
	instance InstanceRepository,
	auth Authenticator,
	principals Principals,
	logger *slog.Logger,
	auditTargets AuditTargetRepository,
	auditBroker AuditTargetBroker,
	encryption EncryptionService,
	scanner Scanner,
	build BuildInfo,
	now func() time.Time,
) http.Handler {
	return newHandlerWithServices(
		repository, instance, auth, principals, logger, auditTargets, auditBroker,
		encryption, scanner, nil, build, now,
	)
}

func newHandlerWithBagDrop(
	repository PlatformRepository, instance InstanceRepository,
	auth Authenticator, principals Principals, logger *slog.Logger,
	bagDrop BagDropService, now func() time.Time,
) http.Handler {
	return newHandlerWithServices(
		repository, instance, auth, principals, logger, nil, nil, nil, nil, bagDrop, BuildInfo{}, now,
	)
}

func newHandlerWithServices(
	repository PlatformRepository,
	instance InstanceRepository,
	auth Authenticator,
	principals Principals,
	logger *slog.Logger,
	auditTargets AuditTargetRepository,
	auditBroker AuditTargetBroker,
	encryption EncryptionService,
	scanner Scanner,
	bagDrop BagDropService,
	build BuildInfo,
	now func() time.Time,
) http.Handler {
	s := &server{
		repository:   repository,
		instance:     instance,
		auth:         auth,
		principals:   principals,
		logger:       logger,
		now:          now,
		auditTargets: auditTargets,
		auditBroker:  auditBroker,
		encryption:   encryption,
		scanner:      scanner,
		bagDrop:      bagDrop,
		build: BuildInfo{
			Version: build.Version, Commit: build.Commit,
			APIVersions: append([]string(nil), build.APIVersions...),
		},
		openAuditSink: func(path string) (auditTargetSink, error) {
			return audit.NewFileSink(path, logger)
		},
	}
	// An internal failure records its detail server-side and returns only a
	// correlation id. Raw errors carry table, column and constraint names from
	// pgx, and an internal error should not be chattier than a deliberate
	// refusal (ADR-0017).
	//
	// Restored after 037fd8f reverted it: that commit took the whole handler file
	// from an unreviewed branch predating the finding-16 fix, so the raw detail
	// came back with it. Nothing failed, because the redaction had no test on
	// this plane.
	errorHandler := func(w http.ResponseWriter, r *http.Request, err error) {
		correlation := audit.CorrelationID(r.Context())
		logger.Error("platform request failed",
			"error", err, "correlation_id", correlation,
			"method", r.Method, "path", r.URL.Path,
		)
		detail := "correlation id " + correlation
		writeError(w, http.StatusInternalServerError, Error{
			Message: "internal server error",
			Detail:  &detail,
		})
	}
	// A request the strict layer cannot decode never reaches a handler, so the
	// handler's own audit event is never opened and the attempt left no trace.
	// Authentication runs BEFORE this — it wraps the routed handler below — so
	// the caller is already known: this is a named principal probing the API's
	// shape, which is exactly the entry an investigation wants (duf-i2u).
	malformed := func(w http.ResponseWriter, r *http.Request, err error) {
		auditMalformedRequest(r)
		detail := err.Error()
		writeError(w, http.StatusBadRequest, Error{
			Message: "invalid request",
			Detail:  &detail,
		})
	}
	strict := NewStrictHandlerWithOptions(s, nil, StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  malformed,
		ResponseErrorHandlerFunc: errorHandler,
	})
	routed := HandlerWithOptions(strict, StdHTTPServerOptions{
		ErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			detail := err.Error()
			writeError(w, http.StatusBadRequest, Error{
				Message: "invalid request",
				Detail:  &detail,
			})
		},
	})
	// Authentication wraps everything, so a route added later is protected
	// without anyone remembering to protect it. Initialization is the single
	// exception and is named in the middleware rather than left implicit.
	return withDescriptors(authenticate(auth, principals, now, routed))
}

func (s *server) ListOrganizations(
	ctx context.Context,
	_ ListOrganizationsRequestObject,
) (ListOrganizationsResponseObject, error) {
	caller, refused := authorizePlatform(ctx, identity.RoleReader)
	if refused != permitted {
		return newRefusal(refused), nil
	}
	// Filtered by the caller's scope, not listed wholesale (ADR-0016).
	organizations, err := s.repository.ListOrganizationsForPrincipal(ctx, caller)
	if err != nil {
		return nil, err
	}
	response := ListOrganizations200JSONResponse{
		Organizations: make([]Organization, 0, len(organizations)),
	}
	for i := range organizations {
		organization, err := renderOrganization(organizations[i])
		if err != nil {
			return nil, err
		}
		response.Organizations = append(response.Organizations, organization)
	}
	return response, nil
}

func (s *server) CreateOrganization(
	ctx context.Context,
	request CreateOrganizationRequestObject,
) (CreateOrganizationResponseObject, error) {
	if _, refused := authorizePlatform(ctx, identity.RoleRoot); refused != permitted {
		return newRefusal(refused), nil
	}
	if request.Body == nil || !validName(request.Body.Name) {
		return badRequestResponse{message: "organization name must contain 1 to 200 characters"}, nil
	}
	organization, err := s.repository.CreateOrganization(ctx, store.Organization{
		ID:        uuid.NewString(),
		Name:      request.Body.Name,
		CreatedAt: s.now().UTC(),
	})
	if errors.Is(err, registry.ErrConflict) {
		return CreateOrganization409JSONResponse{
			ConflictJSONResponse: ConflictJSONResponse{
				Message: "organization already exists",
			},
		}, nil
	}
	if err != nil {
		return nil, err
	}
	rendered, err := renderOrganization(*organization)
	if err != nil {
		return nil, err
	}
	return CreateOrganization201JSONResponse(rendered), nil
}

func (s *server) GetOrganization(
	ctx context.Context,
	request GetOrganizationRequestObject,
) (GetOrganizationResponseObject, error) {
	if _, refused := authorizeOrganizationVisibility(ctx, identity.RoleReader, request.OrganizationId); refused != permitted {
		return newRefusal(refused), nil
	}
	organization, err := s.repository.GetOrganization(ctx, request.OrganizationId.String())
	if errors.Is(err, registry.ErrNotFound) {
		return GetOrganization404JSONResponse{
			NotFoundJSONResponse: NotFoundJSONResponse{
				Message: "organization not found",
			},
		}, nil
	}
	if err != nil {
		return nil, err
	}
	rendered, err := renderOrganization(*organization)
	if err != nil {
		return nil, err
	}
	return GetOrganization200JSONResponse(rendered), nil
}

func (s *server) DeleteOrganization(
	ctx context.Context,
	request DeleteOrganizationRequestObject,
) (DeleteOrganizationResponseObject, error) {
	// Organisation lifecycle is platform surface, not tenancy surface: ADR-0019
	// places organisations under root, and root is platform-scoped only. The
	// tenancy dimension is therefore vacuous — no non-root caller passes either
	// form — so the platform check says what is actually true. Reading an
	// organisation stays tenancy-scoped (GetOrganization); creating and deleting
	// one do not.
	//
	// Ratified deliberately: 037fd8f changed this from authorizeTenancy by
	// taking a whole file from a branch that predated 4f3281e, announcing it
	// nowhere. Same mechanism as the finding-16 revert in the same commit. The
	// form below is the intended one, and TestDeleteOrganizationIsAPlatform-
	// Operation pins it so the next silent swap fails a gate.
	if _, refused := authorizePlatform(ctx, identity.RoleRoot); refused != permitted {
		return newRefusal(refused), nil
	}
	err := s.repository.DeleteOrganization(ctx, request.OrganizationId.String())
	if errors.Is(err, registry.ErrNotFound) {
		return DeleteOrganization404JSONResponse{
			NotFoundJSONResponse: NotFoundJSONResponse{
				Message: "organization not found",
			},
		}, nil
	}
	if errors.Is(err, registry.ErrConflict) {
		return DeleteOrganization409JSONResponse{
			Message: "organization still has projects or organization-scoped principals",
		}, nil
	}
	if err != nil {
		return nil, err
	}
	return DeleteOrganization204Response{}, nil
}

func (s *server) ListProjects(
	ctx context.Context,
	request ListProjectsRequestObject,
) (ListProjectsResponseObject, error) {
	caller, refused := authorizeOrganizationVisibility(ctx, identity.RoleReader, request.OrganizationId)
	if refused != permitted {
		return newRefusal(refused), nil
	}
	projects, err := s.repository.ListProjectsForPrincipal(ctx, caller, request.OrganizationId)
	if err != nil {
		return nil, err
	}
	response := ListProjects200JSONResponse{
		Projects: make([]Project, 0, len(projects)),
	}
	for i := range projects {
		project, err := renderProject(projects[i])
		if err != nil {
			return nil, err
		}
		response.Projects = append(response.Projects, project)
	}
	return response, nil
}

func (s *server) CreateProject(
	ctx context.Context,
	request CreateProjectRequestObject,
) (CreateProjectResponseObject, error) {
	if _, refused := authorizeTenancy(ctx, identity.RoleMaintainer, request.OrganizationId.String(), ""); refused != permitted {
		return newRefusal(refused), nil
	}
	if request.Body == nil || !validName(request.Body.Name) {
		return badRequestResponse{message: "project name must contain 1 to 200 characters"}, nil
	}
	project, err := s.repository.CreateProject(ctx, store.Project{
		ID:             uuid.NewString(),
		OrganizationID: request.OrganizationId.String(),
		Name:           request.Body.Name,
		CreatedAt:      s.now().UTC(),
	})
	if errors.Is(err, registry.ErrConflict) {
		return CreateProject409JSONResponse{
			ConflictJSONResponse: ConflictJSONResponse{
				Message: "project already exists or its organization is missing",
			},
		}, nil
	}
	if err != nil {
		return nil, err
	}
	rendered, err := renderProject(*project)
	if err != nil {
		return nil, err
	}
	return CreateProject201JSONResponse(rendered), nil
}

func (s *server) GetProject(
	ctx context.Context,
	request GetProjectRequestObject,
) (GetProjectResponseObject, error) {
	if _, refused := authorizeTenancy(ctx, identity.RoleReader, request.OrganizationId.String(), request.ProjectId.String()); refused != permitted {
		return newRefusal(refused), nil
	}
	project, err := s.repository.GetProject(
		ctx,
		request.OrganizationId.String(),
		request.ProjectId.String(),
	)
	if errors.Is(err, registry.ErrNotFound) {
		return GetProject404JSONResponse{
			NotFoundJSONResponse: NotFoundJSONResponse{
				Message: "project not found",
			},
		}, nil
	}
	if err != nil {
		return nil, err
	}
	rendered, err := renderProject(*project)
	if err != nil {
		return nil, err
	}
	return GetProject200JSONResponse(rendered), nil
}

func (s *server) DeleteProject(
	ctx context.Context,
	request DeleteProjectRequestObject,
) (DeleteProjectResponseObject, error) {
	if _, refused := authorizeTenancy(ctx, identity.RoleMaintainer, request.OrganizationId.String(), request.ProjectId.String()); refused != permitted {
		return newRefusal(refused), nil
	}
	err := s.repository.DeleteProject(
		ctx,
		request.OrganizationId.String(),
		request.ProjectId.String(),
	)
	if errors.Is(err, registry.ErrNotFound) {
		return DeleteProject404JSONResponse{
			NotFoundJSONResponse: NotFoundJSONResponse{
				Message: "project not found",
			},
		}, nil
	}
	if errors.Is(err, registry.ErrConflict) {
		return DeleteProject409JSONResponse{
			Message: "project still has buckets or project-scoped principals",
		}, nil
	}
	if err != nil {
		return nil, err
	}
	return DeleteProject204Response{}, nil
}

func renderOrganization(organization store.Organization) (Organization, error) {
	id, err := uuid.Parse(organization.ID)
	if err != nil {
		return Organization{}, fmt.Errorf("render organization id: %w", err)
	}
	return Organization{
		Id:        id,
		Name:      organization.Name,
		CreatedAt: organization.CreatedAt,
	}, nil
}

func renderProject(project store.Project) (Project, error) {
	id, err := uuid.Parse(project.ID)
	if err != nil {
		return Project{}, fmt.Errorf("render project id: %w", err)
	}
	organizationID, err := uuid.Parse(project.OrganizationID)
	if err != nil {
		return Project{}, fmt.Errorf("render project organization id: %w", err)
	}
	return Project{
		Id:             id,
		OrganizationId: organizationID,
		Name:           project.Name,
		CreatedAt:      project.CreatedAt,
	}, nil
}

func validName(name string) bool {
	length := utf8.RuneCountInString(name)
	return length >= 1 && length <= 200
}

type badRequestResponse struct {
	message string
}

func (response badRequestResponse) VisitCreateOrganizationResponse(w http.ResponseWriter) error {
	writeError(w, http.StatusBadRequest, Error{Message: response.message})
	return nil
}

func (response badRequestResponse) VisitCreateProjectResponse(w http.ResponseWriter) error {
	writeError(w, http.StatusBadRequest, Error{Message: response.message})
	return nil
}

func (response badRequestResponse) VisitCreatePrincipalResponse(w http.ResponseWriter) error {
	writeError(w, http.StatusBadRequest, Error{Message: response.message})
	return nil
}

func writeError(w http.ResponseWriter, status int, body Error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (s *server) ListPins(
	ctx context.Context,
	request ListPinsRequestObject,
) (ListPinsResponseObject, error) {
	audit := s.beginLifecycleAudit()
	defer func() { audit.log(ctx) }()

	caller, refused := authorizeTenancy(
		ctx, identity.RoleReader, request.OrganizationId.String(), request.ProjectId.String(),
	)
	if refused != permitted {
		audit.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audit.actor(caller)
	pins, err := s.repository.ListPins(
		ctx, store.ParseTenant(request.OrganizationId.String(), request.ProjectId.String()),
	)
	if err != nil {
		audit.failed("storage_failed")
		return nil, err
	}
	audit.succeeded("", "")
	response := ListPins200JSONResponse{Pins: make([]Pin, 0, len(pins))}
	for _, pin := range pins {
		response.Pins = append(response.Pins, renderPin(pin))
	}
	return response, nil
}

func (s *server) DeletePin(
	ctx context.Context,
	request DeletePinRequestObject,
) (DeletePinResponseObject, error) {
	audit := s.beginLifecycleAudit()
	audit.event.TargetID = request.BucketName
	defer func() { audit.log(ctx) }()

	caller, refused := authorizeTenancy(
		ctx, identity.RoleBuilder, request.OrganizationId.String(), request.ProjectId.String(),
	)
	if refused != permitted {
		audit.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audit.actor(caller)
	if err := s.repository.DeletePin(
		ctx,
		store.ParseTenant(request.OrganizationId.String(), request.ProjectId.String()),
		request.BucketName,
	); err != nil {
		audit.failed("storage_failed")
		return nil, err
	}
	audit.succeeded(request.BucketName, "")
	return DeletePin204Response{}, nil
}

func (s *server) SetPin(
	ctx context.Context,
	request SetPinRequestObject,
) (SetPinResponseObject, error) {
	audit := s.beginLifecycleAudit()
	audit.event.TargetID = request.BucketName
	defer func() { audit.log(ctx) }()

	caller, refused := authorizeTenancy(
		ctx, identity.RoleBuilder, request.OrganizationId.String(), request.ProjectId.String(),
	)
	if refused != permitted {
		audit.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audit.actor(caller)
	pin, err := s.repository.SetPin(
		ctx,
		store.ParseTenant(request.OrganizationId.String(), request.ProjectId.String()),
		request.BucketName,
		caller.ID,
		s.now().UTC(),
	)
	if errors.Is(err, registry.ErrNotFound) {
		audit.refused("not_found")
		return SetPin404JSONResponse{Message: "No such bucket in this project"}, nil
	}
	if err != nil {
		audit.failed("storage_failed")
		return nil, err
	}
	audit.succeeded(request.BucketName, "")
	return SetPin200JSONResponse(renderPin(*pin)), nil
}

func renderPin(pin store.Pin) Pin {
	return Pin{
		BucketName: pin.BucketName,
		PinnedAt:   pin.PinnedAt,
		PinnedBy:   &pin.PinnedBy,
	}
}

func (s *server) ListPrincipals(
	ctx context.Context,
	request ListPrincipalsRequestObject,
) (ListPrincipalsResponseObject, error) {
	// Audited like the single-principal read. Recording who read ONE principal
	// while ignoring a refused attempt to enumerate ALL of them is incoherent:
	// principal.read exists so the finding-9 probe is visible, and enumeration
	// is the broader version of that probe (duf-i2u).
	audit := s.beginLifecycleAudit()
	defer func() { audit.log(ctx) }()

	// The selection being listed: explicit in the query, defaulting to the
	// scope the caller itself stands at. Exact scope, never a subtree —
	// see-where-you-stand and create-where-you-stand are the same rule
	// (duf-4qr). A root session browsing a tenancy names it here; a request
	// naming nothing lists the caller's own standing, which for the only
	// platform-scoped role is the platform principals.
	standing, ok := callerFrom(ctx)
	if !ok {
		audit.refused(refusedTenancy.reason())
		return newRefusal(refusedTenancy), nil
	}
	selection := standing.Scope
	if request.Params.OrganizationId != nil || request.Params.ProjectId != nil {
		selection = identity.Scope{}
		if request.Params.OrganizationId != nil {
			selection.OrganizationID = *request.Params.OrganizationId
		}
		if request.Params.ProjectId != nil {
			selection.ProjectID = *request.Params.ProjectId
		}
	}

	// Authorized against the scope being LISTED, not merely the caller's role.
	// The selection is never trusted past this point: a tenancy caller naming
	// a foreign scope — or a project_id without its organization, which
	// authorizeTenancy refuses as an empty organisation — is answered
	// not-found, exactly as if the scope did not exist (ADR-0017).
	caller, refused := authorizeScope(ctx, identity.RoleMaintainer, selection)
	if refused != permitted {
		audit.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audit.actor(caller)

	principals, err := s.repository.ListPrincipals(ctx, caller, selection)
	if err != nil {
		audit.failed("storage_failed")
		return nil, err
	}
	audit.succeeded("", "")
	response := ListPrincipals200JSONResponse{
		Principals: make([]Principal, 0, len(principals)),
	}
	for _, principal := range principals {
		response.Principals = append(response.Principals, renderPrincipal(principal))
	}
	return response, nil
}

func (s *server) CreatePrincipal(
	ctx context.Context,
	request CreatePrincipalRequestObject,
) (CreatePrincipalResponseObject, error) {
	if request.Body == nil {
		return badRequestResponse{message: "principal body is required"}, nil
	}
	audit := s.beginLifecycleAudit()
	defer func() { audit.log(ctx) }()

	requested := identity.Scope{}
	if request.Body.OrganizationId != nil {
		requested.OrganizationID = *request.Body.OrganizationId
	}
	if request.Body.ProjectId != nil {
		requested.ProjectID = *request.Body.ProjectId
	}
	// Authorized against the scope being created IN, so creating a
	// platform-scoped principal requires root rather than skipping the tenancy
	// check for want of a tenancy (review findings 9 and 10).
	caller, refused := authorizeScope(ctx, identity.RoleMaintainer, requested)
	if refused != permitted {
		audit.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audit.actor(caller)

	role, err := identity.ParseRole(string(request.Body.Role))
	if err != nil {
		audit.refused("invalid_role")
		return badRequestResponse{message: "principal role is invalid"}, nil
	}
	if !caller.Role.MayGrant(role) {
		audit.refused("role_exceeds_grantor")
		return newRefusal(refusedRole), nil
	}
	if !validName(request.Body.Name) {
		audit.refused("invalid_name")
		return badRequestResponse{message: "principal name must contain 1 to 200 characters"}, nil
	}

	now := s.now().UTC()
	clientID := uuid.NewString()
	// Created with no secret. Issuing one is a separate, explicit call to
	// CreatePrincipalSecret — the same path rotation uses (duf-4ac).
	principal, err := identity.NewPrincipal(
		uuid.NewString(), request.Body.Name, clientID, requested, role, now,
	)
	if errors.Is(err, identity.ErrInvalid) {
		audit.refused("invalid_scope_or_role")
		return badRequestResponse{message: "principal scope and role are invalid"}, nil
	}
	if err != nil {
		audit.failed("construct_failed")
		return nil, err
	}
	if err := s.repository.CreatePrincipal(ctx, principal); errors.Is(err, identity.ErrConflict) {
		audit.failed("already_exists")
		return CreatePrincipal409JSONResponse{
			ConflictJSONResponse: ConflictJSONResponse{Message: "principal already exists"},
		}, nil
	} else if err != nil {
		audit.failed("storage_failed")
		return nil, err
	}
	audit.succeeded(principal.ID, string(role))

	// No secret in the body, because none was minted. The type carries the
	// guarantee: CreatePrincipal201JSONResponse is a plain Principal, so a
	// create-time credential is not merely absent but unrepresentable.
	return CreatePrincipal201JSONResponse(renderPrincipal(principal)), nil
}

func (s *server) DeletePrincipal(
	ctx context.Context,
	request DeletePrincipalRequestObject,
) (DeletePrincipalResponseObject, error) {
	audit := s.beginLifecycleAudit()
	audit.event.TargetID = request.PrincipalId
	defer func() { audit.log(ctx) }()

	caller, target, refused, err := s.principalForModification(ctx, request.PrincipalId)
	if errors.Is(err, identity.ErrNotFound) {
		audit.refused("not_found")
		return DeletePrincipal404JSONResponse{
			NotFoundJSONResponse: NotFoundJSONResponse{Message: "not found"},
		}, nil
	}
	if err != nil {
		audit.failed("lookup_failed")
		return nil, err
	}
	if refused != permitted {
		audit.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audit.actor(caller)

	if caller.ID == target.ID {
		audit.refused("self_deletion")
		return DeletePrincipal403JSONResponse{
			Message: "a principal may not delete itself",
		}, nil
	}
	if !caller.Role.MayModifyHolderOf(target.Role) {
		audit.refused("target_role_exceeds_caller")
		return newRefusal(refusedRole), nil
	}
	if err := s.repository.DeletePrincipal(ctx, target.ID); errors.Is(err, identity.ErrConflict) {
		audit.failed("last_root")
		return DeletePrincipal409JSONResponse{
			Message: "the last root principal cannot be deleted",
		}, nil
	} else if errors.Is(err, identity.ErrNotFound) {
		audit.refused("not_found")
		return DeletePrincipal404JSONResponse{
			NotFoundJSONResponse: NotFoundJSONResponse{Message: "not found"},
		}, nil
	} else if err != nil {
		audit.failed("storage_failed")
		return nil, err
	}
	audit.succeeded(target.ID, string(target.Role))
	return DeletePrincipal204Response{}, nil
}

func (s *server) GetPrincipal(
	ctx context.Context,
	request GetPrincipalRequestObject,
) (GetPrincipalResponseObject, error) {
	audit := s.beginLifecycleAudit()
	audit.event.TargetID = request.PrincipalId
	defer func() { audit.log(ctx) }()

	principal, err := s.repository.GetPrincipalByID(ctx, request.PrincipalId)
	if errors.Is(err, identity.ErrNotFound) {
		audit.refused("not_found")
		return GetPrincipal404JSONResponse{
			NotFoundJSONResponse: NotFoundJSONResponse{Message: "not found"},
		}, nil
	}
	if err != nil {
		audit.failed("lookup_failed")
		return nil, err
	}
	caller, refused := authorizeScope(ctx, identity.RoleMaintainer, principal.Scope)
	if refused != permitted {
		audit.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audit.actor(caller)
	audit.succeeded("", "")

	rendered := renderPrincipal(principal)
	return GetPrincipal200JSONResponse(rendered), nil
}

func (s *server) CreatePrincipalSecret(
	ctx context.Context,
	request CreatePrincipalSecretRequestObject,
) (CreatePrincipalSecretResponseObject, error) {
	// Targets the PRINCIPAL until a secret exists to name.
	requestHandle := audit.FromContext(ctx)
	audit := s.beginLifecycleAudit()
	defer func() { audit.log(ctx) }()

	caller, target, refused, err := s.principalForModification(ctx, request.PrincipalId)
	if errors.Is(err, identity.ErrNotFound) {
		audit.refused("not_found")
		return CreatePrincipalSecret404JSONResponse{
			NotFoundJSONResponse: NotFoundJSONResponse{Message: "not found"},
		}, nil
	}
	if err != nil {
		audit.failed("lookup_failed")
		return nil, err
	}
	if refused != permitted {
		audit.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audit.actor(caller)

	if !caller.Role.MayModifyHolderOf(target.Role) {
		audit.refused("target_role_exceeds_caller")
		return newRefusal(refusedRole), nil
	}
	var expiresAt *time.Time
	if request.Body != nil && request.Body.ExpiresAt != nil {
		value := request.Body.ExpiresAt.UTC()
		expiresAt = &value
	}
	plaintext, secret, err := s.repository.IssuePrincipalSecret(
		ctx, target.ID, uuid.NewString(), expiresAt, s.now().UTC(),
	)
	if errors.Is(err, identity.ErrInvalid) {
		audit.refused("expiry_not_future")
		return CreatePrincipalSecret400JSONResponse{
			Message: "expires_at must be in the future",
		}, nil
	}
	if errors.Is(err, identity.ErrRootPermanence) {
		audit.refused("root_needs_permanent_secret")
		return CreatePrincipalSecret409JSONResponse{
			Message: "a root principal's secrets must include one that never expires; issue a never-expiring secret first",
		}, nil
	}
	if errors.Is(err, identity.ErrConflict) {
		audit.failed("two_usable_secrets")
		return CreatePrincipalSecret409JSONResponse{
			Message: "principal already has two usable secrets",
		}, nil
	}
	if errors.Is(err, identity.ErrNotFound) {
		audit.refused("not_found")
		return CreatePrincipalSecret404JSONResponse{
			NotFoundJSONResponse: NotFoundJSONResponse{Message: "not found"},
		}, nil
	}
	if err != nil {
		audit.failed("storage_failed")
		return nil, err
	}
	// The secret's ID, never its value: the plaintext leaves in the response body
	// and nowhere else (ADR-0012, ADR-0020).
	audit.succeeded(secret.ID, "")
	requestHandle.ClientSecret(plaintext)
	return CreatePrincipalSecret201JSONResponse{
		Id: secret.ID, Secret: plaintext, CreatedAt: secret.CreatedAt,
		ExpiresAt: secret.ExpiresAt,
	}, nil
}

func (s *server) RevokePrincipalSecret(
	ctx context.Context,
	request RevokePrincipalSecretRequestObject,
) (RevokePrincipalSecretResponseObject, error) {
	audit := s.beginLifecycleAudit()
	audit.event.TargetID = request.SecretId
	defer func() { audit.log(ctx) }()

	caller, target, refused, err := s.principalForModification(ctx, request.PrincipalId)
	if errors.Is(err, identity.ErrNotFound) {
		audit.refused("not_found")
		return RevokePrincipalSecret404JSONResponse{
			NotFoundJSONResponse: NotFoundJSONResponse{Message: "not found"},
		}, nil
	}
	if err != nil {
		audit.failed("lookup_failed")
		return nil, err
	}
	if refused != permitted {
		audit.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audit.actor(caller)

	// Audited symmetrically with the issue path. It was not, and the asymmetry
	// was invisible because neither path had a test.
	if !caller.Role.MayModifyHolderOf(target.Role) {
		audit.refused("target_role_exceeds_caller")
		return newRefusal(refusedRole), nil
	}
	err = s.repository.RevokePrincipalSecret(ctx, target.ID, request.SecretId, s.now().UTC())
	if errors.Is(err, identity.ErrConflict) {
		audit.failed("last_usable_root_secret")
		return RevokePrincipalSecret409JSONResponse{
			Message: "a root principal must keep a usable secret that never expires",
		}, nil
	}
	if errors.Is(err, identity.ErrNotFound) {
		audit.refused("not_found")
		return RevokePrincipalSecret404JSONResponse{
			NotFoundJSONResponse: NotFoundJSONResponse{Message: "not found"},
		}, nil
	}
	if err != nil {
		audit.failed("storage_failed")
		return nil, err
	}
	audit.succeeded("", "")
	return RevokePrincipalSecret204Response{}, nil
}

func (s *server) principalForModification(
	ctx context.Context, principalID string,
) (*identity.Principal, *identity.Principal, refusal, error) {
	target, err := s.repository.GetPrincipalByID(ctx, principalID)
	if err != nil {
		return nil, nil, permitted, err
	}
	// Authorized against the TARGET's scope, so a platform-scoped target requires
	// root rather than being reachable by any maintainer (review finding 9).
	caller, refused := authorizeScope(ctx, identity.RoleMaintainer, target.Scope)
	return caller, target, refused, nil
}

func renderPrincipal(principal *identity.Principal) Principal {
	secrets := principal.Secrets()
	response := Principal{
		Id:        principal.ID,
		Name:      principal.Name,
		ClientId:  principal.ClientID,
		Role:      Role(principal.Role),
		CreatedAt: principal.CreatedAt,
		Secrets:   make([]SecretMetadata, 0, len(secrets)),
	}
	if principal.Scope.OrganizationID != uuid.Nil {
		organizationID := principal.Scope.OrganizationID
		response.OrganizationId = &organizationID
	}
	if principal.Scope.ProjectID != uuid.Nil {
		projectID := principal.Scope.ProjectID
		response.ProjectId = &projectID
	}
	for _, secret := range secrets {
		response.Secrets = append(response.Secrets, SecretMetadata{
			Id: secret.ID, CreatedAt: secret.CreatedAt, LastUsedAt: secret.LastUsedAt,
			ExpiresAt: secret.ExpiresAt,
		})
	}
	return response
}

// Initialize mints the first administrative principal and returns its
// credentials exactly once (ADR-0012).
//
// The single bootstrap mechanism: the first-run wizard and unattended setup
// use this call, then the ordinary authenticated platform endpoints. A
// privileged side door would be another way in and therefore another thing to
// secure, and its absence is what lets an end-to-end test bootstrap a server
// without a browser.
//
// Rejected alternatives, recorded because both look easier: a mounted secret
// forces a runtime assumption, when this must deploy equally on Kubernetes, a
// VM or plain Docker; and writing the credential to the log turns a live
// full-admin credential into whatever the log retention policy happens to be.
//
// ACCEPTED RISK, documented rather than engineered around: while uninitialized,
// whoever reaches this endpoint first owns the deployment. Do not expose an
// uninitialized instance publicly.
func (s *server) Initialize(
	ctx context.Context,
	request InitializeRequestObject,
) (InitializeResponseObject, error) {
	now := s.now().UTC()
	handle := audit.FromContext(ctx)
	event := audit.Enrichment{
		PrincipalID: "anonymous", IdentityKind: identity.IdentityKindAnonymous,
		Scope: identity.AuditScopePlatform, Outcome: identity.AuditOutcomeFailure,
	}
	record := func(outcome identity.AuditOutcome, reason string) {
		event.Outcome = outcome
		event.Reason = reason
	}
	defer func() { handle.Enrich(event) }()

	// Recovery share parameters are fixed here, at initialization — there is
	// no rekey ceremony (ADR-0024). The 1-of-1 default keeps the
	// single-operator experience to one recovery key.
	shareCount, threshold := 1, 1
	if request.Body != nil {
		if request.Body.RecoveryShareCount != nil {
			shareCount = *request.Body.RecoveryShareCount
		}
		if request.Body.RecoveryThreshold != nil {
			threshold = *request.Body.RecoveryThreshold
		}
	}
	shares, digest, err := identity.NewRecoveryShares(shareCount, threshold)
	if err != nil {
		if errors.Is(err, identity.ErrInvalid) {
			record(identity.AuditOutcomeRefused, "invalid_recovery_parameters")
			detail := err.Error()
			return Initialize400JSONResponse{
				Message: "invalid recovery share parameters", Detail: &detail,
			}, nil
		}
		record(identity.AuditOutcomeFailure, "recovery_share_generation_failed")
		return nil, err
	}

	// PLATFORM-scoped root, not organization-scoped. The bootstrap identity sits
	// above every tenancy rather than inside the first one: it must be able to
	// create further organizations, and an identity confined to the organization
	// it happens to have created first could not (ADR-0019).
	//
	// Root outranks the tenancy question rather than satisfying it. The wizard
	// uses this credential to create the first organization and project through
	// the ordinary authenticated platform endpoints.
	clientID := uuid.NewString()
	principal, err := identity.NewPrincipal(
		uuid.NewString(), "initial administrator", clientID,
		identity.Scope{}, identity.RoleRoot, now,
	)
	if err != nil {
		record(identity.AuditOutcomeFailure, "principal_creation_failed")
		return nil, err
	}

	// Bootstrap is the one place a principal MUST arrive with a credential —
	// there is no authenticated caller to issue one afterwards, which is the
	// loop /sys/init exists to break. It issues through IssueSecret rather than
	// a create-time shortcut, so every credential in the system is minted by the
	// same code (duf-4ac).
	secretID := uuid.NewString()
	secret, err := principal.IssueSecret(secretID, nil, now)
	if err != nil {
		record(identity.AuditOutcomeFailure, "secret_issue_failed")
		return nil, err
	}

	if err := s.instance.InitializeInstance(ctx, principal, digest, threshold); err != nil {
		if errors.Is(err, registry.ErrConflict) {
			record(identity.AuditOutcomeRefused, "already_initialized")
			return Initialize409JSONResponse{Message: "already initialized"}, nil
		}
		record(identity.AuditOutcomeFailure, "initialize_failed")
		return nil, err
	}

	record(identity.AuditOutcomeSuccess, "")
	handle.BootstrapSecret(secret)
	for _, share := range shares {
		handle.RecoveryShare(share)
	}
	// The secret and shares reach the caller here and nowhere else. Neither is
	// logged; what was stored is an argon2id hash and a digest, and neither can
	// be recovered from.
	return Initialize200JSONResponse{
		ClientId: clientID, ClientSecret: secret,
		RecoveryShares: shares, RecoveryThreshold: threshold,
	}, nil
}

// Recover mints a fresh root principal for a caller proving custody of the
// recovery shares (ADR-0024). The ceremony is deliberately loud: a new
// principal rather than a resurrected one, on a distinct audited operation —
// any k custodians signing in silently as root is exactly what the
// splitting-the-secret design was rejected for.
func (s *server) Recover(
	ctx context.Context,
	request RecoverRequestObject,
) (RecoverResponseObject, error) {
	now := s.now().UTC()
	handle := audit.FromContext(ctx)
	event := audit.Enrichment{
		PrincipalID: "anonymous", IdentityKind: identity.IdentityKindAnonymous,
		Scope: identity.AuditScopePlatform, Outcome: identity.AuditOutcomeFailure,
	}
	record := func(outcome identity.AuditOutcome, reason string) {
		event.Outcome = outcome
		event.Reason = reason
	}
	defer func() { handle.Enrich(event) }()
	// Recorded before verification, so a refused ceremony still names the
	// shares it was attempted with.
	for _, share := range request.Body.Shares {
		handle.RecoveryShare(share)
	}

	digest, threshold, err := s.instance.RecoveryVerifier(ctx)
	if errors.Is(err, identity.ErrNotFound) {
		record(identity.AuditOutcomeRefused, "recovery_not_configured")
		return Recover409JSONResponse{Message: "recovery is not configured on this instance"}, nil
	}
	if err != nil {
		record(identity.AuditOutcomeFailure, "recovery_verifier_read_failed")
		return nil, err
	}

	if err := identity.VerifyRecoveryShares(request.Body.Shares, threshold, digest); err != nil {
		if errors.Is(err, identity.ErrInvalid) {
			record(identity.AuditOutcomeRefused, "invalid_shares")
			detail := err.Error()
			return Recover400JSONResponse{Message: "invalid recovery shares", Detail: &detail}, nil
		}
		record(identity.AuditOutcomeRefused, "shares_rejected")
		return Recover403JSONResponse{Message: "recovery refused"}, nil
	}

	clientID := uuid.NewString()
	principal, err := identity.NewPrincipal(
		uuid.NewString(), "recovered administrator", clientID,
		identity.Scope{}, identity.RoleRoot, now,
	)
	if err != nil {
		record(identity.AuditOutcomeFailure, "principal_creation_failed")
		return nil, err
	}
	secret, err := principal.IssueSecret(uuid.NewString(), nil, now)
	if err != nil {
		record(identity.AuditOutcomeFailure, "secret_issue_failed")
		return nil, err
	}
	if err := s.repository.CreatePrincipal(ctx, principal); err != nil {
		record(identity.AuditOutcomeFailure, "principal_persist_failed")
		return nil, err
	}

	record(identity.AuditOutcomeSuccess, "")
	event.TargetID = principal.ID
	handle.BootstrapSecret(secret)
	// The credentials reach the caller here and nowhere else, exactly the
	// /sys/init contract. The shares remain valid: they are custody proof, not
	// a consumable, and there is no rotation ceremony to invalidate them.
	return Recover200JSONResponse{ClientId: clientID, ClientSecret: secret}, nil
}

type backingState struct {
	initialized   bool
	initializedAt *time.Time
	database      bool
	audit         HealthAudit
	objectStorage HealthObjectStorage
	encryption    HealthEncryption
	scanner       HealthScanner
	scannerConfig InstanceScanner
}

// backingState reads the same remembered state for both the public health
// probe and the authenticated instance description.
func (s *server) backingState(ctx context.Context) (backingState, error) {
	auditState := HealthAuditDisabled
	if s.auditBroker != nil {
		health := s.auditBroker.Health()
		healthy := 0
		for _, target := range health {
			if target.Status == audit.SinkStatusHealthy {
				healthy++
			}
		}
		switch {
		case len(health) == 0:
			auditState = HealthAuditDisabled
		case healthy == len(health):
			auditState = HealthAuditOk
		case healthy == 0:
			auditState = HealthAuditDegraded
		default:
			auditState = HealthAuditPartial
		}
	}
	objectStorageState := HealthObjectStorage(s.repository.ObjectStorageState())
	scannerState, scannerConfig := s.scannerStates()
	encryptionState := HealthEncryptionUnconfigured
	if s.encryption != nil {
		encryptionState = HealthEncryption(s.encryption.State())
	}
	initialized, initializedAt, database, err := s.instance.InstanceStatus(ctx)
	if err != nil {
		return backingState{
			audit: auditState, objectStorage: objectStorageState, encryption: encryptionState,
			scanner: scannerState, scannerConfig: scannerConfig,
		}, err
	}
	return backingState{
		initialized: initialized, initializedAt: initializedAt, database: database,
		audit: auditState, objectStorage: objectStorageState, encryption: encryptionState,
		scanner: scannerState, scannerConfig: scannerConfig,
	}, nil
}

func (s *server) GetInstance(
	ctx context.Context,
	_ GetInstanceRequestObject,
) (GetInstanceResponseObject, error) {
	if _, refused := authorizePlatform(ctx, identity.RoleReader); refused != permitted {
		return newRefusal(refused), nil
	}
	state, err := s.backingState(ctx)
	if err != nil {
		// Same disclosure posture as /sys/health: the response carries only the
		// false reachability bit, while infrastructure detail stays server-side.
		s.logger.Error("instance read could not read backing state", "error", err)
	}
	auditState := InstanceAuditEnabled
	if state.audit == HealthAuditDisabled {
		auditState = InstanceAuditDisabled
	}
	return GetInstance200JSONResponse{
		Version: s.build.Version, Commit: s.build.Commit,
		ApiVersions:   append([]string(nil), s.build.APIVersions...),
		InitializedAt: state.initializedAt,
		Store:         state.database,
		ObjectStorage: InstanceObjectStorage(state.objectStorage),
		Audit:         auditState,
		Encryption:    InstanceEncryption(state.encryption),
		Scanner:       state.scannerConfig,
	}, nil
}

func (s *server) GetSelf(
	ctx context.Context,
	_ GetSelfRequestObject,
) (GetSelfResponseObject, error) {
	caller, refused := authorizePlatform(ctx, identity.RoleReader)
	if refused != permitted {
		return newRefusal(refused), nil
	}
	response := GetSelf200JSONResponse{
		PrincipalId: caller.ID,
		Name:        caller.Name,
		Role:        Role(caller.Role),
	}
	if caller.Scope.OrganizationID != uuid.Nil {
		organizationID := caller.Scope.OrganizationID
		response.OrganizationId = &organizationID
	}
	if caller.Scope.ProjectID != uuid.Nil {
		projectID := caller.Scope.ProjectID
		response.ProjectId = &projectID
	}
	return response, nil
}

// Health is the unauthenticated probe, modelled on Vault's /sys/health: the
// status code carries the state so a readiness probe needs no body parsing.
// 501 means reachable-but-unclaimed, which is how the console decides that
// first run is the wizard rather than the sign-in screen without POSTing to
// /init and thereby claiming the instance.
func (s *server) Health(ctx context.Context, _ HealthRequestObject) (HealthResponseObject, error) {
	state, err := s.backingState(ctx)
	if err != nil {
		// The reason is server-side only: this response reaches an anonymous
		// caller, and pg errors name hosts and schema (review finding 16).
		s.logger.Error("health probe could not read instance status", "error", err)
		return Health503JSONResponse{
			Initialized: false, Database: false,
			Audit: state.audit, ObjectStorage: state.objectStorage,
			Encryption: state.encryption, Scanner: state.scanner,
		}, nil
	}
	if state.audit == HealthAuditDegraded {
		return Health503JSONResponse{
			Initialized: state.initialized, Database: state.database,
			Audit: state.audit, ObjectStorage: state.objectStorage,
			Encryption: state.encryption, Scanner: state.scanner,
		}, nil
	}
	if !state.initialized {
		return Health501JSONResponse{
			Initialized: false, Database: state.database,
			Audit: state.audit, ObjectStorage: state.objectStorage,
			Encryption: state.encryption, Scanner: state.scanner,
		}, nil
	}
	// An unreachable or absent store is not a reason to fail the probe: only
	// SBOM upload depends on it, and the rest of the registry still answers.
	return Health200JSONResponse{
		Initialized: true, Database: state.database,
		Audit: state.audit, ObjectStorage: state.objectStorage,
		Encryption: state.encryption, Scanner: state.scanner,
	}, nil
}
