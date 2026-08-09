import { useCallback } from 'react'

import type { ApiOrganization } from '../api/client'
import { useAuth } from '../auth/AuthContext'

export type Tenant = {
  id: string
  organization: string
  projectID: string
  project: string
}

/**
 * The tenants this session may act on.
 *
 * Derived from the token and the server's listings, never from anything the
 * user can type: the console must not be able to name a tenant the server has
 * not already granted it (ADR-0017).
 *
 * Names come from the listings where available and fall back to identifiers,
 * because showing a plausible-looking name for the wrong tenant is worse than
 * showing an unfriendly one for the right tenant.
 */
export function useTenant() {
  const {
    state, organizations, boundOrganizationName, selectedOrganization,
    permittedProjects, projectNames, selectedProject, selectProject,
  } = useAuth()

  const organization = selectedOrganization ?? ''
  // A platform session knows organization names from its listing. A tenancy
  // session resolves the fixed binding carried in its token with an item read.
  const organizationName =
    organizations.find((candidate) => candidate.id === organization)?.name ??
    boundOrganizationName ?? organization
  const project = selectedProject ?? state?.claims.projectID ?? ''
  const projectTenants = permittedProjects.map((projectID) => ({
    id: `${organization}/${projectID}`,
    organization: organizationName,
    projectID,
    project: projectNames[projectID] ?? projectID,
  }))
  // The deliberate step up to organisation level (duf-4qr): a session not
  // bound to a project may stand at none. Rendered as a dash — a minimal
  // marker, not a wordy label — because the data screens' gap states do the
  // explaining. The empty projectID survives the oldest-project auto-select
  // ('' is not nullish), so choosing it is not silently undone.
  const aboveProjects = state !== null && state.claims.projectID === null
  const tenants = aboveProjects && organization !== ''
    ? [
        { id: `${organization}/`, organization: organizationName, projectID: '', project: '—' },
        ...projectTenants,
      ]
    : projectTenants
  const tenant = tenants.find((candidate) => candidate.projectID === project) ?? {
    id: `${organization}/${project}`,
    organization: organizationName,
    projectID: project,
    project: projectNames[project] ?? project,
  }

  return {
    tenant,
    tenants,
    setTenant: useCallback(
      (t: Tenant) => {
        selectTenantProject(t, selectProject)
      },
      [selectProject],
    ),
  }
}

export function selectTenantProject(tenant: Tenant, selectProject: (projectID: string) => void) {
  selectProject(tenant.projectID)
}

/**
 * The rows a PLATFORM session's organisation select offers: the deliberate
 * step back up to platform standing — ADR-0014's "nothing selected", where
 * platform principals are listed and created — ahead of the real
 * organisations, with the same dash treatment as the blank project row above.
 * It stores '', which the sole-organisation auto-select cannot undo ('' is
 * not nullish), so a root that stepped up stays up. Only a platform session
 * renders an organisation select at all; a tenancy session's organisation is
 * the token's and is never a choice, so it never sees this row.
 */
export function organizationRows(organizations: ApiOrganization[]): ApiOrganization[] {
  return [{ id: '', name: '—', created_at: '' }, ...organizations]
}

/** Why a data screen has nothing to query yet, said plainly (duf-tkw). */
export type TenancyGap = {
  title: string
  detail: string
}

/**
 * What a session standing ABOVE a project is missing before buckets and
 * channels can be scoped, once the listings have settled. Null means fully
 * scoped.
 *
 * A closed set of honest states rather than a silent empty table: until the
 * picker names a whole tenancy there is genuinely nothing to fetch — and
 * fetching with fabricated identifiers is the alternative this replaces.
 *
 * `platform` gates the organisation half: only a platform session chooses its
 * organisation. An organisation-scoped session standing at the blank project
 * row (duf-4qr) reaches only the project half, since its organisation is the
 * token's and never a choice.
 */
export function platformTenancyGap({
  platform, organizationCount, selectedOrganization, projectCount, selectedProject,
}: {
  platform: boolean
  organizationCount: number
  selectedOrganization: string | null
  projectCount: number
  selectedProject: string | null
}): TenancyGap | null {
  if (platform) {
    if (organizationCount === 0) {
      return {
        title: 'No organisations exist',
        detail:
          'This session is platform-scoped and there is no organisation to view. ' +
          'Buckets and channels live inside an organisation’s project; ' +
          'Principals and Instance work without one.',
      }
    }
    if (!selectedOrganization) {
      return {
        title: 'Choose an organisation',
        detail:
          'This platform-scoped session can view any organisation. ' +
          'Pick one from the header to see its buckets and channels.',
      }
    }
  }
  if (projectCount === 0) {
    return {
      title: 'No projects in this organisation',
      detail: 'Buckets live inside a project, and this organisation has none.',
    }
  }
  if (!selectedProject) {
    return {
      title: 'Choose a project',
      detail: 'Pick a project from the header to see what it holds.',
    }
  }
  return null
}
