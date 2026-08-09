// Package registry holds the domain aggregates: buckets, versions, builds,
// artifacts and channels.
//
// Nothing here may import a wire model. Completion is a bool, not the "v0"
// sentinel; not-found is one domain error, not a per-endpoint gRPC code.
// Adapters under internal/compat translate. Enforced by depguard — see
// docs/architecture.md.
package registry
