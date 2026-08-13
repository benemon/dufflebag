import { useState } from 'react'
import {
  Alert, Breadcrumb, BreadcrumbItem, Button, Card, CardBody, CardTitle, Checkbox,
  ClipboardCopyButton, CodeBlock, CodeBlockAction, CodeBlockCode, Content,
  DescriptionList, DescriptionListDescription, DescriptionListGroup,
  DescriptionListTerm, Form, FormGroup, FormSelect, FormSelectOption, Label, Modal,
  ModalBody, ModalFooter, ModalHeader, PageSection, Radio, TextArea, TextInput, Title,
} from '@patternfly/react-core'
import { ExpandableRowContent, Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'
import { useNavigate, useParams } from 'react-router'

import {
  assignChannelVersion, deleteVersion, revokeVersion, restoreVersion, signOutIfUnauthorized,
  type RevokeVersionOptions,
} from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { RoleRestrictedButton } from '../auth/RoleRestrictedButton'
import type { Role } from '../auth/permissions'
import { TypedConfirmModal } from '../components/TypedConfirmModal'
import { When } from '../components/When'
import {
  useVersion, useVersionFindings, type AncestryChild, type BucketChannel, type Build,
  type BuildState, type Version as VersionData, type VersionDetail,
} from '../data/versions'
import { VersionSecurityCard } from '../components/VersionSecurity'
import type { BuildFindings } from '../data/findings'
import { VersionStateLabel } from './Versions'
import type { TenancyGap } from '../data/tenant'
import { FacetRail, knownCount } from './RegistryFacets'

/**
 * Version — one version's lineage, operations and expandable build list.
 *
 * Components follow the design's version page (mockup 1a1, isVersion):
 *   Breadcrumb · Title · LabelGroup · DescriptionList · Card · Table
 * Publishers may promote a complete version from the operations card;
 * Terraform remains the automation path beside that console action.
 */
export function Version() {
  const { bucket = '', fingerprint = '' } = useParams()
  const navigate = useNavigate()
  const { data, loading, failure, gap, reload } = useVersion(bucket, fingerprint)
  const { state, self, selectedOrganization, selectedProject, signOut } = useAuth()
  const tenant = state && selectedOrganization && selectedProject
    ? { organizationID: selectedOrganization, projectID: selectedProject }
    : null
  const { data: findings } = useVersionFindings(
    bucket,
    fingerprint,
    (data?.version.builds ?? []).map((build) => ({
      id: build.id, platform: build.platform, component: build.component,
    })),
  )
  return (
    <VersionView
      bucket={bucket}
      detail={data}
      findings={findings}
      loading={loading}
      failure={failure}
      gap={gap}
      onBackToRegistry={() => navigate('/buckets')}
      onBackToBucket={() => navigate(`/buckets/${encodeURIComponent(bucket)}`)}
      onOpenBuild={(build) => navigate(
        `/buckets/${encodeURIComponent(bucket)}/versions/${encodeURIComponent(fingerprint)}` +
        `/builds/${encodeURIComponent(build)}`,
      )}
      onOpenVersion={(relatedBucket, relatedFingerprint) => navigate(
        `/buckets/${encodeURIComponent(relatedBucket)}/versions/${encodeURIComponent(relatedFingerprint)}`,
      )}
      callerRole={self?.role ?? null}
      onRevoke={async (options) => {
        if (!state || !tenant) throw new Error('No session.')
        try {
          await revokeVersion(state.token, tenant, bucket, fingerprint, options)
          reload()
        } catch (err: unknown) {
          signOutIfUnauthorized(err, signOut)
          throw err
        }
      }}
      onRestore={async () => {
        if (!state || !tenant) throw new Error('No session.')
        try {
          await restoreVersion(state.token, tenant, bucket, fingerprint)
          reload()
        } catch (err: unknown) {
          signOutIfUnauthorized(err, signOut)
          throw err
        }
      }}
      onDelete={async () => {
        if (!state || !tenant) throw new Error('No session.')
        try {
          await deleteVersion(state.token, tenant, bucket, fingerprint)
          navigate(`/buckets/${encodeURIComponent(bucket)}`)
        } catch (err: unknown) {
          signOutIfUnauthorized(err, signOut)
          throw err
        }
      }}
      onPromote={async (channel) => {
        if (!state || !tenant) throw new Error('No session.')
        try {
          await assignChannelVersion(state.token, tenant, bucket, channel, fingerprint)
          reload()
        } catch (err: unknown) {
          signOutIfUnauthorized(err, signOut)
          throw err
        }
      }}
    />
  )
}

export function VersionView({
  bucket,
  detail,
  version: suppliedVersion,
  findings = [],
  loading,
  failure,
  gap,
  onBackToRegistry,
  onBackToBucket,
  onOpenBuild = () => {},
  onOpenVersion = () => {},
  callerRole = null,
  onRevoke = () => Promise.reject(new Error('No session.')),
  onRestore = () => Promise.reject(new Error('No session.')),
  onDelete = () => Promise.reject(new Error('No session.')),
  onPromote = () => Promise.reject(new Error('No session.')),
}: {
  bucket: string
  detail?: VersionDetail | null
  /** Kept for focused view tests that do not need the ancestry fetch. */
  version?: VersionData | null
  /** Per-build inventories, fetched by the container like every other read. */
  findings?: BuildFindings[]
  loading: boolean
  failure: string | null
  /** A platform session with no tenancy chosen yet — stated, never fetched around. */
  gap?: TenancyGap | null
  onBackToRegistry: () => void
  onBackToBucket: () => void
  onOpenBuild?: (build: string) => void
  onOpenVersion?: (bucket: string, fingerprint: string) => void
  callerRole?: Role | null
  onRevoke?: (options: RevokeVersionOptions) => Promise<void>
  onRestore?: () => Promise<void>
  onDelete?: () => Promise<void>
  onPromote?: (channel: string) => Promise<void>
}) {
  const version = detail?.version ?? suppliedVersion ?? null
  const [facet, setFacet] = useState<'overview' | 'builds'>('overview')
  const [action, setAction] = useState<'revoke' | 'restore' | 'delete' | null>(null)
  const restores = version?.state === 'revoked' || version?.state === 'revocation-scheduled'
  return (
    <>
      <PageSection variant="default">
        <Breadcrumb>
          <BreadcrumbItem component="button" onClick={onBackToRegistry}>
            Registry
          </BreadcrumbItem>
          <BreadcrumbItem component="button" onClick={onBackToBucket}>
            {bucket}
          </BreadcrumbItem>
          <BreadcrumbItem isActive>{version?.name ?? '…'}</BreadcrumbItem>
        </Breadcrumb>
        {version && (
          <>
            <div style={{ display: 'flex', justifyContent: 'space-between', gap: 24 }}>
              <Title headingLevel="h1" size="2xl">
                <span style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
                  {version.name}
                  <VersionStateLabel state={version.state} />
                  {version.channels.map((channel) => (
                    <Label key={channel} isCompact>{channel}</Label>
                  ))}
                  {version.templateType && <Label isCompact>{version.templateType}</Label>}
                </span>
              </Title>
              <div style={{ display: 'flex', alignItems: 'flex-start', gap: 8 }}>
                {restores ? (
                  <div>
                    <RoleRestrictedButton
                      action="revokeVersions"
                      callerRole={callerRole}
                      variant="secondary"
                      onClick={() => setAction('restore')}
                    >
                      Restore
                    </RoleRestrictedButton>
                    {version.state === 'revocation-scheduled' ? (
                      <Content component="p" style={{ marginTop: 4 }}>
                        Restoring cancels the scheduled revocation.
                      </Content>
                    ) : null}
                  </div>
                ) : (
                  <RoleRestrictedButton
                    action="revokeVersions"
                    callerRole={callerRole}
                    variant="danger"
                    onClick={() => setAction('revoke')}
                  >
                    Revoke
                  </RoleRestrictedButton>
                )}
                <RoleRestrictedButton
                  action="deleteVersions"
                  callerRole={callerRole}
                  variant="danger"
                  onClick={() => setAction('delete')}
                >
                  Delete version
                </RoleRestrictedButton>
              </div>
            </div>
            <DescriptionList isHorizontal style={{ marginTop: 16 }}>
              <DescriptionListGroup>
                <DescriptionListTerm>Fingerprint</DescriptionListTerm>
                <DescriptionListDescription>
                  <code className="registry-fingerprint">{version.fingerprint}</code>
                </DescriptionListDescription>
              </DescriptionListGroup>
              <DescriptionListGroup>
                <DescriptionListTerm>Created</DescriptionListTerm>
                <DescriptionListDescription><When iso={version.created} /></DescriptionListDescription>
              </DescriptionListGroup>
            </DescriptionList>
          </>
        )}
      </PageSection>

      {/* The rail sits flush against the header and left edge; only the facet
          content carries the grey well's padding. Alert states have no rail,
          so they keep the section padding. */}
      <PageSection
        variant="secondary"
        isFilled
        hasBodyWrapper={false}
        padding={{ default: !loading && !failure && !gap && version ? 'noPadding' : 'padding' }}
      >
        {loading ? (
          <Content component="p">Loading version…</Content>
        ) : failure ? (
          <Alert variant="danger" isInline title="Version could not be loaded">
            <Content component="p">{failure}</Content>
          </Alert>
        ) : gap ? (
          <Alert variant="info" isInline title={gap.title}>
            <Content component="p">{gap.detail}</Content>
          </Alert>
        ) : version ? (
          <FacetRail
            active={facet}
            onSelect={setFacet}
            heading="This version"
            label="Version facets"
            facets={[
              {
                key: 'overview', label: 'Overview',
                content: (
                  <VersionOverview
                    bucket={bucket}
                    version={version}
                    detail={detail ?? null}
                    findings={findings}
                    callerRole={callerRole}
                    onOpenBuild={onOpenBuild}
                    onOpenVersion={onOpenVersion}
                    onPromote={onPromote}
                  />
                ),
              },
              {
                key: 'builds', label: 'Builds', count: knownCount(version.builds.length),
                content: (
                  <Card style={{ flex: '1 1 520px' }}>
                    <CardTitle>Builds</CardTitle>
                    <CardBody>
                      {version.builds.length === 0 ? (
                        <Content component="p">No builds have been reported for this version.</Content>
                      ) : (
                        <BuildTable builds={version.builds} onOpenBuild={onOpenBuild} />
                      )}
                    </CardBody>
                  </Card>
                ),
              },
            ]}
          />
        ) : null}
      </PageSection>
      {version && action === 'revoke' ? (
        <RevokeModal
          bucket={bucket}
          version={version}
          callerRole={callerRole}
          onConfirm={onRevoke}
          onClose={() => setAction(null)}
        />
      ) : null}
      {version && action === 'restore' ? (
        <RestoreModal
          bucket={bucket}
          version={version}
          callerRole={callerRole}
          onConfirm={onRestore}
          onClose={() => setAction(null)}
        />
      ) : null}
      {version && action === 'delete' ? (
        <DeleteVersionModal
          bucket={bucket}
          version={version}
          callerRole={callerRole}
          onConfirm={onDelete}
          onClose={() => setAction(null)}
        />
      ) : null}
    </>
  )
}

function DeleteVersionModal({
  bucket, version, callerRole, onConfirm, onClose,
}: {
  bucket: string
  version: VersionData
  callerRole: Role | null
  onConfirm: () => Promise<void>
  onClose: () => void
}) {
  const [submitting, setSubmitting] = useState(false)
  const [failure, setFailure] = useState<string | null>(null)
  const confirm = async () => {
    setSubmitting(true)
    setFailure(null)
    try {
      await onConfirm()
      onClose()
    } catch (err: unknown) {
      setFailure(err instanceof Error ? err.message : 'The action failed.')
    } finally {
      setSubmitting(false)
    }
  }
  return <DeleteVersionModalView
    bucket={bucket}
    version={version}
    callerRole={callerRole}
    submitting={submitting}
    failure={failure}
    onConfirm={confirm}
    onClose={onClose}
  />
}

export function DeleteVersionModalView({
  bucket, version, submitting, failure, onConfirm, onClose,
}: {
  bucket: string
  version: VersionData
  callerRole: Role | null
  submitting: boolean
  failure: string | null
  onConfirm: () => Promise<void>
  onClose: () => void
}) {
  return (
    <TypedConfirmModal
      title={`Delete ${bucket} ${version.name}`}
      expected={version.name}
      verb="Delete version"
      busy={submitting}
      onConfirm={onConfirm}
      onCancel={onClose}
      body={<>
        {failure ? (
          <Alert variant="danger" isInline title="The action was refused">
            <Content component="p">{failure}</Content>
          </Alert>
        ) : null}
        <Content component="p">
          Deleting {bucket} {version.name} is permanent. Its builds, artifacts and SBOMs
          are deleted with it. Channels must be unassigned from this version first.
        </Content>
      </>}
    />
  )
}

type RevokeWhen = 'now' | 'scheduled'

function RevokeModal({
  bucket, version, callerRole, onConfirm, onClose,
}: {
  bucket: string
  version: VersionData
  callerRole: Role | null
  onConfirm: (options: RevokeVersionOptions) => Promise<void>
  onClose: () => void
}) {
  const [message, setMessage] = useState('')
  const [when, setWhen] = useState<RevokeWhen>('now')
  const [scheduledAt, setScheduledAt] = useState('')
  const [skipDescendants, setSkipDescendants] = useState(false)
  const [disableRollback, setDisableRollback] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [failure, setFailure] = useState<string | null>(null)

  const confirm = async (options: RevokeVersionOptions) => {
    setSubmitting(true)
    setFailure(null)
    try {
      await onConfirm(options)
      onClose()
    } catch (err: unknown) {
      setFailure(err instanceof Error ? err.message : 'The action failed.')
    } finally {
      setSubmitting(false)
    }
  }

  return <RevokeModalView
    bucket={bucket}
    version={version}
    callerRole={callerRole}
    message={message}
    when={when}
    scheduledAt={scheduledAt}
    skipDescendants={skipDescendants}
    disableRollback={disableRollback}
    submitting={submitting}
    failure={failure}
    onMessageChange={setMessage}
    onWhenChange={setWhen}
    onScheduledAtChange={setScheduledAt}
    onSkipDescendantsChange={setSkipDescendants}
    onDisableRollbackChange={setDisableRollback}
    onConfirm={confirm}
    onClose={onClose}
  />
}

export function RevokeModalView({
  bucket, version, message, when, scheduledAt, skipDescendants,
  disableRollback, submitting, failure, onMessageChange, onWhenChange,
  onScheduledAtChange, onSkipDescendantsChange, onDisableRollbackChange,
  onConfirm, onClose,
}: {
  bucket: string
  version: VersionData
  callerRole: Role | null
  message: string
  when: RevokeWhen
  scheduledAt: string
  skipDescendants: boolean
  disableRollback: boolean
  submitting: boolean
  failure: string | null
  onMessageChange: (message: string) => void
  onWhenChange: (when: RevokeWhen) => void
  onScheduledAtChange: (scheduledAt: string) => void
  onSkipDescendantsChange: (checked: boolean) => void
  onDisableRollbackChange: (checked: boolean) => void
  onConfirm: (options: RevokeVersionOptions) => Promise<void>
  onClose: () => void
}) {
  const parsedSchedule = scheduledAt === '' ? null : new Date(scheduledAt)
  const scheduleMissing = when === 'scheduled' && (
    parsedSchedule === null || Number.isNaN(parsedSchedule.getTime())
  )
  const schedulePast = when === 'scheduled' && !scheduleMissing &&
    parsedSchedule!.getTime() <= Date.now()
  const scheduleFailure = scheduleMissing
    ? 'Choose a scheduled time.'
    : schedulePast
      ? 'Scheduled time must be in the future.'
      : null
  const idPrefix = `revoke-${version.fingerprint}`

  return (
    <TypedConfirmModal
      title={`Revoke ${bucket} ${version.name}`}
      expected={version.name}
      verb={`Revoke ${bucket} ${version.name}`}
      busy={submitting}
      confirmDisabled={scheduleFailure !== null}
      onCancel={onClose}
      onConfirm={() => onConfirm({
        revoke_at: when === 'now' ? new Date().toISOString() : parsedSchedule!.toISOString(),
        ...(message.trim() ? { revocation_message: message.trim() } : {}),
        ...(skipDescendants ? { skip_descendants_revocation: true } : {}),
        ...(disableRollback ? { disable_rollback_channels: true } : {}),
      })}
      body={<>
        {failure ? (
          <Alert variant="danger" isInline title="The action was refused">
            <Content component="p">{failure}</Content>
          </Alert>
        ) : null}
        <Form>
          <FormGroup label="Revocation message" fieldId={`${idPrefix}-message`}>
            <TextArea
              id={`${idPrefix}-message`}
              value={message}
              resizeOrientation="vertical"
              onChange={(_event, value) => onMessageChange(value)}
            />
          </FormGroup>
          <FormGroup label="When" fieldId={`${idPrefix}-when`} role="radiogroup">
            <Radio
              id={`${idPrefix}-now`}
              name={`${idPrefix}-when`}
              label="Now"
              isChecked={when === 'now'}
              onChange={() => onWhenChange('now')}
            />
            <Radio
              id={`${idPrefix}-scheduled`}
              name={`${idPrefix}-when`}
              label="At a scheduled time"
              isChecked={when === 'scheduled'}
              onChange={() => onWhenChange('scheduled')}
            />
          </FormGroup>
          {when === 'scheduled' ? (
            <FormGroup label="Scheduled time" isRequired fieldId={`${idPrefix}-scheduled-at`}>
              <TextInput
                id={`${idPrefix}-scheduled-at`}
                type="datetime-local"
                value={scheduledAt}
                validated={scheduleFailure ? 'error' : 'default'}
                aria-invalid={scheduleFailure ? 'true' : undefined}
                aria-describedby={scheduleFailure ? `${idPrefix}-scheduled-at-error` : undefined}
                onChange={(_event, value) => onScheduledAtChange(value)}
              />
              {scheduleFailure ? (
                <Content component="p" id={`${idPrefix}-scheduled-at-error`}>
                  {scheduleFailure}
                </Content>
              ) : null}
            </FormGroup>
          ) : null}
          <Checkbox
            id={`${idPrefix}-skip-descendants`}
            label="Skip descendant revocation"
            description="Leave descendant versions active."
            isChecked={skipDescendants}
            onChange={(_event, checked) => onSkipDescendantsChange(checked)}
          />
          <Checkbox
            id={`${idPrefix}-disable-rollback`}
            label="Do not roll channels back"
            description="Leave channel assignments unchanged."
            isChecked={disableRollback}
            onChange={(_event, checked) => onDisableRollbackChange(checked)}
          />
        </Form>
      </>}
    />
  )
}

function RestoreModal({
  bucket, version, callerRole, onConfirm, onClose,
}: {
  bucket: string
  version: VersionData
  callerRole: Role | null
  onConfirm: () => Promise<void>
  onClose: () => void
}) {
  const [submitting, setSubmitting] = useState(false)
  const [failure, setFailure] = useState<string | null>(null)

  const confirm = async () => {
    setSubmitting(true)
    setFailure(null)
    try {
      await onConfirm()
      onClose()
    } catch (err: unknown) {
      setFailure(err instanceof Error ? err.message : 'The action failed.')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Modal aria-labelledby="restore-version-modal-title" isOpen onClose={onClose} variant="small">
      <RestoreModalView
        bucket={bucket}
        version={version}
        callerRole={callerRole}
        submitting={submitting}
        failure={failure}
        onConfirm={confirm}
        onClose={onClose}
      />
    </Modal>
  )
}

export function RestoreModalView({
  bucket, version, callerRole, submitting, failure, onConfirm, onClose,
}: {
  bucket: string
  version: VersionData
  callerRole: Role | null
  submitting: boolean
  failure: string | null
  onConfirm: () => Promise<void>
  onClose: () => void
}) {
  return (
    <>
      <ModalHeader labelId="restore-version-modal-title" title={`Restore ${bucket} ${version.name}`} />
      <ModalBody>
        {failure ? (
          <Alert variant="danger" isInline title="The action was refused">
            <Content component="p">{failure}</Content>
          </Alert>
        ) : null}
        <Content component="p">
          This clears the revocation and inherited descendant revocations; manual descendant
          revocations remain, and channel assignments are not reassigned.
        </Content>
      </ModalBody>
      <ModalFooter>
        <RoleRestrictedButton
          action="revokeVersions"
          callerRole={callerRole}
          variant="primary"
          isLoading={submitting}
          isDisabled={submitting}
          onClick={onConfirm}
        >
          Restore {bucket} {version.name}
        </RoleRestrictedButton>
        <Button variant="link" isDisabled={submitting} onClick={onClose}>Cancel</Button>
      </ModalFooter>
    </>
  )
}

function VersionOverview({
  bucket,
  version,
  detail,
  findings,
  callerRole,
  onOpenBuild,
  onOpenVersion,
  onPromote,
}: {
  bucket: string
  version: VersionData
  detail: VersionDetail | null
  findings: BuildFindings[]
  callerRole: Role | null
  onOpenBuild: (build: string) => void
  onOpenVersion: (bucket: string, fingerprint: string) => void
  onPromote: (channel: string) => Promise<void>
}) {
  return (
    <div style={{ display: 'flex', gap: 24, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <div style={{ flex: '1 1 520px', display: 'flex', flexDirection: 'column', gap: 24 }}>
        {detail && findings.length > 0 && (
          <VersionSecurityCard
            builds={findings}
            onOpenBuild={onOpenBuild}
            // The channels this version actually carries, so a version a
            // channel selects is never reported as unmaintained.
            outOfScanSet={version.channels.length === 0}
          />
        )}
        {detail && <LineageCard version={version} onOpenVersion={onOpenVersion} />}
      </div>
      {detail && (
        <div style={{ flex: '0 1 440px', display: 'flex', flexDirection: 'column', gap: 24 }}>
          <ConsumeCard bucket={bucket} version={version} />
          <OperationsCard
            bucket={bucket}
            version={version}
            channels={detail.channels}
            callerRole={callerRole}
            onPromote={onPromote}
          />
        </div>
      )}
    </div>
  )
}

function LineageCard({
  version,
  onOpenVersion,
}: {
  version: VersionData
  onOpenVersion: (bucket: string, fingerprint: string) => void
}) {
  return (
    <Card>
      <CardTitle>Lineage</CardTitle>
      <CardBody style={{ display: 'flex', gap: 32, flexWrap: 'wrap' }}>
        <LineageSide heading="Parents" entries={version.parents ?? []} onOpenVersion={onOpenVersion} />
        <LineageSide heading="Children" entries={version.children ?? []} onOpenVersion={onOpenVersion} />
      </CardBody>
    </Card>
  )
}

/**
 * One side of the lineage card. Entries read "bucket vN" and link to that
 * version's screen, which carries the fingerprint a reader would copy.
 */
function LineageSide({
  heading,
  entries,
  onOpenVersion,
}: {
  heading: string
  entries: AncestryChild[]
  onOpenVersion: (bucket: string, fingerprint: string) => void
}) {
  return (
    <div style={{ flex: '1 1 220px' }}>
      <Content component="h3" style={subheadingStyle}>{heading}</Content>
      {entries.length === 0 ? <Content component="p">None.</Content> :
        entries.map((entry) => (
          <div key={`${entry.bucket}/${entry.fingerprint}`} style={{ padding: '5px 0' }}>
            <Button variant="link" isInline onClick={() => onOpenVersion(entry.bucket, entry.fingerprint)}>
              {entry.bucket} {entry.versionName}
            </Button>
            {(entry.channels ?? []).length > 0 && (
              <div style={{ color: '#4d4d4d', fontSize: 13 }}>{(entry.channels ?? []).join(', ')}</div>
            )}
          </div>
        ))}
    </div>
  )
}

const subheadingStyle = {
  color: '#4d4d4d', fontSize: 12, letterSpacing: '.04em', textTransform: 'uppercase' as const,
}

function ConsumeCard({ bucket, version }: { bucket: string; version: VersionData }) {
  const snippet = terraformConsumeSnippet(bucket, version)
  return (
    <Card>
      <CardTitle>Consume this version</CardTitle>
      <CardBody>
        {snippet ? (
          <TerraformCode snippet={snippet} label="Copy consume configuration" />
        ) : (
          <Alert variant="info" isInline title="No Terraform lookup can identify this version">
            <Content component="p">
              <code>hcp_packer_version</code> resolves a channel, and no channel currently points
              at this version. An artifact block appears once an artifact has been reported.
            </Content>
          </Alert>
        )}
        {version.channels.length > 0 && (
          <Content component="p">
            The version lookup follows {version.channels[0]}. The artifact lookup pins the exact
            fingerprint shown above.
          </Content>
        )}
      </CardBody>
    </Card>
  )
}

export function OperationsCard({
  bucket,
  version,
  channels,
  callerRole,
  onPromote,
}: {
  bucket: string
  version: VersionData
  channels: BucketChannel[]
  callerRole: Role | null
  onPromote: (channel: string) => Promise<void>
}) {
  const promotable = channels.filter((channel) => !channel.managed)
  const [channel, setChannel] = useState(promotable[0]?.name ?? '')
  const [submitting, setSubmitting] = useState(false)
  const [failure, setFailure] = useState<string | null>(null)
  if (promotable.length === 0) return null
  const promote = async () => {
    setSubmitting(true)
    setFailure(null)
    try {
      await onPromote(channel)
    } catch (err: unknown) {
      setFailure(err instanceof Error ? err.message : 'The action failed.')
    } finally {
      setSubmitting(false)
    }
  }
  return (
    <Card>
      <CardTitle>Operations</CardTitle>
      <CardBody>
        {version.state === 'complete' ? (
          <Form>
            {failure ? (
              <Alert variant="danger" isInline title="The action was refused">
                <Content component="p">{failure}</Content>
              </Alert>
            ) : null}
            <FormGroup label="Promote to channel" fieldId="promotion-channel">
              <FormSelect
                id="promotion-channel"
                value={channel}
                onChange={(_event, value) => setChannel(value)}
              >
                {promotable.map((option) => (
                  <FormSelectOption key={option.name} value={option.name} label={option.name} />
                ))}
              </FormSelect>
            </FormGroup>
            <RoleRestrictedButton
              action="manageChannels"
              callerRole={callerRole}
              variant="primary"
              isLoading={submitting}
              isDisabled={submitting || channel === ''}
              onClick={() => void promote()}
            >
              Promote
            </RoleRestrictedButton>
          </Form>
        ) : null}
        {promotable.map((channel) => (
          <div key={channel.name} style={{ marginTop: 16 }}>
            <Content component="p">
              {channel.fingerprint === version.fingerprint ? 'Keep' : 'Promote'} {version.name} on{' '}
              <strong>{channel.name}</strong>
            </Content>
            <TerraformCode
              snippet={terraformPromotionSnippet(bucket, channel.name, version.fingerprint)}
              label={`Copy ${channel.name} assignment`}
            />
          </div>
        ))}
      </CardBody>
    </Card>
  )
}

function TerraformCode({ snippet, label }: { snippet: string; label: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <CodeBlock
      actions={(
        <CodeBlockAction>
          <ClipboardCopyButton
            id={`copy-${label.replace(/\s+/g, '-').toLowerCase()}`}
            variant="plain"
            aria-label={label}
            onClick={() => {
              void navigator.clipboard.writeText(snippet)
              setCopied(true)
            }}
            exitDelay={copied ? 1500 : 600}
            onTooltipHidden={() => setCopied(false)}
          >
            {copied ? 'Copied' : 'Copy'}
          </ClipboardCopyButton>
        </CodeBlockAction>
      )}
    >
      <CodeBlockCode>{snippet}</CodeBlockCode>
    </CodeBlock>
  )
}

export function terraformConsumeSnippet(bucket: string, version: VersionData): string | null {
  const channel = version.channels[0]
  const artifact = version.builds.flatMap((build) =>
    build.artifacts.map((item) => ({ ...item, platform: build.platform })),
  )[0]
  if (!channel && !artifact) return null

  const label = terraformLabel(bucket)
  const blocks: string[] = []
  if (channel) {
    blocks.push(`data "hcp_packer_version" "${label}" {\n` +
      `  bucket_name  = ${JSON.stringify(bucket)}\n` +
      `  channel_name = ${JSON.stringify(channel)}\n` +
      '}')
  }
  if (artifact) {
    blocks.push(`data "hcp_packer_artifact" "${label}" {\n` +
      `  bucket_name         = ${JSON.stringify(bucket)}\n` +
      `  version_fingerprint = ${JSON.stringify(version.fingerprint)}\n` +
      `  platform            = ${JSON.stringify(artifact.platform)}\n` +
      `  region              = ${JSON.stringify(artifact.region)}\n` +
      '}')
  }
  return blocks.join('\n\n')
}

export function terraformPromotionSnippet(
  bucket: string,
  channel: string,
  fingerprint: string,
): string {
  return `resource "hcp_packer_channel_assignment" "${terraformLabel(channel)}" {\n` +
    `  bucket_name         = ${JSON.stringify(bucket)}\n` +
    `  channel_name        = ${JSON.stringify(channel)}\n` +
    `  version_fingerprint = ${JSON.stringify(fingerprint)}\n` +
    '}'
}

function terraformLabel(value: string): string {
  const label = value.toLowerCase().replace(/[^a-z0-9_]/g, '_') || 'version'
  return /^[0-9]/.test(label) ? `v_${label}` : label
}

function BuildTable({ builds, onOpenBuild }: { builds: Build[]; onOpenBuild: (id: string) => void }) {
  const [expanded, setExpanded] = useState<string | null>(null)
  return (
    <Table aria-label="Builds" variant="compact">
      <Thead>
        <Tr>
          <Th screenReaderText="Row expansion" />
          <Th>Name</Th>
          <Th>Status</Th>
          <Th>Packer runner OS</Th>
          <Th>Arch</Th>
          <Th>Updated</Th>
        </Tr>
      </Thead>
      {builds.map((build, index) => (
        <Tbody key={build.id} isExpanded={expanded === build.id}>
          <Tr>
            <Td
              expand={{
                rowIndex: index,
                isExpanded: expanded === build.id,
                onToggle: () => setExpanded(expanded === build.id ? null : build.id),
                expandId: `build-${build.id}`,
              }}
            />
            <Td dataLabel="Name">
              <Button variant="link" isInline onClick={() => onOpenBuild(build.id)}>
                {build.component || '—'}
              </Button>
            </Td>
            <Td dataLabel="Status"><BuildStateLabel state={build.state} /></Td>
            <Td dataLabel="Packer runner OS">{build.runnerOS || '—'}</Td>
            <Td dataLabel="Arch">{build.arch || '—'}</Td>
            <Td dataLabel="Updated"><When iso={build.updated} /></Td>
          </Tr>
          <Tr isExpanded={expanded === build.id}>
            <Td />
            <Td dataLabel="Build detail" colSpan={5}>
              <ExpandableRowContent>
                <DescriptionList isHorizontal isCompact>
                  <DescriptionListGroup>
                    <DescriptionListTerm>Packer</DescriptionListTerm>
                    <DescriptionListDescription>{build.packerVersion || '—'}</DescriptionListDescription>
                  </DescriptionListGroup>
                  <DescriptionListGroup>
                    <DescriptionListTerm>Plugin</DescriptionListTerm>
                    <DescriptionListDescription>{pluginSummary(build)}</DescriptionListDescription>
                  </DescriptionListGroup>
                  <DescriptionListGroup>
                    <DescriptionListTerm>Artifacts</DescriptionListTerm>
                    <DescriptionListDescription>
                      {countLabel(build.artifacts.length, 'artifact')}
                    </DescriptionListDescription>
                  </DescriptionListGroup>
                  <DescriptionListGroup>
                    <DescriptionListTerm>Packages</DescriptionListTerm>
                    <DescriptionListDescription>{packageSummary(build)}</DescriptionListDescription>
                  </DescriptionListGroup>
                  <DescriptionListGroup>
                    <DescriptionListTerm>Run UUID</DescriptionListTerm>
                    <DescriptionListDescription><code>{build.packerRunUUID || '—'}</code></DescriptionListDescription>
                  </DescriptionListGroup>
                </DescriptionList>
                <Button variant="link" isInline onClick={() => onOpenBuild(build.id)}>
                  Open {build.component || 'build'} →
                </Button>
              </ExpandableRowContent>
            </Td>
          </Tr>
        </Tbody>
      ))}
    </Table>
  )
}

export function BuildStateLabel({ state }: { state: BuildState }) {
  switch (state) {
    case 'done':
      return <Label isCompact color="green">done</Label>
    case 'running':
      return <Label isCompact color="blue">running</Label>
    case 'failed':
      return <Label isCompact color="red">failed</Label>
    case 'cancelled':
      return <Label isCompact color="orange">cancelled</Label>
    case 'pending':
      return <Label isCompact color="grey">pending</Label>
  }
}

export function pluginSummary(build: Build): string {
  if (build.plugins.length === 0) return '—'
  return build.plugins
    .map((plugin) => [plugin.name, plugin.version].filter(Boolean).join(' '))
    .filter(Boolean)
    .join(', ') || '—'
}

export function packageSummary(build: Build): string {
  switch (build.packageInventory.status) {
    case 'parsed':
      return countLabel(build.packageInventory.packages.length, 'package')
    case 'unparseable':
      return 'SBOM unparseable'
    case 'not-loaded':
      return '—'
  }
}

function countLabel(count: number, singular: string): string {
  return `${count} ${singular}${count === 1 ? '' : 's'}`
}
