import {
  Button, EmptyState, EmptyStateActions, EmptyStateBody, EmptyStateFooter, PageSection,
} from '@patternfly/react-core'
import { useNavigate } from 'react-router'

import { useAuth } from '../auth/AuthContext'
import type { Role } from '../auth/permissions'
import { TenancyGapEmptyState } from '../components/TenancyCreation'
import { platformTenancyGap, type TenancyGap } from '../data/tenant'

export function Registry() {
  const navigate = useNavigate()
  const {
    state, self, organizations, organizationsLoading, selectedOrganization,
    permittedProjects, projectsLoading, projectFailure, selectedProject,
  } = useAuth()
  const platform = state !== null && state.claims.organizationID === null
  const aboveProjects = state !== null && state.claims.projectID === null
  const settling = organizationsLoading || projectsLoading || projectFailure !== null
  const gap = aboveProjects && !settling
    ? platformTenancyGap({
        platform,
        organizationCount: organizations.length,
        selectedOrganization,
        projectCount: permittedProjects.length,
        // The blank row stores '' so the auto-select cannot undo it; the
        // gap helper only asks whether a project is in effect.
        selectedProject: selectedProject || null,
      })
    : null
  return (
    <PageSection variant="secondary" isFilled>
      <RegistryView
        gap={gap}
        callerRole={self?.role ?? null}
        onConnectClient={() => navigate('/instance')}
      />
    </PageSection>
  )
}

export function RegistryView({
  gap, callerRole, onConnectClient,
}: {
  gap: TenancyGap | null
  callerRole: Role | null
  onConnectClient: () => void
}) {
  if (gap) return <TenancyGapEmptyState gap={gap} callerRole={callerRole} />
  return (
    <EmptyState titleText="Choose a bucket" headingLevel="h2">
      <EmptyStateBody>
        Pick a bucket from the masthead picker, or create one there.
      </EmptyStateBody>
      <EmptyStateFooter>
        <EmptyStateActions>
          <Button variant="primary" onClick={onConnectClient}>Connect a client</Button>
        </EmptyStateActions>
        <EmptyStateActions>
          <Button
            component="a"
            href="https://developer.hashicorp.com/packer/docs"
            target="_blank"
            rel="noreferrer"
            variant="link"
          >
            Packer docs
          </Button>
        </EmptyStateActions>
      </EmptyStateFooter>
    </EmptyState>
  )
}
