// Package postgres persists the domain aggregates.
//
// Depends on the domain, never the reverse, and never on a wire model.
// Tenant isolation is enforced by row-level security driven by a per-request
// SET LOCAL inside a transaction — see docs/architecture.md.
package postgres
