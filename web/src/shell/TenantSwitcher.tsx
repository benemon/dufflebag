import { useState, type ReactNode } from 'react'
import {
  Button, Content, MenuFooter, MenuToggle, Popover, Select, SelectList, SelectOption, Skeleton,
} from '@patternfly/react-core'
import type { MenuToggleElement } from '@patternfly/react-core'
import ExclamationCircleIcon from '@patternfly/react-icons/dist/esm/icons/exclamation-circle-icon'

import { useAuth } from '../auth/AuthContext'
import { CreateTenancyButton } from '../components/TenancyCreation'
import { organizationRows, useTenant } from '../data/tenant'

/**
 * Organization / project selector.
 *
 * A tenant is the (organization, project) pair, and every compatibility-plane
 * path carries both — so nothing can be fetched until one is chosen.
 *
 * A tenancy-scoped session shows one combined selector, because its
 * organisation is the token's and is never a choice. A platform-scoped session
 * (the bootstrap root, duf-tkw) chooses the organisation first, so it gets two:
 * organisation, then project within it — and the organisation select carries
 * an explicit platform row, because ADR-0014's "nothing
 * selected" must stay reachable after the sole-organisation auto-select.
 */
export function TenantSwitcher() {
  const {
    state, self, organizations, organizationsLoading, organizationFailure,
    organizationRefreshFailure, selectedOrganization,
  } = useAuth()

  const platform = state !== null && state.claims.organizationID === null
  if (!platform) {
    return <ProjectSelect combined />
  }
  // Settled truths render as text, not as an empty menu pretending to offer
  // something: an empty listing and a failed one are different facts.
  if (organizationsLoading) {
    return (
      <PickerField label="Organisation">
        <Skeleton width="10rem" fontSize="lg" screenreaderText="Loading organisations…" />
      </PickerField>
    )
  }
  if (organizationFailure) {
    return (
      <PickerField label="Organisation">
        <Content component="p" style={{ margin: 0 }}>Organisations could not be loaded</Content>
      </PickerField>
    )
  }
  if (organizations.length === 0) {
    return (
      <PickerField label="Organisation">
        <Content component="p" style={{ margin: 0 }}>No organisations exist</Content>
        <CreateTenancyButton kind="organization" callerRole={self?.role ?? null} variant="link" />
      </PickerField>
    )
  }
  return (
    <span className="tenant-switchers">
      <OrganizationSelect refreshFailure={organizationRefreshFailure} />
      {selectedOrganization ? <ProjectSelect /> : null}
    </span>
  )
}

export function refreshOrganizationsOnPickerOpen(
  open: boolean,
  refreshOrganizations: () => Promise<unknown>,
) {
  if (open) void refreshOrganizations()
}

function PickerField({ label, children }: { label: string; children: ReactNode }) {
  return (
    <span className="tenant-picker">
      <span className="tenant-picker-caption">{label}:</span>
      {children}
    </span>
  )
}

function OrganizationSelect({ refreshFailure }: { refreshFailure: string | null }) {
  const {
    self, organizations, selectedOrganization, selectOrganization, refreshOrganizations,
  } = useAuth()
  const [open, setOpen] = useState(false)
  // The platform row ahead of the real organisations is the deliberate step
  // back up to platform standing (ADR-0014's "nothing selected"). Selecting it
  // stores '' — which the sole-organisation auto-select cannot undo — and
  // clears the project selection with it; the data screens state the gap.
  const rows = organizationRows(organizations)
  const selected = rows.find((candidate) => candidate.id === selectedOrganization)
  const setPickerOpen = (nextOpen: boolean) => {
    setOpen(nextOpen)
    refreshOrganizationsOnPickerOpen(nextOpen, refreshOrganizations)
  }

  return (
    <PickerField label="Organisation">
      <Select
        isOpen={open}
        selected={selectedOrganization ?? undefined}
        onSelect={(_e, value) => {
          if (typeof value === 'string') selectOrganization(value)
          setOpen(false)
        }}
        onOpenChange={setPickerOpen}
        toggle={(ref: React.Ref<MenuToggleElement>) => (
          <MenuToggle
            id="tenant-organization"
            ref={ref}
            isExpanded={open}
            onClick={() => setPickerOpen(!open)}
            variant="plainText"
          >
            {selected?.name ?? 'Choose an organisation'}
          </MenuToggle>
        )}
      >
        <SelectList>
          {rows.map((organization) => (
            <SelectOption key={organization.id} value={organization.id}>
              {organization.name}
            </SelectOption>
          ))}
        </SelectList>
        <MenuFooter>
          <CreateTenancyButton
            kind="organization"
            callerRole={self?.role ?? null}
            variant="link"
          />
        </MenuFooter>
      </Select>
      {refreshFailure ? (
        <Popover
          aria-label="Organisation refresh failure"
          alertSeverityVariant="danger"
          bodyContent={(
            <span role="alert">Organisations could not be refreshed: {refreshFailure}</span>
          )}
        >
          <Button
            variant="plain"
            size="sm"
            hasNoPadding
            aria-label="Show organisation refresh failure"
            icon={<ExclamationCircleIcon />}
          />
        </Popover>
      ) : null}
    </PickerField>
  )
}

/**
 * The project half. `combined` labels options as "organisation / project" for
 * the tenancy-scoped session, whose single selector carries the whole pair; the
 * platform session's sits beside an organisation selector already naming the
 * organisation, so repeating it would say nothing.
 */
function ProjectSelect({ combined = false }: { combined?: boolean }) {
  const { self, selectedOrganization } = useAuth()
  const { tenant, tenants, setTenant } = useTenant()
  const [open, setOpen] = useState(false)
  const label = (t: typeof tenant) => (combined ? `${t.organization} / ${t.project}` : t.project)

  return (
    <PickerField label="Project">
      <Select
        isOpen={open}
        selected={tenant.id}
        onSelect={(_e, value) => {
          const next = tenants.find((t) => t.id === value)
          if (next) setTenant(next)
          setOpen(false)
        }}
        onOpenChange={setOpen}
        toggle={(ref: React.Ref<MenuToggleElement>) => (
          <MenuToggle
            id="tenant-project"
            ref={ref}
            isExpanded={open}
            onClick={() => setOpen(!open)}
            variant="plainText"
          >
            {label(tenant)}
          </MenuToggle>
        )}
      >
        <SelectList>
          {tenants.map((t) => (
            <SelectOption key={t.id} value={t.id}>
              {label(t)}
            </SelectOption>
          ))}
        </SelectList>
        <MenuFooter>
          <CreateTenancyButton
            kind="project"
            callerRole={self?.role ?? null}
            organizationID={selectedOrganization ?? undefined}
            variant="link"
          />
        </MenuFooter>
      </Select>
    </PickerField>
  )
}
