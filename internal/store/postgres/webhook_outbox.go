package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/store/postgres/postgresdb"
	"github.com/benemon/dufflebag/internal/webhook"
)

func enqueueWebhookEvent(
	ctx context.Context, q *postgresdb.Queries, tenant Tenant, operation string,
	target webhook.Target, payload any, occurredAt time.Time,
) error {
	actor := webhook.Actor{PrincipalID: "system:dufflebag", Name: "Dufflebag"}
	if caller, ok := identity.ActorFromContext(ctx); ok {
		actor = webhook.Actor{PrincipalID: caller.PrincipalID, Name: caller.Name}
	}
	targetJSON, err := json.Marshal(target)
	if err != nil {
		return fmt.Errorf("marshal webhook target: %w", err)
	}
	actorJSON, err := json.Marshal(actor)
	if err != nil {
		return fmt.Errorf("marshal webhook actor: %w", err)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal webhook payload: %w", err)
	}
	if err := q.EnqueueWebhookEvent(ctx, postgresdb.EnqueueWebhookEventParams{
		OrganizationID: tenant.OrganizationID, ProjectID: tenant.ProjectID,
		EventID: registry.NewID(occurredAt).String(), OccurredAt: occurredAt,
		Operation: operation, Target: targetJSON, Actor: actorJSON, Payload: payloadJSON,
	}); err != nil {
		return fmt.Errorf("enqueue %s webhook event: %w", operation, err)
	}
	return nil
}

func bucketWebhookPayload(tenant Tenant, bucket Bucket) map[string]any {
	platforms := bucket.Platforms
	if platforms == nil {
		platforms = []string{}
	}
	wire := map[string]any{
		"id": bucket.ID.String(), "name": bucket.Name, "description": bucket.Description,
		"labels": bucket.Labels, "platforms": platforms,
		"resource_name": "packer/project/" + tenant.ProjectID.String() + "/bucket/" + bucket.Name,
		"location": map[string]any{
			"organization_id": tenant.OrganizationID.String(), "project_id": tenant.ProjectID.String(),
		},
		"created_at": bucket.CreatedAt, "updated_at": bucket.UpdatedAt,
	}
	if bucket.LatestVersion != nil {
		wire["latest_version"] = versionWirePayload(bucket.LatestVersion, bucket.UpdatedAt)
	}
	return map[string]any{"bucket": wire}
}

func versionWebhookPayload(version *registry.Version, at time.Time) map[string]any {
	return map[string]any{"version": versionWirePayload(version, at)}
}

func versionWirePayload(version *registry.Version, at time.Time) map[string]any {
	name := "v0"
	status := "VERSION_RUNNING"
	if sequence, complete := version.Sequence(); complete {
		name = fmt.Sprintf("v%d", sequence)
		status = "VERSION_ACTIVE"
	}
	payload := map[string]any{
		"id": version.ID.String(), "name": name, "bucket_name": version.BucketName,
		"fingerprint": version.Fingerprint, "author_id": version.AuthorID,
		"has_descendants": version.HasDescendants, "template_type": string(version.TemplateType),
		"status": status, "builds": []any{}, "revoke_at": nil,
		"created_at": version.CreatedAt, "updated_at": version.UpdatedAt,
	}
	if revocation := version.Revocation(); revocation != nil {
		payload["revoke_at"] = revocation.RevokeAt
		payload["revocation_message"] = revocation.Message
		payload["revocation_author"] = revocation.Author
		payload["revocation_type"] = "MANUAL"
		if ancestor := revocation.InheritedFrom; ancestor != nil {
			payload["revocation_type"] = "INHERITED"
			payload["revocation_inherited_from"] = map[string]any{
				"bucket_name": ancestor.BucketName, "version_fingerprint": ancestor.Fingerprint,
				"version_id": ancestor.VersionID.String(), "version_name": ancestor.VersionName,
			}
		}
		if revocation.RevokeAt.After(at) {
			payload["status"] = "VERSION_REVOCATION_SCHEDULED"
		} else {
			payload["status"] = "VERSION_REVOKED"
		}
	}
	return payload
}

func channelWebhookPayload(channel *Channel, previous *Channel) map[string]any {
	render := func(value *Channel) map[string]any {
		if value == nil {
			return nil
		}
		result := map[string]any{
			"id": value.ID.String(), "bucket_name": value.BucketName, "name": value.Name,
			"restricted": value.Restricted, "managed": value.Managed,
			"author_id": value.AssignmentAuthorID, "created_at": value.CreatedAt, "updated_at": value.UpdatedAt,
		}
		if value.Version != nil {
			result["version"] = versionWirePayload(value.Version, value.UpdatedAt)
		}
		return result
	}
	payload := map[string]any{"channel": render(channel)}
	if previous != nil {
		payload["previous_channel"] = render(previous)
		if previous.Version != nil {
			payload["previous_version_fingerprint"] = previous.Version.Fingerprint
		}
	}
	return payload
}
