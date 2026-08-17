import { useEffect, useRef, useState, type ReactNode } from 'react'
import {
  Button, Content, MenuFooter, MenuToggle, Popover, Select, SelectGroup, SelectList, SelectOption,
  Skeleton, TextInputGroup, TextInputGroupMain, TextInputGroupUtilities,
} from '@patternfly/react-core'
import type { MenuToggleElement } from '@patternfly/react-core'
import TimesIcon from '@patternfly/react-icons/dist/esm/icons/times-icon'
import ExclamationCircleIcon from '@patternfly/react-icons/dist/esm/icons/exclamation-circle-icon'
import { useNavigate, useParams } from 'react-router'

import { createBucket } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import type { Role } from '../auth/permissions'
import { CreateBucketButton } from '../components/BucketCreation'
import { CreateTenancyButton, refreshThenSelect } from '../components/TenancyCreation'
import { useBucketPicker } from '../data/bucketPicker'
import { useAutoRefresh } from '../data/polling'
import { organizationRows, useTenant } from '../data/tenant'

type PickerOption = {
  value: string
  label: string
  group?: string
}

/** Organization, project and route-derived bucket selectors. */
export function TenantSwitcher() {
  const {
    state, self, organizations, organizationsLoading, organizationFailure,
    organizationRefreshFailure, selectedOrganization,
  } = useAuth()

  const platform = state !== null && state.claims.organizationID === null
  if (!platform) {
    return (
      <span className="tenant-switchers">
        <ProjectSelect combined />
        <BucketPicker />
      </span>
    )
  }

  let organizationPicker: ReactNode
  if (organizationsLoading) {
    organizationPicker = (
      <PickerField label="Organisation">
        <Skeleton width="10rem" fontSize="lg" screenreaderText="Loading organisations…" />
      </PickerField>
    )
  } else if (organizationFailure) {
    organizationPicker = (
      <PickerField label="Organisation">
        <Content component="p" style={{ margin: 0 }}>Organisations could not be loaded</Content>
      </PickerField>
    )
  } else if (organizations.length === 0) {
    organizationPicker = (
      <PickerField label="Organisation">
        <Content component="p" style={{ margin: 0 }}>No organisations exist</Content>
        <CreateTenancyButton kind="organization" callerRole={self?.role ?? null} variant="link" />
      </PickerField>
    )
  } else {
    organizationPicker = <OrganizationSelect refreshFailure={organizationRefreshFailure} />
  }

  return (
    <span className="tenant-switchers">
      {organizationPicker}
      {selectedOrganization ? <ProjectSelect /> : null}
      <BucketPicker />
    </span>
  )
}

// A picker refreshes when opened so resources created elsewhere do not require
// a tenancy round-trip before they appear.
export function refreshOnPickerOpen(
  open: boolean,
  refresh: () => Promise<unknown>,
) {
  if (open) void refresh()
}

function PickerField({ label, children }: { label: string; children: ReactNode }) {
  return (
    <span className="tenant-picker">
      <span className="tenant-picker-caption">{label}:</span>
      {children}
    </span>
  )
}

function TypeaheadPicker({
  id, label, options, selectedValue, selectedLabel, footer, onSelect, onOpen,
}: {
  id: string
  label: string
  options: PickerOption[]
  selectedValue?: string
  selectedLabel: string
  footer: ReactNode
  onSelect: (value: string) => void
  onOpen: () => void
}) {
  const [open, setOpen] = useState(false)
  const [inputValue, setInputValue] = useState(selectedLabel)
  const [filterValue, setFilterValue] = useState('')
  const inputRef = useRef<HTMLInputElement>(null)
  const query = filterValue.trim().toLowerCase()
  const filtered = options.filter((option) => option.label.toLowerCase().includes(query))
  const groups = [...new Set(filtered.flatMap((option) => option.group ? [option.group] : []))]

  useEffect(() => {
    if (!open) setInputValue(selectedLabel)
  }, [open, selectedLabel])

  const setPickerOpen = (nextOpen: boolean) => {
    setOpen(nextOpen)
    if (nextOpen) {
      setInputValue(selectedLabel)
      setFilterValue('')
      onOpen()
    } else {
      setInputValue(selectedLabel)
      setFilterValue('')
    }
  }
  const select = (value: string) => {
    const option = options.find((candidate) => candidate.value === value)
    if (!option) return
    setInputValue(option.label)
    setFilterValue('')
    onSelect(value)
    setOpen(false)
  }
  const optionNode = (option: PickerOption) => (
    <SelectOption key={option.value} value={option.value}>{option.label}</SelectOption>
  )

  return (
    <Select
      isOpen={open}
      selected={filterValue ? undefined : selectedValue}
      onSelect={(_event, value) => {
        if (typeof value === 'string') select(value)
      }}
      onOpenChange={setPickerOpen}
      variant="typeahead"
      toggle={(ref: React.Ref<MenuToggleElement>) => (
        <MenuToggle
          id={id}
          ref={ref}
          isExpanded={open}
          onClick={() => {
            setPickerOpen(!open)
            inputRef.current?.focus()
          }}
          variant="typeahead"
        >
          <TextInputGroup isPlain>
            <TextInputGroupMain
              inputId={`${id}-input`}
              value={inputValue}
              onClick={() => { if (!open) setPickerOpen(true) }}
              onChange={(_event, value) => {
                setInputValue(value)
                setFilterValue(value)
                if (!open) {
                  setOpen(true)
                  onOpen()
                }
              }}
              autoComplete="off"
              innerRef={inputRef}
              aria-label={`Search ${label.toLowerCase()}`}
              placeholder={selectedLabel}
              role="combobox"
              isExpanded={open}
              aria-controls={`${id}-listbox`}
            />
            <TextInputGroupUtilities style={inputValue ? undefined : { display: 'none' }}>
              <Button
                variant="plain"
                aria-label={`Clear ${label.toLowerCase()} search`}
                icon={<TimesIcon />}
                onClick={(event) => {
                  event.stopPropagation()
                  setInputValue('')
                  setFilterValue('')
                  inputRef.current?.focus()
                }}
              />
            </TextInputGroupUtilities>
          </TextInputGroup>
        </MenuToggle>
      )}
    >
      <SelectList id={`${id}-listbox`}>
        {filtered.length === 0 ? (
          <SelectOption isAriaDisabled value="__no-results">
            No results found for “{filterValue}”
          </SelectOption>
        ) : groups.length > 0 ? (
          <>
            {filtered.filter((option) => !option.group).map(optionNode)}
            {groups.map((group) => (
              <SelectGroup key={group} label={group}>
                {filtered.filter((option) => option.group === group).map(optionNode)}
              </SelectGroup>
            ))}
          </>
        ) : filtered.map(optionNode)}
      </SelectList>
      {footer}
    </Select>
  )
}

function OrganizationSelect({ refreshFailure }: { refreshFailure: string | null }) {
  const {
    self, organizations, selectedOrganization, selectOrganization, refreshOrganizations,
  } = useAuth()
  const rows = organizationRows(organizations)
  const selected = rows.find((candidate) => candidate.id === selectedOrganization)
  return (
    <PickerField label="Organisation">
      <TypeaheadPicker
        id="tenant-organization"
        label="Organisation"
        options={rows.map((organization) => ({
          value: organization.id,
          label: organization.name,
        }))}
        selectedValue={selectedOrganization ?? undefined}
        selectedLabel={selected?.name ?? 'Choose an organisation'}
        onSelect={selectOrganization}
        onOpen={() => refreshOnPickerOpen(true, refreshOrganizations)}
        footer={(
          <MenuFooter>
            <CreateTenancyButton
              kind="organization"
              callerRole={self?.role ?? null}
              variant="link"
            />
          </MenuFooter>
        )}
      />
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

function ProjectSelect({ combined = false }: { combined?: boolean }) {
  const {
    self, selectedOrganization, permittedProjects, projectsLoading, projectFailure,
    refreshProjects,
  } = useAuth()
  const { tenant, tenants, setTenant } = useTenant()
  const label = (candidate: typeof tenant) => (
    combined ? `${candidate.organization} / ${candidate.project}` : candidate.project
  )

  if (projectsLoading) {
    return (
      <PickerField label="Project">
        <Skeleton width="10rem" fontSize="lg" screenreaderText="Loading projects…" />
      </PickerField>
    )
  }
  if (projectFailure) {
    return (
      <PickerField label="Project">
        <Content component="p" style={{ margin: 0 }}>Projects could not be loaded</Content>
      </PickerField>
    )
  }
  if (permittedProjects.length === 0) {
    return (
      <PickerField label="Project">
        <Content component="p" style={{ margin: 0 }}>No projects exist</Content>
        <CreateTenancyButton
          kind="project"
          callerRole={self?.role ?? null}
          organizationID={selectedOrganization ?? undefined}
          variant="link"
        />
      </PickerField>
    )
  }

  return (
    <PickerField label="Project">
      <TypeaheadPicker
        id="tenant-project"
        label="Project"
        options={tenants.map((candidate) => ({
          value: candidate.id,
          label: label(candidate),
        }))}
        selectedValue={tenant.id}
        selectedLabel={label(tenant)}
        onSelect={(value) => {
          const next = tenants.find((candidate) => candidate.id === value)
          if (next) setTenant(next)
        }}
        onOpen={() => refreshOnPickerOpen(true, refreshProjects)}
        footer={(
          <MenuFooter>
            <CreateTenancyButton
              kind="project"
              callerRole={self?.role ?? null}
              organizationID={selectedOrganization ?? undefined}
              variant="link"
            />
          </MenuFooter>
        )}
      />
    </PickerField>
  )
}

export function BucketPicker() {
  const { bucket } = useParams()
  const navigate = useNavigate()
  const {
    state, self, selectedOrganization, selectedProject, selectedBucket, selectBucket,
  } = useAuth()
  const { buckets, pins, loading, failure, refresh } = useBucketPicker()
  const scoped = Boolean(state && selectedOrganization && selectedProject)
  // An empty listing renders text with no toggle, so refetch-on-open cannot
  // fire — and empty is exactly the awaiting-change state: the first publish
  // must appear without a reload. Poll while empty; stop once anything lands.
  useAutoRefresh({
    hot: scoped && !loading && failure === null && buckets.length === 0,
    onRefresh: refresh,
  })
  // Bucket routes stay authoritative: visiting one carries its bucket into
  // the tenancy context, so the selection survives onto screens whose routes
  // name no bucket — Principals derives its standing from it (duf-4qr).
  // Synced once per route value, never reconciled continuously: a deliberate
  // step-up clears the selection while the route is still current, and a
  // reconciling effect would immediately carry it back.
  const lastRouteSync = useRef<string | undefined>(undefined)
  useEffect(() => {
    // A tenancy change invalidates the sync: the route may name a bucket the
    // new pair does not own, and "already handled" must not leave the display
    // asserting a standing the context no longer carries.
    lastRouteSync.current = undefined
  }, [selectedOrganization, selectedProject])
  useEffect(() => {
    if (bucket === undefined) {
      lastRouteSync.current = undefined
      return
    }
    if (lastRouteSync.current === bucket) return
    const listed = buckets.find((candidate) => candidate.name === bucket)
    if (listed?.id) {
      lastRouteSync.current = bucket
      selectBucket({ id: listed.id, name: listed.name })
    }
  }, [bucket, buckets, selectBucket])
  const select = (name: string) => {
    if (name === '') {
      // The blank row is the deliberate step back up to project standing
      // (duf-4qr, extended to buckets).
      selectBucket(null)
      if (bucket !== undefined) navigate('/buckets')
      return
    }
    const listed = buckets.find((candidate) => candidate.name === name)
    if (listed?.id) selectBucket({ id: listed.id, name: listed.name })
    navigate(`/buckets/${encodeURIComponent(name)}`)
  }
  const create = async (name: string) => {
    if (!state || !selectedOrganization || !selectedProject) throw new Error('No session.')
    const created = await createBucket(state.token, {
      organizationID: selectedOrganization,
      projectID: selectedProject,
    }, name)
    await refreshThenSelect(
      created,
      refresh,
      (listed) => {
        if (listed.id) selectBucket({ id: listed.id, name: listed.name })
        navigate(`/buckets/${encodeURIComponent(listed.name)}`)
      },
      (listed) => listed.name,
    )
  }
  return (
    <BucketPickerView
      selectedBucket={bucket ?? selectedBucket?.name}
      buckets={buckets}
      pins={pins}
      scoped={scoped}
      loading={loading}
      failure={failure}
      callerRole={self?.role ?? null}
      onRefresh={refresh}
      onSelect={select}
      onCreate={create}
    />
  )
}

export function BucketPickerView({
  selectedBucket, buckets, pins, scoped, loading, failure, callerRole,
  onRefresh, onSelect, onCreate,
}: {
  selectedBucket?: string
  buckets: { name: string }[]
  pins: { bucket_name: string }[]
  scoped: boolean
  loading: boolean
  failure: string | null
  callerRole: Role | null
  onRefresh: () => Promise<unknown>
  onSelect: (name: string) => void
  onCreate: (name: string) => Promise<void>
}) {
  const createButton = (
    <CreateBucketButton callerRole={callerRole} onCreate={onCreate} variant="link" />
  )
  if (!scoped) {
    return (
      <PickerField label="Bucket">
        <Content component="p" style={{ margin: 0 }}>Choose a project first</Content>
      </PickerField>
    )
  }
  if (loading) {
    return (
      <PickerField label="Bucket">
        <Skeleton width="10rem" fontSize="lg" screenreaderText="Loading buckets…" />
      </PickerField>
    )
  }
  if (failure) {
    return (
      <PickerField label="Bucket">
        <Content component="p" style={{ margin: 0 }}>Buckets could not be loaded</Content>
      </PickerField>
    )
  }
  if (buckets.length === 0) {
    return (
      <PickerField label="Bucket">
        <Content component="p" style={{ margin: 0 }}>No buckets exist</Content>
        {createButton}
      </PickerField>
    )
  }

  const options = bucketPickerOptions(buckets, pins, selectedBucket != null)
  return (
    <PickerField label="Bucket">
      <TypeaheadPicker
        id="tenant-bucket"
        label="Bucket"
        options={options}
        selectedValue={selectedBucket}
        selectedLabel={selectedBucket ?? '—'}
        onSelect={onSelect}
        onOpen={() => refreshOnPickerOpen(true, onRefresh)}
        footer={<MenuFooter>{createButton}</MenuFooter>}
      />
    </PickerField>
  )
}

export function bucketPickerOptions(
  buckets: { name: string }[],
  pins: { bucket_name: string }[],
  hasSelection = false,
): PickerOption[] {
  const pinnedNames = new Set(pins.map((pin) => pin.bucket_name))
  const byName = new Map(buckets.map((listed) => [listed.name, listed]))
  const pinned = pins.flatMap((pin) => {
    const listed = byName.get(pin.bucket_name)
    return listed ? [listed] : []
  })
  const rest = buckets.filter((listed) => !pinnedNames.has(listed.name))
  return [
    // The blank row is the deliberate step back up to project standing
    // (duf-4qr, extended to buckets): offered exactly while a bucket is in
    // effect, so there is always a way to stand above it.
    ...(hasSelection ? [{ value: '', label: '\u2014' }] : []),
    ...pinned.map((listed) => ({ value: listed.name, label: listed.name, group: 'Pinned' })),
    ...rest.map((listed) => ({ value: listed.name, label: listed.name, group: 'Buckets' })),
  ]
}
