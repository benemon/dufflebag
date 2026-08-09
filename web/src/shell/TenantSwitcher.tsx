import { useState } from 'react'
import { Content, MenuToggle, Select, SelectList, SelectOption } from '@patternfly/react-core'
import type { MenuToggleElement } from '@patternfly/react-core'

import { useAuth } from '../auth/AuthContext'
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
 * the dash row back to platform standing, because ADR-0014's "nothing
 * selected" must stay reachable after the sole-organisation auto-select.
 */
export function TenantSwitcher() {
  const {
    state, organizations, organizationsLoading, organizationFailure,
    organizationRefreshFailure, selectedOrganization,
  } = useAuth()

  const platform = state !== null && state.claims.organizationID === null
  if (!platform) {
    return <ProjectSelect combined />
  }
  // Settled truths render as text, not as an empty menu pretending to offer
  // something: an empty listing and a failed one are different facts.
  if (organizationsLoading) {
    return <Content component="p">Loading organisations…</Content>
  }
  if (organizationFailure) {
    return <Content component="p">Organisations could not be loaded</Content>
  }
  if (organizations.length === 0) {
    return <Content component="p">No organisations exist</Content>
  }
  return (
    <>
      <OrganizationSelect />
      {organizationRefreshFailure ? (
        <span role="alert">Organisations could not be refreshed: {organizationRefreshFailure}</span>
      ) : null}
      {selectedOrganization ? <ProjectSelect /> : null}
    </>
  )
}

export function refreshOrganizationsOnPickerOpen(
  open: boolean,
  refreshOrganizations: () => Promise<void>,
) {
  if (open) void refreshOrganizations()
}

function OrganizationSelect() {
  const { organizations, selectedOrganization, selectOrganization, refreshOrganizations } = useAuth()
  const [open, setOpen] = useState(false)
  // The dash row ahead of the real organisations: the deliberate step back up
  // to platform standing (ADR-0014's "nothing selected"), matching the blank
  // project row (duf-4qr). One list feeds the options and the toggle both, so
  // the dash cannot exist in one and not the other. Selecting it stores '' —
  // which the sole-organisation auto-select cannot undo — and clears the
  // project selection with it; the data screens state the platform gap.
  const rows = organizationRows(organizations)
  const selected = rows.find((candidate) => candidate.id === selectedOrganization)
  const setPickerOpen = (nextOpen: boolean) => {
    setOpen(nextOpen)
    refreshOrganizationsOnPickerOpen(nextOpen, refreshOrganizations)
  }

  return (
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
    </Select>
  )
}

/**
 * The project half. `combined` labels options as "organisation / project" for
 * the tenancy-scoped session, whose single selector carries the whole pair; the
 * platform session's sits beside an organisation selector already naming the
 * organisation, so repeating it would say nothing.
 */
function ProjectSelect({ combined = false }: { combined?: boolean }) {
  const { tenant, tenants, setTenant } = useTenant()
  const [open, setOpen] = useState(false)
  const label = (t: typeof tenant) => (combined ? `${t.organization} / ${t.project}` : t.project)

  return (
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
    </Select>
  )
}
