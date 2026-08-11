package registry

import (
	"fmt"
	"time"
)

// TemplateType is the kind of Packer template a version was built from.
//
// It must be known at creation and can never change: Packer refuses to add a
// build to a version created from a different template type.
type TemplateType string

const (
	TemplateHCL2 TemplateType = "HCL2"
	TemplateJSON TemplateType = "JSON"
)

func (t TemplateType) valid() bool {
	return t == TemplateHCL2 || t == TemplateJSON
}

// BuildStatus is the lifecycle state of one component build.
type BuildStatus string

const (
	BuildPending   BuildStatus = "pending"
	BuildRunning   BuildStatus = "running"
	BuildDone      BuildStatus = "done"
	BuildFailed    BuildStatus = "failed"
	BuildCancelled BuildStatus = "cancelled"
)

func (s BuildStatus) succeeded() bool { return s == BuildDone }

// Build is one component's contribution to a version.
type Build struct {
	ID            ID
	ComponentType string
	Status        BuildStatus
	Platform      string
	MetadataSeen  bool
}

// AncestryStatus describes whether a version still follows its recorded parents.
type AncestryStatus string

const (
	AncestryUndetermined AncestryStatus = "undetermined"
	AncestryUpToDate     AncestryStatus = "up_to_date"
	AncestryOutOfDate    AncestryStatus = "out_of_date"
)

// VersionParents is the ancestry summary for a version that names a parent.
type VersionParents struct {
	Status AncestryStatus
}

// RevokedAncestor identifies the ancestor a revocation was inherited from.
//
// The identity is denormalized at revocation time: bucket names and version
// names are fixed by then, and carrying them keeps rendering join-free.
type RevokedAncestor struct {
	VersionID   ID
	BucketName  string
	Fingerprint string
	VersionName string
}

// Revocation is a version's revocation state.
//
// RevokeAt is the effect time, not the record time: a future value is a
// scheduled revocation, and readers derive whether it has taken effect rather
// than any job flipping state. InheritedFrom is nil for a manual revocation;
// the wire's manual/inherited type is derived from it, never stored.
type Revocation struct {
	RevokeAt      time.Time
	Message       string
	Author        string
	InheritedFrom *RevokedAncestor
}

// Version is a set of builds sharing a fingerprint.
//
// Completion is a bool and a sequence number here, deliberately. The
// 2023-01-01 wire model collapses both into a single "name" string — "v0" while
// incomplete, "v<n>" once complete — which is a lossy migration of the two
// well-designed fields the 2021-04-30 model carried. The compatibility adapters
// re-derive that string; the domain never sees it.
type Version struct {
	ID             ID
	BucketName     string
	Fingerprint    string
	TemplateType   TemplateType
	AuthorID       string
	HasDescendants bool
	Parents        *VersionParents
	Builds         []Build
	CreatedAt      time.Time
	UpdatedAt      time.Time

	// complete and sequence are unexported so the invariant binding them
	// cannot be sidestepped: a sequence exists if and only if the version is
	// complete. Only MarkComplete sets either.
	complete bool
	sequence int

	// revocation is unexported for the same reason: only Revoke and Restore
	// change it, and a version cannot be revoked twice without a restore.
	revocation *Revocation
}

// NewVersion creates an incomplete version.
func NewVersion(id ID, bucket, fingerprint string, tt TemplateType, at time.Time) (*Version, error) {
	switch {
	case bucket == "":
		return nil, fmt.Errorf("%w: bucket name is required", ErrInvalid)
	case fingerprint == "":
		return nil, fmt.Errorf("%w: fingerprint is required", ErrInvalid)
	case !tt.valid():
		// Never allow an unset template type: Packer treats it as an internal
		// bug and refuses to proceed.
		return nil, fmt.Errorf("%w: template type %q is not known", ErrInvalid, tt)
	}

	return &Version{
		ID:           id,
		BucketName:   bucket,
		Fingerprint:  fingerprint,
		TemplateType: tt,
		CreatedAt:    at,
		UpdatedAt:    at,
	}, nil
}

// Complete reports whether every build succeeded and reported its metadata.
func (v *Version) Complete() bool { return v.complete }

// Sequence is the human-readable version number. It exists only once complete.
func (v *Version) Sequence() (int, bool) {
	if !v.complete {
		return 0, false
	}
	return v.sequence, true
}

// Revocation is the version's revocation state, nil when never revoked.
func (v *Version) Revocation() *Revocation { return v.revocation }

// Revoke records a revocation, manual or inherited.
//
// An already-revoked version is refused rather than overwritten: a manual
// revocation must not be silently replaced by an inherited one arriving later.
// A caller that means to replace a revocation must restore, then revoke again.
func (v *Version) Revoke(rev Revocation, at time.Time) error {
	switch {
	case v.revocation != nil:
		return fmt.Errorf("%w: version %s is already revoked", ErrConflict, v.ID)
	case rev.RevokeAt.IsZero():
		return fmt.Errorf("%w: a revocation needs an effect time", ErrInvalid)
	case rev.Author == "":
		return fmt.Errorf("%w: a revocation needs an author", ErrInvalid)
	}
	if a := rev.InheritedFrom; a != nil {
		if a.VersionID == "" || a.BucketName == "" || a.Fingerprint == "" || a.VersionName == "" {
			return fmt.Errorf("%w: an inherited revocation needs its ancestor's full identity", ErrInvalid)
		}
	}

	v.revocation = &rev
	v.UpdatedAt = at
	return nil
}

// Restore clears a version's revocation state.
func (v *Version) Restore(at time.Time) error {
	if v.revocation == nil {
		return fmt.Errorf("%w: version %s is not revoked", ErrConflict, v.ID)
	}
	v.revocation = nil
	v.UpdatedAt = at
	return nil
}

// EnsureTemplateType rejects a run whose template type differs from the one the
// version was created with.
func (v *Version) EnsureTemplateType(tt TemplateType) error {
	if v.TemplateType != tt {
		return fmt.Errorf(
			"%w: version was created from a %s template, cannot add a %s build",
			ErrConflict, v.TemplateType, tt,
		)
	}
	return nil
}

// ReadyToComplete reports whether every build has succeeded and reported
// metadata. A version with no builds is never ready.
func (v *Version) ReadyToComplete() bool {
	if len(v.Builds) == 0 {
		return false
	}
	for _, b := range v.Builds {
		if !b.Status.succeeded() || !b.MetadataSeen {
			return false
		}
	}
	return true
}

// MarkComplete assigns the version its sequence number.
//
// sequence is allocated per bucket and monotonic, and is assigned at completion
// rather than creation — an incomplete version has no number.
func (v *Version) MarkComplete(sequence int, at time.Time) error {
	if v.complete {
		return fmt.Errorf("%w: version %s is already complete", ErrConflict, v.ID)
	}
	if sequence < 1 {
		return fmt.Errorf("%w: sequence must be positive, got %d", ErrInvalid, sequence)
	}
	if !v.ReadyToComplete() {
		return fmt.Errorf(
			"%w: version %s has builds that have not succeeded and reported metadata",
			ErrConflict, v.ID,
		)
	}

	v.complete = true
	v.sequence = sequence
	v.UpdatedAt = at
	return nil
}

// RestoreVersion rebuilds a Version from persisted state, bypassing the
// creation invariants. For the store layer only.
func RestoreVersion(v Version, complete bool, sequence int, revocation *Revocation) (*Version, error) {
	if !complete && sequence != 0 {
		return nil, fmt.Errorf("%w: incomplete version %s carries sequence %d", ErrInvalid, v.ID, sequence)
	}
	if complete && sequence < 1 {
		return nil, fmt.Errorf("%w: complete version %s has no sequence", ErrInvalid, v.ID)
	}
	if revocation != nil && revocation.RevokeAt.IsZero() {
		return nil, fmt.Errorf("%w: revoked version %s has no effect time", ErrInvalid, v.ID)
	}

	v.complete = complete
	v.sequence = sequence
	v.revocation = revocation
	return &v, nil
}

// AssignableToChannel reports whether a channel may point at this version.
//
// Only complete versions may be assigned: the 2021-04-30 spec states a complete
// iteration "is considered ready to use, and can have channels assigned to it".
func (v *Version) AssignableToChannel() error {
	if !v.complete {
		return fmt.Errorf("%w: version %s is incomplete", ErrConflict, v.ID)
	}
	return nil
}
