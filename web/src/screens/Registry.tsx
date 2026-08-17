import {
  Alert, Button, Content, EmptyState, EmptyStateActions, EmptyStateBody, EmptyStateFooter,
  PageSection,
} from '@patternfly/react-core'
import { useNavigate } from 'react-router'

import { useAuth } from '../auth/AuthContext'
import type { Role } from '../auth/permissions'
import { TenancyGapEmptyState } from '../components/TenancyCreation'
import { platformTenancyGap, type TenancyGap } from '../data/tenant'

export function Registry() {
  const navigate = useNavigate()
  const {
    state, self, organizations, organizationsLoading, organizationFailure,
    selectedOrganization, permittedProjects, projectsLoading, projectFailure, selectedProject,
  } = useAuth()
  const platform = state !== null && state.claims.organizationID === null
  const aboveProjects = state !== null && state.claims.projectID === null
  // Failures are stated, not classified as settling: an organisation list that
  // failed to load must not read as "No organisations exist" (the old
  // discoveryFailure wiring, data/buckets.ts before the picker rework).
  const failure = aboveProjects ? (organizationFailure ?? projectFailure) : null
  const settling = organizationsLoading || projectsLoading
  const gap = aboveProjects && !settling && !failure
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
        failure={failure}
        gap={gap}
        callerRole={self?.role ?? null}
        onConnectClient={() => navigate('/instance')}
      />
    </PageSection>
  )
}

export function RegistryView({
  failure = null, gap, callerRole, onConnectClient,
}: {
  failure?: string | null
  gap: TenancyGap | null
  callerRole: Role | null
  onConnectClient: () => void
}) {
  if (failure) {
    return (
      <Alert variant="danger" isInline title="Registry could not be loaded">
        <Content component="p">{failure}</Content>
      </Alert>
    )
  }
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
