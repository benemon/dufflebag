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

type surfacedSyncError struct {
	messages []string
}

func (e *surfacedSyncError) Error() string { return strings.Join(e.messages, "; ") }

type ReconcileRepository interface {
	ListBagDropProjects(context.Context) ([]Project, error)
	GetBagDropConfig(context.Context, string, string) (*Record, error)
	ListBagDropAssociations(context.Context, string, string) ([]Association, error)
	GetBagDropBucketSnapshot(context.Context, string, string, string) (*BucketSnapshot, error)
	MarkBagDropAssociationAttempt(context.Context, string, string, string, time.Time) error
	RecordBagDropAssociationSuccess(context.Context, string, string, string, time.Time) error
	RecordBagDropAssociationFailure(context.Context, string, string, string, string, time.Time) error
	DeleteBagDropAssociation(context.Context, string, string, string) error
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

	mu          sync.Mutex
	running     bool
	pending     map[Project]bool
	queue       []Project
	backoffs    map[Project]projectBackoff
	reconciling map[Project]bool
	lastPass    map[Project]time.Time
	nextTimer   time.Time
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

type ReconcilerStatus struct {
	Reconciling     bool
	NextPass        *time.Time
	LastPass        *time.Time
	Interval        time.Duration
	BackoffFailures int
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
		reconciling: make(map[Project]bool), lastPass: make(map[Project]time.Time),
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
	r.mu.Lock()
	r.nextTimer = r.now()
	r.mu.Unlock()
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
			nextTimer := r.now().Add(r.interval)
			timer.Reset(r.interval)
			r.mu.Lock()
			r.nextTimer = nextTimer
			r.mu.Unlock()
		case <-r.trigger:
			for {
				project, ok := r.nextTriggeredProject()
				if !ok {
					break
				}
				if paused {
					continue
				}
				if err := r.reconcilePass(ctx, project); errors.Is(err, ErrAuditUnavailable) {
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

func (r *Reconciler) ReconcileStatus(organizationID, projectID string) ReconcilerStatus {
	project := Project{OrganizationID: organizationID, ProjectID: projectID}
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()

	status := ReconcilerStatus{
		Reconciling: r.reconciling[project], Interval: r.interval,
	}
	backoff := r.backoffs[project]
	status.BackoffFailures = backoff.failures
	if now.Before(backoff.next) {
		next := backoff.next
		status.NextPass = &next
	} else if !r.nextTimer.IsZero() {
		next := r.nextTimer
		status.NextPass = &next
	}
	if last, ok := r.lastPass[project]; ok {
		status.LastPass = &last
	}
	return status
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
		if err := r.reconcilePass(ctx, project); errors.Is(err, ErrAuditUnavailable) {
			return err
		}
	}
	return nil
}

func (r *Reconciler) reconcilePass(ctx context.Context, project Project) (err error) {
	r.mu.Lock()
	r.reconciling[project] = true
	r.mu.Unlock()
	defer func() {
		completedAt := r.now().UTC()
		r.mu.Lock()
		delete(r.reconciling, project)
		r.lastPass[project] = completedAt
		r.mu.Unlock()
	}()
	return r.reconcileAndBackoff(ctx, project)
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

func orderAssociationsByAncestry(
	associations []Association, snapshots map[string]*BucketSnapshot,
) []Association {
	ordered := make([]Association, 0, len(associations))
	if len(associations) < 2 {
		return append(ordered, associations...)
	}

	artifacts := make(map[string][]int)
	for associationIndex, association := range associations {
		bucket := snapshots[association.BucketName]
		if bucket == nil {
			continue
		}
		for _, version := range bucket.Versions {
			for _, build := range version.Builds {
				for _, artifact := range build.Artifacts {
					artifacts[artifact.ExternalIdentifier] = append(
						artifacts[artifact.ExternalIdentifier], associationIndex,
					)
				}
			}
		}
	}

	edges := make([][]bool, len(associations))
	for i := range edges {
		edges[i] = make([]bool, len(associations))
	}
	for childIndex, association := range associations {
		bucket := snapshots[association.BucketName]
		if bucket == nil {
			continue
		}
		for _, version := range bucket.Versions {
			for _, build := range version.Builds {
				for _, parentIndex := range artifacts[build.SourceExternalIdentifier] {
					if parentIndex != childIndex {
						edges[parentIndex][childIndex] = true
					}
				}
			}
		}
	}

	// Ignore ordering within cycles. Members of a cycle have the same external
	// reachability, so the stable selection below emits them in input order.
	reachable := make([][]bool, len(edges))
	for i := range edges {
		reachable[i] = append([]bool(nil), edges[i]...)
	}
	for through := range reachable {
		for from := range reachable {
			if !reachable[from][through] {
				continue
			}
			for to := range reachable {
				reachable[from][to] = reachable[from][to] || reachable[through][to]
			}
		}
	}
	indegree := make([]int, len(associations))
	for from := range reachable {
		for to := range reachable[from] {
			if reachable[from][to] && !reachable[to][from] {
				indegree[to]++
			}
		}
	}

	emitted := make([]bool, len(associations))
	for range associations {
		next := 0
		for emitted[next] || indegree[next] != 0 {
			next++
		}
		emitted[next] = true
		ordered = append(ordered, associations[next])
		for child := range reachable[next] {
			if reachable[next][child] && !reachable[child][next] {
				indegree[child]--
			}
		}
	}
	return ordered
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
	destination := destinationForRecord(record, secret)
	associations, err := r.repository.ListBagDropAssociations(ctx, project.OrganizationID, project.ProjectID)
	if err != nil {
		return err
	}
	snapshots := make(map[string]*BucketSnapshot, len(associations))
	for _, association := range associations {
		if association.State != AssociationActive {
			continue
		}
		snapshots[association.BucketName], err = r.repository.GetBagDropBucketSnapshot(
			ctx, project.OrganizationID, project.ProjectID, association.BucketName,
		)
		if err != nil {
			return err
		}
	}
	associations = orderAssociationsByAncestry(associations, snapshots)
	var run ReconcileRun
	var runErr error
	var failures []error
	for _, association := range associations {
		bucket := snapshots[association.BucketName]
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
			switch {
			case association.State == AssociationPendingRemoval:
				associationErr = r.deleteAssociatedBucket(ctx, project, destination, run, association, "unassociate")
			case bucket == nil:
				associationErr = r.deleteAssociatedBucket(ctx, project, destination, run, association, "local_delete")
			default:
				associationErr = r.reconcileBucket(ctx, project, destination, run, *bucket)
			}
		}
		if associationErr == nil {
			if association.State == AssociationPendingRemoval || bucket == nil {
				continue
			}
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
		var surfaced *surfacedSyncError
		if errors.As(associationErr, &surfaced) {
			continue
		}
		if errors.Is(associationErr, ErrAuditUnavailable) {
			return associationErr
		}
		failures = append(failures, associationErr)
	}
	return errors.Join(failures...)
}

func (r *Reconciler) deleteAssociatedBucket(
	ctx context.Context, project Project, destination Destination, run ReconcileRun,
	association Association, reason string,
) error {
	if err := r.mutate(ctx, project, destination, "bagdrop.sync.bucket.delete", "bucket",
		association.BucketName, reason, func() error {
			return run.DeleteBucket(ctx, association.BucketName)
		}); err != nil {
		return err
	}
	return r.repository.DeleteBagDropAssociation(
		ctx, project.OrganizationID, project.ProjectID, association.BucketName,
	)
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
		remote = &RemoteBucket{}
	} else if remote.Description != bucket.Description {
		if err := r.mutate(ctx, project, destination, "bagdrop.sync.bucket.update", "bucket", bucket.Name, "",
			func() error { return run.UpdateBucket(ctx, bucket) }); err != nil {
			return err
		}
	}
	remoteBuildsByVersion := make(map[string][]RemoteBuild, len(bucket.Versions))
	var surfaced []string
	for _, version := range bucket.Versions {
		remoteVersion, exists, err := run.GetVersion(ctx, bucket.Name, version.Fingerprint)
		if err != nil {
			return err
		}
		if !exists {
			if err := r.mutate(ctx, project, destination, "bagdrop.sync.version.create", "version", version.Fingerprint, "",
				func() error { return run.CreateVersion(ctx, bucket.Name, version) }); err != nil {
				return err
			}
			remoteVersion, exists, err = run.GetVersion(ctx, bucket.Name, version.Fingerprint)
			if err != nil {
				return err
			}
			if !exists {
				return errors.New("created destination version was not returned by GetVersion")
			}
		}
		if version.RevokeAt == nil && remoteVersion.RevokeAt != nil {
			if err := r.mutate(ctx, project, destination, "bagdrop.sync.version.restore", "version",
				version.Fingerprint, "", func() error {
					return run.RestoreVersion(ctx, bucket.Name, version.Fingerprint)
				}); err != nil {
				return err
			}
			remoteVersion.RevokeAt = nil
		}
		remoteBuilds, err := run.ListBuilds(ctx, bucket.Name, version.Fingerprint)
		if err != nil {
			return err
		}
		remoteBuildsByVersion[version.Fingerprint] = remoteBuilds
		remoteBuildsByComponent := make(map[string]RemoteBuild, len(remoteBuilds)+len(version.Builds))
		for _, remoteBuild := range remoteBuilds {
			if _, exists := remoteBuildsByComponent[remoteBuild.ComponentType]; !exists {
				remoteBuildsByComponent[remoteBuild.ComponentType] = remoteBuild
			}
		}
		for _, build := range version.Builds {
			if _, exists := remoteBuildsByComponent[build.ComponentType]; exists {
				continue
			}
			if build.PackerRunUUID == "" {
				return fmt.Errorf("build %s has blank packer_run_uuid", build.ID)
			}
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
				for _, remoteBuild := range listed {
					if _, exists := remoteBuildsByComponent[remoteBuild.ComponentType]; !exists {
						remoteBuildsByComponent[remoteBuild.ComponentType] = remoteBuild
					}
				}
				remoteBuild, exists := remoteBuildsByComponent[build.ComponentType]
				if !exists {
					return errors.New("created destination build was not returned by ListBuilds")
				}
				remoteBuildsByComponent[build.ComponentType] = RemoteBuild{
					ID: remoteBuild.ID, ComponentType: build.ComponentType, Status: "BUILD_PENDING",
				}
				continue
			}
			remoteBuildsByComponent[build.ComponentType] = RemoteBuild{
				ID: buildID, ComponentType: build.ComponentType, Status: "BUILD_PENDING",
			}
		}
		for _, build := range version.Builds {
			remoteBuild := remoteBuildsByComponent[build.ComponentType]
			if remoteBuild.Status != "BUILD_DONE" && len(build.Sboms) == 0 {
				if build.PackerRunUUID == "" {
					return fmt.Errorf("build %s has blank packer_run_uuid", build.ID)
				}
				if err := r.mutate(ctx, project, destination, "bagdrop.sync.build.update", "build",
					version.Fingerprint+"/"+build.ComponentType, "",
					func() error { return run.UpdateBuild(ctx, bucket.Name, version.Fingerprint, remoteBuild.ID, build) }); err != nil {
					return err
				}
				remoteBuild.Status = "BUILD_DONE"
			}
			remoteSboms, err := run.ListSboms(ctx, bucket.Name, version.Fingerprint, remoteBuild.ID)
			if err != nil {
				return err
			}
			remoteSbomNames := make(map[string]bool, len(remoteSboms))
			for _, sbom := range remoteSboms {
				remoteSbomNames[sbom.Name] = true
			}
			localSbomNames := make(map[string]bool, len(build.Sboms))
			if remoteBuild.Status != "BUILD_DONE" {
				if build.PackerRunUUID == "" {
					return fmt.Errorf("build %s has blank packer_run_uuid", build.ID)
				}
				if len(build.Sboms) > 0 && remoteBuild.Status != "BUILD_RUNNING" {
					if err := r.mutate(ctx, project, destination, "bagdrop.sync.build.update", "build",
						version.Fingerprint+"/"+build.ComponentType, "status BUILD_RUNNING", func() error {
							return run.UpdateBuildRunning(ctx, bucket.Name, version.Fingerprint, remoteBuild.ID)
						}); err != nil {
						return err
					}
					remoteBuild.Status = "BUILD_RUNNING"
				}
			}
			for _, sbom := range build.Sboms {
				localSbomNames[sbom.Name] = true
				if remoteSbomNames[sbom.Name] {
					continue
				}
				target := version.Fingerprint + "/" + build.ComponentType + "/" + sbom.Name
				if remoteBuild.Status == "BUILD_DONE" {
					surfaced = append(surfaced, "SBOM "+target+" cannot be uploaded to a completed destination build")
					continue
				}
				err := r.mutate(ctx, project, destination, "bagdrop.sync.sbom.upload", "sbom", target, "",
					func() error {
						return run.UploadSbom(ctx, bucket.Name, version.Fingerprint, remoteBuild.ID, sbom)
					})
				if sbomSizeRefusal(err) {
					surfaced = append(surfaced, "SBOM "+target+" skipped after destination size refusal: "+err.Error())
					continue
				}
				if err != nil {
					return err
				}
				remoteSbomNames[sbom.Name] = true
			}
			if remoteBuild.Status != "BUILD_DONE" {
				if err := r.mutate(ctx, project, destination, "bagdrop.sync.build.update", "build",
					version.Fingerprint+"/"+build.ComponentType, "",
					func() error { return run.UpdateBuild(ctx, bucket.Name, version.Fingerprint, remoteBuild.ID, build) }); err != nil {
					return err
				}
				remoteBuild.Status = "BUILD_DONE"
			}
			for _, sbom := range remoteSboms {
				if !localSbomNames[sbom.Name] {
					surfaced = append(surfaced, "destination SBOM "+version.Fingerprint+"/"+
						build.ComponentType+"/"+sbom.Name+" has no local source and cannot be deleted")
				}
			}
		}
		if version.RevokeAt != nil && remoteVersion.RevokeAt == nil {
			detail := "revoke_at " + version.RevokeAt.UTC().Format(time.RFC3339Nano)
			if err := r.mutate(ctx, project, destination, "bagdrop.sync.version.revoke", "version",
				version.Fingerprint, detail, func() error {
					return run.RevokeVersion(
						ctx, bucket.Name, version.Fingerprint, *version.RevokeAt, version.RevocationMessage,
					)
				}); err != nil {
				return err
			}
		}
	}
	// Channel assignments can only reference complete local versions. Because
	// the snapshot is transactional and versions/builds converge above, every
	// assignment target exists remotely before its pointer is set here.
	if err := r.reconcileChannels(ctx, project, destination, run, bucket); err != nil {
		return err
	}
	localVersions := make(map[string]bool, len(bucket.Versions))
	for _, version := range bucket.Versions {
		localVersions[version.Fingerprint] = true
	}
	for _, version := range remote.Versions {
		if _, exists := localVersions[version.Fingerprint]; exists {
			continue
		}
		if err := r.mutate(ctx, project, destination, "bagdrop.sync.version.delete", "version",
			version.Fingerprint, "drift", func() error {
				return run.DeleteVersion(ctx, bucket.Name, version.Fingerprint)
			}); err != nil {
			return err
		}
	}
	for _, version := range bucket.Versions {
		localBuilds := make(map[string]bool, len(version.Builds))
		for _, build := range version.Builds {
			localBuilds[build.ComponentType] = true
		}
		for _, build := range remoteBuildsByVersion[version.Fingerprint] {
			if localBuilds[build.ComponentType] {
				continue
			}
			if err := r.mutate(ctx, project, destination, "bagdrop.sync.build.delete", "build",
				version.Fingerprint+"/"+build.ComponentType, "drift", func() error {
					return run.DeleteBuild(ctx, bucket.Name, version.Fingerprint, build.ID)
				}); err != nil {
				return err
			}
		}
	}
	if len(surfaced) != 0 {
		return &surfacedSyncError{messages: surfaced}
	}
	return nil
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
	localChannels := make(map[string]bool, len(bucket.Channels))
	for _, channel := range bucket.Channels {
		localChannels[channel.Name] = true
		remoteChannel, exists := ordinaryRemote[channel.Name]
		if !exists {
			if err := r.mutate(ctx, project, destination, "bagdrop.sync.channel.create", "channel", channel.Name, "",
				func() error { return run.CreateChannel(ctx, bucket.Name, channel) }); err != nil {
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
		if remoteChannel.Restricted != channel.Restricted {
			detail := "restricted false"
			if channel.Restricted {
				detail = "restricted true"
			}
			if err := r.mutate(ctx, project, destination, "bagdrop.sync.channel.update", "channel", channel.Name, detail,
				func() error {
					return run.UpdateChannelRestriction(ctx, bucket.Name, channel.Name, channel.Restricted)
				}); err != nil {
				return err
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
	for name := range ordinaryRemote {
		if localChannels[name] {
			continue
		}
		if err := r.mutate(ctx, project, destination, "bagdrop.sync.channel.delete", "channel", name, "drift",
			func() error { return run.DeleteChannel(ctx, bucket.Name, name) }); err != nil {
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
