package bagdrop

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/domain/identity"
)

var (
	ErrReconcilerNotRunning = errors.New("bag drop reconciler is not running in this process")
	ErrAuditUnavailable     = errors.New("bag drop sync audit is unavailable")
)

type ReconcileRepository interface {
	ListBagDropProjects(context.Context) ([]Project, error)
	GetBagDropConfig(context.Context, string, string) (*Record, error)
	ListBagDropAssociations(context.Context, string, string) ([]Association, error)
	GetBagDropBucketSnapshot(context.Context, string, string, string) (*BucketSnapshot, error)
	MarkBagDropAssociationAttempt(context.Context, string, string, string, time.Time) error
	RecordBagDropAssociationSuccess(context.Context, string, string, string, time.Time) error
	RecordBagDropAssociationFailure(context.Context, string, string, string, string, time.Time) error
}

type Reconciler struct {
	repository ReconcileRepository
	sealer     *CredentialSealer
	adapters   Registry
	audit      *audit.SystemEmitter
	interval   time.Duration
	now        func() time.Time
	logger     *slog.Logger
	trigger    chan struct{}
	started    chan struct{}
	startOnce  sync.Once

	mu       sync.Mutex
	running  bool
	pending  map[Project]bool
	queue    []Project
	backoffs map[Project]projectBackoff
}

// Runtime composes the request-facing configuration service with the
// process-owned reconciliation loop for platform handler injection.
type Runtime struct {
	*Service
	*Reconciler
}

type projectBackoff struct {
	failures int
	next     time.Time
}

func NewReconciler(
	repository ReconcileRepository, sealer *CredentialSealer, adapters Registry,
	writer audit.Writer, interval time.Duration, logger *slog.Logger,
) (*Reconciler, error) {
	if repository == nil || sealer == nil || writer == nil {
		return nil, errors.New("bag drop reconciler requires repository, sealer, and audit writer")
	}
	if interval <= 0 {
		return nil, errors.New("bag drop reconcile interval must be positive")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Reconciler{
		repository: repository, sealer: sealer, adapters: adapters,
		audit: audit.NewBagDropEmitter(writer), interval: interval, now: time.Now, logger: logger,
		trigger: make(chan struct{}, 1), started: make(chan struct{}),
		pending: make(map[Project]bool), backoffs: make(map[Project]projectBackoff),
	}, nil
}

func (r *Reconciler) Run(ctx context.Context) {
	r.mu.Lock()
	r.running = true
	r.mu.Unlock()
	r.startOnce.Do(func() { close(r.started) })
	defer func() {
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
	}()

	timer := time.NewTimer(0)
	defer timer.Stop()
	paused := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			paused = false
			if err := r.ReconcileAll(ctx); errors.Is(err, ErrAuditUnavailable) {
				paused = true
			} else if err != nil && ctx.Err() == nil {
				r.logger.Warn("Bag Drop reconcile pass failed")
			}
			timer.Reset(r.interval)
		case <-r.trigger:
			for {
				project, ok := r.nextTriggeredProject()
				if !ok {
					break
				}
				if paused {
					continue
				}
				if err := r.reconcileAndBackoff(ctx, project); errors.Is(err, ErrAuditUnavailable) {
					paused = true
				} else if err != nil && ctx.Err() == nil {
					r.logger.Warn("Bag Drop project reconcile failed",
						"organization_id", project.OrganizationID, "project_id", project.ProjectID)
				}
			}
		}
	}
}

// Started is closed once Run is accepting on-demand triggers.
func (r *Reconciler) Started() <-chan struct{} { return r.started }

func (r *Reconciler) Trigger(_ context.Context, organizationID, projectID string) error {
	project := Project{OrganizationID: organizationID, ProjectID: projectID}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.running {
		return ErrReconcilerNotRunning
	}
	if r.pending[project] {
		return nil
	}
	r.pending[project] = true
	r.queue = append(r.queue, project)
	select {
	case r.trigger <- struct{}{}:
	default:
	}
	return nil
}

func (r *Reconciler) nextTriggeredProject() (Project, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.queue) == 0 {
		return Project{}, false
	}
	project := r.queue[0]
	r.queue = r.queue[1:]
	delete(r.pending, project)
	return project, true
}

func (r *Reconciler) ReconcileAll(ctx context.Context) error {
	projects, err := r.repository.ListBagDropProjects(ctx)
	if err != nil {
		return err
	}
	for _, project := range projects {
		r.mu.Lock()
		backoff := r.backoffs[project]
		r.mu.Unlock()
		if r.now().Before(backoff.next) {
			continue
		}
		if err := r.reconcileAndBackoff(ctx, project); errors.Is(err, ErrAuditUnavailable) {
			return err
		}
	}
	return nil
}

func (r *Reconciler) reconcileAndBackoff(ctx context.Context, project Project) error {
	err := r.ReconcileProject(ctx, project)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err == nil {
		delete(r.backoffs, project)
		return nil
	}
	backoff := r.backoffs[project]
	backoff.failures++
	delay := backoffDelay(r.interval, backoff.failures)
	var destinationError *AdapterError
	if errors.As(err, &destinationError) && destinationError.RetryAfter > delay {
		delay = destinationError.RetryAfter
	}
	backoff.next = r.now().Add(delay)
	r.backoffs[project] = backoff
	return err
}

func backoffDelay(interval time.Duration, failures int) time.Duration {
	delay := interval
	for range failures {
		if delay >= time.Hour/2 {
			return time.Hour
		}
		delay *= 2
	}
	if delay > time.Hour {
		return time.Hour
	}
	return delay
}

func (r *Reconciler) ReconcileProject(ctx context.Context, project Project) error {
	record, err := r.repository.GetBagDropConfig(ctx, project.OrganizationID, project.ProjectID)
	if err != nil || !record.Enabled {
		return err
	}
	adapter, ok := r.adapters[record.Adapter]
	if !ok {
		return fmt.Errorf("unsupported Bag Drop adapter %q", record.Adapter)
	}
	secret, err := r.sealer.Unseal(record.OrganizationID, record.ProjectID, record.SealedSecret)
	if err != nil {
		return err
	}
	destination := Destination{HCPPackerConfig: record.HCPPacker, ClientSecret: secret}
	associations, err := r.repository.ListBagDropAssociations(ctx, project.OrganizationID, project.ProjectID)
	if err != nil {
		return err
	}
	var run ReconcileRun
	var runErr error
	var failures []error
	for _, association := range associations {
		if association.State != AssociationActive {
			continue
		}
		bucket, err := r.repository.GetBagDropBucketSnapshot(
			ctx, project.OrganizationID, project.ProjectID, association.BucketName,
		)
		if err != nil {
			return err
		}
		if bucket == nil {
			continue
		}
		attemptedAt := r.now().UTC()
		if err := r.repository.MarkBagDropAssociationAttempt(
			ctx, project.OrganizationID, project.ProjectID, association.BucketName, attemptedAt,
		); err != nil {
			return err
		}
		if run == nil && runErr == nil {
			run, runErr = adapter.BeginReconcile(ctx, destination)
		}
		associationErr := runErr
		if associationErr == nil {
			associationErr = r.reconcileBucket(ctx, project, destination, run, *bucket)
		}
		if associationErr == nil {
			if err := r.repository.RecordBagDropAssociationSuccess(
				ctx, project.OrganizationID, project.ProjectID, association.BucketName, r.now().UTC(),
			); err != nil {
				return err
			}
			continue
		}
		summary := terseError(associationErr, secret)
		if errors.Is(associationErr, ErrAuditUnavailable) {
			summary = "audit unavailable; Bag Drop sync paused"
		}
		if err := r.repository.RecordBagDropAssociationFailure(
			ctx, project.OrganizationID, project.ProjectID, association.BucketName, summary, r.now().UTC(),
		); err != nil {
			return err
		}
		if errors.Is(associationErr, ErrAuditUnavailable) {
			return associationErr
		}
		failures = append(failures, associationErr)
	}
	return errors.Join(failures...)
}

func (r *Reconciler) reconcileBucket(
	ctx context.Context, project Project, destination Destination, run ReconcileRun, bucket BucketSnapshot,
) error {
	remote, exists, err := run.GetBucket(ctx, bucket.Name)
	if err != nil {
		return err
	}
	if !exists {
		if err := r.mutate(ctx, project, destination, "bagdrop.sync.bucket.create", "bucket", bucket.Name, "",
			func() error { return run.CreateBucket(ctx, bucket) }); err != nil {
			return err
		}
	} else if remote.Description != bucket.Description {
		if err := r.mutate(ctx, project, destination, "bagdrop.sync.bucket.update", "bucket", bucket.Name, "",
			func() error { return run.UpdateBucket(ctx, bucket) }); err != nil {
			return err
		}
	}
	for _, version := range bucket.Versions {
		exists, err := run.GetVersion(ctx, bucket.Name, version.Fingerprint)
		if err != nil {
			return err
		}
		if !exists {
			if err := r.mutate(ctx, project, destination, "bagdrop.sync.version.create", "version", version.Fingerprint, "",
				func() error { return run.CreateVersion(ctx, bucket.Name, version) }); err != nil {
				return err
			}
		}
		remoteBuilds, err := run.ListBuilds(ctx, bucket.Name, version.Fingerprint)
		if err != nil {
			return err
		}
		for _, build := range version.Builds {
			remoteBuild := findRemoteBuild(remoteBuilds, build.ComponentType)
			if remoteBuild != nil && remoteBuild.Status == "BUILD_DONE" {
				continue
			}
			if build.PackerRunUUID == "" {
				return fmt.Errorf("build %s has blank packer_run_uuid", build.ID)
			}
			if remoteBuild == nil {
				var buildID string
				if err := r.mutate(ctx, project, destination, "bagdrop.sync.build.create", "build",
					version.Fingerprint+"/"+build.ComponentType, "", func() error {
						var createErr error
						buildID, createErr = run.CreateBuild(ctx, bucket.Name, version.Fingerprint, build)
						return createErr
					}); err != nil {
					return err
				}
				if buildID == "" {
					listed, err := run.ListBuilds(ctx, bucket.Name, version.Fingerprint)
					if err != nil {
						return err
					}
					remoteBuild = findRemoteBuild(listed, build.ComponentType)
					if remoteBuild == nil {
						return errors.New("created destination build was not returned by ListBuilds")
					}
					buildID = remoteBuild.ID
				}
				remoteBuild = &RemoteBuild{ID: buildID, ComponentType: build.ComponentType}
			}
			if err := r.mutate(ctx, project, destination, "bagdrop.sync.build.update", "build",
				version.Fingerprint+"/"+build.ComponentType, "",
				func() error { return run.UpdateBuild(ctx, bucket.Name, version.Fingerprint, remoteBuild.ID, build) }); err != nil {
				return err
			}
		}
	}
	// Channel assignments can only reference complete local versions. Because
	// the snapshot is transactional and versions/builds converge above, every
	// assignment target exists remotely before its pointer is set here.
	return r.reconcileChannels(ctx, project, destination, run, bucket)
}

func (r *Reconciler) reconcileChannels(
	ctx context.Context, project Project, destination Destination, run ReconcileRun, bucket BucketSnapshot,
) error {
	remoteChannels, err := run.ListChannels(ctx, bucket.Name)
	if err != nil {
		return err
	}
	ordinaryRemote := make(map[string]RemoteChannel, len(remoteChannels))
	for _, channel := range remoteChannels {
		if !channel.Managed {
			ordinaryRemote[channel.Name] = channel
		}
	}
	for _, channel := range bucket.Channels {
		remoteChannel, exists := ordinaryRemote[channel.Name]
		if !exists {
			if err := r.mutate(ctx, project, destination, "bagdrop.sync.channel.create", "channel", channel.Name, "",
				func() error { return run.CreateChannel(ctx, bucket.Name, channel.Name) }); err != nil {
				return err
			}
			// Re-observe after create so a 409/code-6 adoption converges the
			// existing pointer rather than assuming a newly empty channel.
			listed, err := run.ListChannels(ctx, bucket.Name)
			if err != nil {
				return err
			}
			remoteChannel = RemoteChannel{Name: channel.Name}
			found := false
			for _, candidate := range listed {
				if candidate.Name == channel.Name && !candidate.Managed {
					remoteChannel = candidate
					found = true
					break
				}
			}
			if !found {
				return errors.New("created destination channel was not returned by ListChannels")
			}
		}
		if sameFingerprint(channel.AssignedVersionFingerprint, remoteChannel.AssignedVersionFingerprint) {
			continue
		}
		detail := "clear assignment"
		if channel.AssignedVersionFingerprint != nil {
			detail = "assign version fingerprint " + *channel.AssignedVersionFingerprint
		}
		if err := r.mutate(ctx, project, destination, "bagdrop.sync.channel.update", "channel", channel.Name, detail,
			func() error {
				return run.UpdateChannelAssignment(
					ctx, bucket.Name, channel.Name, channel.AssignedVersionFingerprint,
				)
			}); err != nil {
			return err
		}
	}
	return nil
}

func sameFingerprint(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func findRemoteBuild(builds []RemoteBuild, componentType string) *RemoteBuild {
	for i := range builds {
		if builds[i].ComponentType == componentType {
			return &builds[i]
		}
	}
	return nil
}

func (r *Reconciler) mutate(
	ctx context.Context, project Project, destination Destination,
	operation identity.AuditOperation, targetType, targetID, detail string, mutation func() error,
) error {
	event := audit.SystemEvent{
		Operation: operation, TargetType: targetType, TargetID: targetID, Detail: detail,
		Scope:          identity.AuditScopeProject,
		OrganizationID: project.OrganizationID, ProjectID: project.ProjectID,
		DestinationOrganizationID: destination.OrganizationID,
		DestinationProjectID:      destination.ProjectID,
	}
	correlationID, err := r.audit.Request(event)
	if err != nil {
		return fmt.Errorf("%w: request event", ErrAuditUnavailable)
	}
	mutationErr := mutation()
	outcome, reason := identity.AuditOutcomeSuccess, ""
	if mutationErr != nil {
		outcome = identity.AuditOutcomeFailure
		reason = terseError(mutationErr, destination.ClientSecret)
	}
	if err := r.audit.Response(correlationID, event, outcome, reason); err != nil {
		return fmt.Errorf("%w: response event", ErrAuditUnavailable)
	}
	return mutationErr
}

func terseError(err error, secrets ...string) string {
	if err == nil {
		return ""
	}
	summary := strings.TrimSpace(err.Error())
	for _, secret := range secrets {
		if secret != "" {
			summary = strings.ReplaceAll(summary, secret, "[redacted]")
		}
	}
	if len(summary) > 512 {
		summary = summary[:512]
	}
	return summary
}
