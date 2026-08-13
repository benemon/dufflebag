export const ROLES = ['reader', 'builder', 'publisher', 'maintainer', 'root'] as const
export type Role = (typeof ROLES)[number]

export const ACTION_REQUIREMENTS = {
  // internal/platform/v1/handler.go:343's CreateOrganization authorizes platform RoleRoot.
  createOrganizations: 'root',
  // internal/platform/v1/handler.go:463's CreateProject authorizes tenancy RoleMaintainer.
  createProjects: 'maintainer',
  // internal/platform/v1/handler.go's SetPin and DeletePin authorize RoleBuilder.
  pinBuckets: 'builder',
  // Compatibility-plane registry safety operations authorize RolePublisher.
  revokeVersions: 'publisher',
  // Compatibility-plane version deletion authorizes RolePublisher.
  deleteVersions: 'publisher',
  // Compatibility-plane channel lifecycle and assignment authorize RolePublisher.
  manageChannels: 'publisher',
  // Compatibility-plane bucket deletion authorizes RolePublisher.
  deleteBuckets: 'publisher',
  // internal/platform/v1/audit_targets.go checks RoleRoot for create/delete.
  configureAudit: 'root',
  // internal/platform/v1/encryption.go checks RoleRoot for encryption operations.
  manageEncryption: 'root',
  // internal/platform/v1/handler.go checks RoleMaintainer for principal and secret lifecycle.
  managePrincipals: 'maintainer',
  // internal/platform/v1/bagdrop.go:16's admitBagDrop authorizes RoleMaintainer.
  configureBagDrop: 'maintainer',
  // internal/platform/v1/webhooks.go uses admitBagDrop's maintainer disclosure funnel.
  configureWebhooks: 'maintainer',
} as const satisfies Record<string, Role>

export type ConsoleAction = keyof typeof ACTION_REQUIREMENTS

export const NAV_REQUIREMENTS = {
  // internal/compat/hcp2023/handler.go's route table authorizes registry reads at reader.
  buckets: 'reader',
  // internal/platform/v1/handler.go:621 authorizes ListPrincipals at maintainer.
  principals: 'maintainer',
  // internal/platform/v1/audit_targets.go:20 authorizes ListAuditTargets at root.
  audit: 'root',
  // internal/platform/v1/encryption.go:15 authorizes GetEncryption at root.
  encryption: 'root',
  // internal/platform/v1/bagdrop.go:37's admitBagDropStatus authorizes RoleReader.
  bagdrop: 'reader',
  // internal/platform/v1/webhooks.go gates the conventional screen at maintainer.
  webhooks: 'maintainer',
  // internal/platform/v1/handler.go:1214 authorizes GetInstance at reader.
  instance: 'reader',
} as const satisfies Record<string, Role>

export type NavKey = keyof typeof NAV_REQUIREMENTS

export function allowedActions(role: Role | null): ConsoleAction[] {
  if (role === null) return []
  const rank = ROLES.indexOf(role)
  return (Object.keys(ACTION_REQUIREMENTS) as ConsoleAction[])
    .filter((action) => rank >= ROLES.indexOf(ACTION_REQUIREMENTS[action]))
}

export function visibleNavItems(role: Role | null): NavKey[] {
  // Until /self resolves (or when it fails), show only reader-tier navigation:
  // this is a flicker-free safe default, while direct URLs still answer honestly.
  const rank = ROLES.indexOf(role ?? 'reader')
  return (Object.keys(NAV_REQUIREMENTS) as NavKey[])
    .filter((item) => rank >= ROLES.indexOf(NAV_REQUIREMENTS[item]))
}

export function permitsAction(role: Role | null, action: ConsoleAction): boolean {
  return allowedActions(role).includes(action)
}

export function requirementReason(action: ConsoleAction): string {
  const required = ACTION_REQUIREMENTS[action]
  return `Requires ${required}`
}
