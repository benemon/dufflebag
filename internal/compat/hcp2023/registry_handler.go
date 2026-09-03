package hcp2023

import (
	"errors"
	"net/http"

	"github.com/benemon/dufflebag/internal/compat/hcp2023/models"
	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/go-openapi/strfmt"
)

// getRegistry answers the registry document for a project.
//
// Required rather than optional: one of Packer's four organization/project
// resolution paths (project pinned, organization not) calls this and errors if
// it gets no registry back (ADR-0003).
func (h *handler) getRegistry(w http.ResponseWriter, r *http.Request) {
	tenant := tenant(r)

	// A registry is not a stored aggregate — it is the project, seen through the
	// vocabulary the 2023-01-01 API uses. There is exactly one per project, so it
	// is rendered rather than looked up, and cannot be missing for a tenant the
	// caller has already been authorized for.
	organization, project := tenant.OrganizationID.String(), tenant.ProjectID.String()
	// PLUS rather than STANDARD: the tier gates features upstream.
	tier := models.HashicorpCloudPacker20230101RegistryConfigTierPLUS

	writeJSON(w, http.StatusOK, models.HashicorpCloudPacker20230101GetRegistryResponse{
		Registry: &models.HashicorpCloudPacker20230101Registry{
			ID: project,
			Location: &models.HashicorpCloudLocationLocation{
				OrganizationID: organization,
				ProjectID:      project,
			},
			CreatedAt: strfmt.DateTime(h.now().UTC()),
			UpdatedAt: strfmt.DateTime(h.now().UTC()),
			Config: &models.HashicorpCloudPacker20230101RegistryConfig{
				Activated:   true,
				FeatureTier: &tier,
			},
		},
	})
}

// getEnforcedBlocksByBucket answers the enforced blocks applying to a bucket.
//
// Packer calls this from Bucket.FetchEnforcedBlocks early in every build, so it
// sits on the build path whatever we think of the feature.
//
// The list is non-nil deliberately: the field has no omitempty, so a nil slice
// marshals to null, and a client ranging over it without a nil check would fail
// on a response that is supposed to mean "nothing to enforce".
func (h *handler) getEnforcedBlocksByBucket(w http.ResponseWriter, r *http.Request) {
	// The bucket is confirmed first so an unknown bucket answers not-found rather
	// than an empty block list, which would read as "nothing enforced here" for a
	// bucket that does not exist.
	if _, err := h.repository.GetBucket(r.Context(), tenant(r), r.PathValue("bucket")); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			writeBucketNotFound(w, r.PathValue("bucket"))
			return
		}
		h.writeInternal(w, r, "compat request failed", err)
		return
	}
	writeJSON(w, http.StatusOK, models.HashicorpCloudPacker20230101GetEnforcedBlocksByBucketResponse{
		EnforcedBlockDetail: []*models.HashicorpCloudPacker20230101EnforcedBlockDetail{},
	})
}

// listVersions returns a bucket's versions, newest first.
//
// Absent upstream from Packer's build path — the CLI works one fingerprint at a
// time — but the console needs it to show what a bucket contains, and the
// console is a compatibility-plane client like any other (ADR-0006).
func (h *handler) listVersions(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	if _, err := h.repository.GetBucket(r.Context(), tenant(r), bucket); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			// A bucket that does not exist is not the same claim as a bucket holding
			// no versions, and only one of them means "check the name".
			writeBucketNotFound(w, bucket)
			return
		}
		h.writeInternal(w, r, "compat request failed", err)
		return
	}

	versions, buildsByFingerprint, err := h.repository.ListVersions(r.Context(), tenant(r), bucket)
	if err != nil {
		h.writeInternal(w, r, "compat request failed", err)
		return
	}
	rendered := make([]*models.HashicorpCloudPacker20230101Version, 0, len(versions))
	for _, version := range versions {
		wire, err := renderVersion(tenant(r), version, buildsByFingerprint[version.Fingerprint], h.now().UTC())
		if err != nil {
			h.writeInternal(w, r, "render version", err)
			return
		}
		rendered = append(rendered, wire)
	}
	writeJSON(w, http.StatusOK, &models.HashicorpCloudPacker20230101ListVersionsResponse{
		Versions: rendered,
	})
}
