package identity

import (
	"time"
)

// IdentityKind says how the actor proved, or failed to prove, its identity.
type IdentityKind string

const (
	SystemScannerPrincipalID = "system:scanner"
	SystemBagDropPrincipalID = "system:bagdrop"
)

const (
	IdentityKindAnonymous        IdentityKind = "anonymous"
	IdentityKindServicePrincipal IdentityKind = "service_principal"
	IdentityKindSystem           IdentityKind = "system"
	IdentityKindUnknown          IdentityKind = "unknown"
)

// AuditOperation names the security-relevant action being attempted.
type AuditOperation string

const (
	AuditOperationTokenIssue         AuditOperation = "token.issue"
	AuditOperationProjectList        AuditOperation = "project.list"
	AuditOperationProjectRead        AuditOperation = "project.read"
	AuditOperationInstanceInitialize AuditOperation = "instance.initialize"
	AuditOperationInstanceRecover    AuditOperation = "instance.recover"

	// The console's cookie session (duf-1cn): a credential materializing into
	// or leaving the browser's cookie jar is a lifecycle event like issuance.
	AuditOperationSessionCreate AuditOperation = "session.create"
	AuditOperationSessionDelete AuditOperation = "session.delete"

	// The principal and credential lifecycle. ADR-0019 makes audit a
	// PRECONDITION of the role model rather than a follow-up: permitting a human
	// to hold root is justified by attribution, and that justification is only as
	// good as the trail. Granting and revoking authority are the highest-value
	// entries in it.
	AuditOperationPrincipalList AuditOperation = "principal.list"
	AuditOperationPrincipalRead AuditOperation = "principal.read"

	AuditOperationPrincipalCreate AuditOperation = "principal.create"
	AuditOperationPrincipalDelete AuditOperation = "principal.delete"
	AuditOperationSecretIssue     AuditOperation = "secret.issue"
	AuditOperationSecretRevoke    AuditOperation = "secret.revoke"

	AuditOperationScanExecute   AuditOperation = "scan.execute"
	AuditOperationScanRetention AuditOperation = "scan.retention"
)

// AuditScope identifies where an operation was attempted.
type AuditScope string

const (
	AuditScopePlatform     AuditScope = "platform"
	AuditScopeOrganization AuditScope = "organization"
	AuditScopeProject      AuditScope = "project"
)

// AuditOutcome distinguishes completed actions from refusals and system failures.
type AuditOutcome string

const (
	AuditOutcomeSuccess AuditOutcome = "success"
	AuditOutcomeRefused AuditOutcome = "refused"
	AuditOutcomeFailure AuditOutcome = "failure"
)

// AuditTarget is one file the broker writes every audit event to.
//
// Slot is deliberately absent. The database assigns 1..3 to make a fourth
// target unwriteable (migration 000011), but that is storage enforcing a limit,
// not a property of the target — exposing it would invite a client to choose
// one, and the ordering it implies means nothing to a caller.
type AuditTarget struct {
	ID        string
	Path      string
	CreatedAt time.Time
}
