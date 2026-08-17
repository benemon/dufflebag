import { useState } from 'react'
import {
  Alert, Content, EmptyState, EmptyStateActions, EmptyStateBody, EmptyStateFooter,
  Modal, ModalHeader, type ButtonProps,
} from '@patternfly/react-core'

import { createOrganization, createProject } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { RoleRestrictedButton } from '../auth/RoleRestrictedButton'
import type { Role } from '../auth/permissions'
import { useTenant, type TenancyGap } from '../data/tenant'
import { TenancyForm, type TenancyKind } from './TenancyForm'

const labels = {
  organization: 'Create organisation',
  project: 'Create project',
} as const

export async function refreshThenSelect<T>(
  created: T,
  refresh: () => Promise<T[] | null>,
  select: (created: T) => void,
  identity: (item: T) => string = (item) => (item as { id: string }).id,
) {
  const listed = await refresh()
  if (!listed?.some((candidate) => identity(candidate) === identity(created))) {
    throw new Error('The new resource was created but its listing could not be refreshed.')
  }
  select(created)
}

export function CreateTenancyButton({
  kind, callerRole, organizationID, variant = 'primary',
}: {
  kind: TenancyKind
  callerRole: Role | null
  organizationID?: string
  variant?: ButtonProps['variant']
}) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <RoleRestrictedButton
        action={kind === 'organization' ? 'createOrganizations' : 'createProjects'}
        callerRole={callerRole}
        variant={variant}
        isDisabled={kind === 'project' && !organizationID}
        onClick={(event) => {
          event.stopPropagation()
          setOpen(true)
        }}
      >
        {labels[kind]}
      </RoleRestrictedButton>
      {open ? (
        <TenancyModal kind={kind} organizationID={organizationID} onClose={() => setOpen(false)} />
      ) : null}
    </>
  )
}

function TenancyModal({
  kind, organizationID, onClose,
}: {
  kind: TenancyKind
  organizationID?: string
  onClose: () => void
}) {
  const {
    state, refreshOrganizations, selectOrganization, refreshProjects,
  } = useAuth()
  const { setTenant } = useTenant()
  const [submitting, setSubmitting] = useState(false)
  const [failure, setFailure] = useState<string | null>(null)
  const submit = async (name: string) => {
    setSubmitting(true)
    setFailure(null)
    try {
      if (!state) throw new Error('No session.')
      if (kind === 'organization') {
        const created = await createOrganization(state.token, name)
        await refreshThenSelect(created, refreshOrganizations, (organization) => {
          selectOrganization(organization.id)
        })
      } else {
        if (!organizationID) throw new Error('Choose an organisation first.')
        const created = await createProject(state.token, organizationID, name)
        await refreshThenSelect(created, refreshProjects, (project) => {
          setTenant({
            id: `${organizationID}/${project.id}`,
            organization: organizationID,
            projectID: project.id,
            project: project.name,
          })
        })
      }
      onClose()
    } catch (err: unknown) {
      setFailure(err instanceof Error ? err.message : 'The tenancy could not be created.')
    } finally {
      setSubmitting(false)
    }
  }
  return (
    <TenancyModalView
      kind={kind}
      submitting={submitting}
      failure={failure}
      onSubmit={submit}
      onClose={onClose}
    />
  )
}

export function TenancyModalView({
  kind, submitting, failure, onSubmit, onClose,
}: {
  kind: TenancyKind
  submitting: boolean
  failure: string | null
  onSubmit: (name: string) => Promise<void>
  onClose: () => void
}) {
  const label = labels[kind]
  return (
    <Modal aria-labelledby={`create-${kind}-modal-title`} isOpen onClose={onClose} variant="small">
      <ModalHeader labelId={`create-${kind}-modal-title`} title={label} />
      <TenancyForm
        kind={kind}
        formID={`create-${kind}`}
        submitLabel={label}
        submitting={submitting}
        footer="modal"
        message={failure ? (
          <Alert variant="danger" isInline title="The tenancy could not be created">
            <Content component="p">{failure}</Content>
          </Alert>
        ) : null}
        onSubmit={onSubmit}
        onCancel={onClose}
      />
    </Modal>
  )
}

export function TenancyGapEmptyState({
  gap, callerRole,
}: {
  gap: TenancyGap
  callerRole: Role | null
}) {
  return (
    <EmptyState titleText={gap.title} headingLevel="h2">
      <EmptyStateBody>{gap.detail}</EmptyStateBody>
      <EmptyStateFooter>
        <EmptyStateActions>
          <CreateTenancyButton
            kind={gap.resource}
            callerRole={callerRole}
            organizationID={gap.organizationID}
          />
        </EmptyStateActions>
      </EmptyStateFooter>
    </EmptyState>
  )
}
