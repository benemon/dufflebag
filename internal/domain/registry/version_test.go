package registry

import (
	"errors"
	"testing"
	"time"
)

var epoch = time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)

func doneBuild(t *testing.T, component string) Build {
	t.Helper()
	return Build{ID: NewID(epoch), ComponentType: component, Status: BuildDone, MetadataSeen: true}
}

func newVersion(t *testing.T) *Version {
	t.Helper()
	v, err := NewVersion(NewID(epoch), "bucket", "fp-1", TemplateHCL2, epoch)
	if err != nil {
		t.Fatalf("NewVersion: %v", err)
	}
	return v
}

func TestNewVersionRejectsBadInput(t *testing.T) {
	cases := []struct {
		name         string
		bucket, fp   string
		templateType TemplateType
	}{
		{"empty bucket", "", "fp-1", TemplateHCL2},
		{"empty fingerprint", "bucket", "", TemplateHCL2},
		{"unset template type", "bucket", "fp-1", ""},
		{"unknown template type", "bucket", "fp-1", "TEMPLATE_TYPE_UNSET"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewVersion(NewID(epoch), c.bucket, c.fp, c.templateType, epoch); !errors.Is(err, ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
		})
	}
}

func TestNewVersionIsIncompleteAndHasNoSequence(t *testing.T) {
	v := newVersion(t)
	if v.Complete() {
		t.Fatal("a new version must be incomplete")
	}
	if _, ok := v.Sequence(); ok {
		t.Fatal("an incomplete version must not expose a sequence")
	}
}

func TestReadyToComplete(t *testing.T) {
	cases := []struct {
		name   string
		builds []Build
		want   bool
	}{
		{"no builds", nil, false},
		{"one done", []Build{doneBuild(t, "docker")}, true},
		{
			"one done one running",
			[]Build{doneBuild(t, "docker"), {ID: NewID(epoch), Status: BuildRunning, MetadataSeen: false}},
			false,
		},
		{
			"failed build",
			[]Build{{ID: NewID(epoch), Status: BuildFailed, MetadataSeen: true}},
			false,
		},
		{
			"succeeded but no metadata",
			[]Build{{ID: NewID(epoch), Status: BuildDone, MetadataSeen: false}},
			false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := newVersion(t)
			v.Builds = c.builds
			if got := v.ReadyToComplete(); got != c.want {
				t.Fatalf("ReadyToComplete() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestMarkComplete(t *testing.T) {
	v := newVersion(t)
	v.Builds = []Build{doneBuild(t, "docker")}

	later := epoch.Add(time.Minute)
	if err := v.MarkComplete(1, later); err != nil {
		t.Fatalf("MarkComplete: %v", err)
	}
	if !v.Complete() {
		t.Fatal("version should be complete")
	}
	seq, ok := v.Sequence()
	if !ok || seq != 1 {
		t.Fatalf("Sequence() = %d, %v; want 1, true", seq, ok)
	}
	if !v.UpdatedAt.Equal(later) {
		t.Fatalf("UpdatedAt = %v, want %v", v.UpdatedAt, later)
	}
}

func TestMarkCompleteRejections(t *testing.T) {
	t.Run("builds not finished", func(t *testing.T) {
		v := newVersion(t)
		v.Builds = []Build{{ID: NewID(epoch), Status: BuildRunning}}
		if err := v.MarkComplete(1, epoch); !errors.Is(err, ErrConflict) {
			t.Fatalf("want ErrConflict, got %v", err)
		}
	})

	t.Run("no builds", func(t *testing.T) {
		v := newVersion(t)
		if err := v.MarkComplete(1, epoch); !errors.Is(err, ErrConflict) {
			t.Fatalf("want ErrConflict, got %v", err)
		}
	})

	t.Run("non-positive sequence", func(t *testing.T) {
		v := newVersion(t)
		v.Builds = []Build{doneBuild(t, "docker")}
		if err := v.MarkComplete(0, epoch); !errors.Is(err, ErrInvalid) {
			t.Fatalf("want ErrInvalid, got %v", err)
		}
	})

	t.Run("already complete", func(t *testing.T) {
		v := newVersion(t)
		v.Builds = []Build{doneBuild(t, "docker")}
		if err := v.MarkComplete(1, epoch); err != nil {
			t.Fatalf("first MarkComplete: %v", err)
		}
		if err := v.MarkComplete(2, epoch); !errors.Is(err, ErrConflict) {
			t.Fatalf("want ErrConflict on re-completion, got %v", err)
		}
		if seq, _ := v.Sequence(); seq != 1 {
			t.Fatalf("sequence changed to %d after a rejected re-completion", seq)
		}
	})
}

func TestEnsureTemplateType(t *testing.T) {
	v := newVersion(t)
	if err := v.EnsureTemplateType(TemplateHCL2); err != nil {
		t.Fatalf("matching template type rejected: %v", err)
	}
	if err := v.EnsureTemplateType(TemplateJSON); !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict on template type change, got %v", err)
	}
}

func TestAssignableToChannelRequiresCompletion(t *testing.T) {
	v := newVersion(t)
	if err := v.AssignableToChannel(); !errors.Is(err, ErrConflict) {
		t.Fatalf("incomplete version must not be assignable, got %v", err)
	}

	v.Builds = []Build{doneBuild(t, "docker")}
	if err := v.MarkComplete(1, epoch); err != nil {
		t.Fatalf("MarkComplete: %v", err)
	}
	if err := v.AssignableToChannel(); err != nil {
		t.Fatalf("complete version must be assignable: %v", err)
	}
}

func TestRestoreVersionRejectsInconsistentState(t *testing.T) {
	base := Version{ID: NewID(epoch), BucketName: "b", Fingerprint: "fp", TemplateType: TemplateHCL2}

	if _, err := RestoreVersion(base, false, 3, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("incomplete version with a sequence must be rejected, got %v", err)
	}
	if _, err := RestoreVersion(base, true, 0, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("complete version without a sequence must be rejected, got %v", err)
	}
	if _, err := RestoreVersion(base, true, 7, &Revocation{Author: "ops"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("revocation without an effect time must be rejected, got %v", err)
	}

	v, err := RestoreVersion(base, true, 7, &Revocation{RevokeAt: epoch, Author: "ops"})
	if err != nil {
		t.Fatalf("RestoreVersion: %v", err)
	}
	if seq, ok := v.Sequence(); !ok || seq != 7 {
		t.Fatalf("Sequence() = %d, %v; want 7, true", seq, ok)
	}
	if rev := v.Revocation(); rev == nil || !rev.RevokeAt.Equal(epoch) {
		t.Fatalf("Revocation() = %+v; want the restored state", rev)
	}
}

func TestRevokeRecordsStateAndRefusesASecondRevocation(t *testing.T) {
	v := newVersion(t)
	later := epoch.Add(time.Hour)
	rev := Revocation{RevokeAt: later, Message: "CVE-2026-0001", Author: "ops"}
	if err := v.Revoke(rev, epoch); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	got := v.Revocation()
	if got == nil || !got.RevokeAt.Equal(later) || got.Message != "CVE-2026-0001" ||
		got.Author != "ops" || got.InheritedFrom != nil {
		t.Fatalf("Revocation() = %+v; want the recorded manual revocation", got)
	}
	if !v.UpdatedAt.Equal(epoch) {
		t.Fatalf("UpdatedAt = %v; want the revocation record time", v.UpdatedAt)
	}

	// A second revocation must not overwrite the first — inherited arriving
	// after manual included.
	inherited := Revocation{
		RevokeAt: later, Author: "ops",
		InheritedFrom: &RevokedAncestor{
			VersionID: NewID(epoch), BucketName: "base", Fingerprint: "fp-0", VersionName: "v1",
		},
	}
	if err := v.Revoke(inherited, epoch); !errors.Is(err, ErrConflict) {
		t.Fatalf("second Revoke must be ErrConflict, got %v", err)
	}
	if v.Revocation().InheritedFrom != nil {
		t.Fatal("the refused revocation must not have replaced the recorded one")
	}
}

func TestRevokeRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		rev  Revocation
	}{
		{"no effect time", Revocation{Author: "ops"}},
		{"no author", Revocation{RevokeAt: epoch}},
		{"partial ancestor", Revocation{
			RevokeAt: epoch, Author: "ops",
			InheritedFrom: &RevokedAncestor{BucketName: "base"},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := newVersion(t)
			if err := v.Revoke(c.rev, epoch); !errors.Is(err, ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
			if v.Revocation() != nil {
				t.Fatal("a refused revocation must leave the version unrevoked")
			}
		})
	}
}
