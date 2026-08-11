import { useCallback, useEffect, useState } from 'react'
import {
  ActionGroup, Alert, Button, Card, CardBody, CardTitle, Content,
  DescriptionList, DescriptionListDescription, DescriptionListGroup, DescriptionListTerm,
  DualListSelector, DualListSelectorControl, DualListSelectorControlsWrapper,
  DualListSelectorList, DualListSelectorListItem, DualListSelectorPane,
  Form, FormGroup, FormSelect, FormSelectOption, Label, PageSection, TextArea,
  TextInput, Title, Tooltip,
} from '@patternfly/react-core'
import AngleLeftIcon from '@patternfly/react-icons/dist/esm/icons/angle-left-icon'
import AngleRightIcon from '@patternfly/react-icons/dist/esm/icons/angle-right-icon'
import { ExpandableRowContent, Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'

import {
  ApiError, deleteBagDropAssociation, deleteBagDropConfig, disableBagDrop, enableBagDrop,
  getBagDropConfig, getBagDropStatus, listBagDropAssociations, listBuckets,
  putBagDropConfig, reconcileBagDrop, setBagDropAssociation, verifyBagDrop,
  type ApiBagDropAssociation, type ApiBagDropConfig, type ApiBagDropConfigWrite,
  type ApiBagDropEnableResult, type ApiBagDropStatus, type ApiBagDropVerificationResult,
  type ApiBucket,
} from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { permitsAction, type Role } from '../auth/permissions'

type DestinationDraft = {
  adapter: 'hcp-packer' | 'dufflebag'
  endpoint: string
  caChain: string
  organizationID: string
  projectID: string
  clientID: string
  clientSecret: string
}

const emptyDraft = (): DestinationDraft => ({
  adapter: 'hcp-packer', endpoint: '', caChain: '', organizationID: '', projectID: '',
  clientID: '', clientSecret: '',
})

export function draftForBagDropConfig(config: ApiBagDropConfig | null): DestinationDraft {
  if (!config) return emptyDraft()
  if (config.adapter === 'dufflebag' && config.dufflebag) {
    return {
      adapter: 'dufflebag', endpoint: config.dufflebag.endpoint,
      caChain: config.dufflebag.ca_chain ?? '',
      organizationID: config.dufflebag.organization_id,
      projectID: config.dufflebag.project_id, clientID: config.dufflebag.client_id,
      clientSecret: '',
    }
  }
  return {
    ...emptyDraft(), adapter: 'hcp-packer',
    organizationID: config.hcp_packer?.organization_id ?? '',
    projectID: config.hcp_packer?.project_id ?? '',
    clientID: config.hcp_packer?.client_id ?? '',
  }
}

export function bagDropWrite(draft: DestinationDraft): ApiBagDropConfigWrite {
  const secret = draft.clientSecret.trim()
  if (draft.adapter === 'dufflebag') {
    return {
      adapter: 'dufflebag',
      dufflebag: {
        endpoint: draft.endpoint.trim(),
        ...(draft.caChain.trim() === '' ? {} : { ca_chain: draft.caChain }),
        organization_id: draft.organizationID.trim(), project_id: draft.projectID.trim(),
        client_id: draft.clientID.trim(), ...(secret === '' ? {} : { client_secret: secret }),
      },
    }
  }
  return {
    adapter: 'hcp-packer',
    hcp_packer: {
      organization_id: draft.organizationID.trim(), project_id: draft.projectID.trim(),
      client_id: draft.clientID.trim(), ...(secret === '' ? {} : { client_secret: secret }),
    },
  }
}

function draftIsValid(draft: DestinationDraft, config: ApiBagDropConfig | null): boolean {
  if (!config?.secret_set && draft.clientSecret.trim() === '') return false
  if ([draft.organizationID, draft.projectID, draft.clientID].some((value) => value.trim() === '')) {
    return false
  }
  return draft.adapter !== 'dufflebag' || draft.endpoint.trim() !== ''
}

export function BagDrop() {
  const { state, self, selectedOrganization, selectedProject } = useAuth()
  const organizationID = selectedOrganization ?? state?.claims.organizationID ?? null
  const projectID = selectedProject ?? state?.claims.projectID ?? null
  const token = state?.token ?? ''
  const callerRole = self?.role ?? null
  const canConfigure = permitsAction(callerRole, 'configureBagDrop')
  const tenant = organizationID && projectID ? { organizationID, projectID } : null

  const [config, setConfig] = useState<ApiBagDropConfig | null>(null)
  const [configLoading, setConfigLoading] = useState(canConfigure)
  const [configFailure, setConfigFailure] = useState<string | null>(null)
  const [buckets, setBuckets] = useState<ApiBucket[]>([])
  const [associations, setAssociations] = useState<ApiBagDropAssociation[]>([])
  const [associationsLoading, setAssociationsLoading] = useState(canConfigure)
  const [associationsFailure, setAssociationsFailure] = useState<string | null>(null)
  const [status, setStatus] = useState<ApiBagDropStatus | null>(null)
  const [statusLoading, setStatusLoading] = useState(true)
  const [statusFailure, setStatusFailure] = useState<string | null>(null)
  const [pollAfterReconcile, setPollAfterReconcile] = useState(false)

  const loadStatus = useCallback(async (quiet = false): Promise<ApiBagDropStatus | null> => {
    if (!tenant || token === '') return null
    if (!quiet) setStatusLoading(true)
    setStatusFailure(null)
    try {
      const loaded = await getBagDropStatus(token, tenant)
      setStatus(loaded)
      return loaded
    } catch (err: unknown) {
      setStatusFailure(messageFor(err, 'Bag Drop status could not be loaded.'))
      return null
    } finally {
      if (!quiet) setStatusLoading(false)
    }
  }, [tenant?.organizationID, tenant?.projectID, token])

  const loadMaintainer = useCallback(async () => {
    if (!tenant || token === '' || !canConfigure) return
    setConfigLoading(true)
    setAssociationsLoading(true)
    setConfigFailure(null)
    setAssociationsFailure(null)
    let loadedConfig: ApiBagDropConfig | null = null
    try {
      loadedConfig = await getBagDropConfig(token, tenant)
      setConfig(loadedConfig)
    } catch (err: unknown) {
      if (err instanceof ApiError && err.status === 404) {
        setConfig(null)
      } else {
        setConfigFailure(messageFor(err, 'Bag Drop configuration could not be loaded.'))
      }
    } finally {
      setConfigLoading(false)
    }
    try {
      const [loadedBuckets, loadedAssociations] = await Promise.all([
        listBuckets(token, tenant),
        loadedConfig ? listBagDropAssociations(token, tenant) : Promise.resolve([]),
      ])
      setBuckets(loadedBuckets)
      setAssociations(loadedAssociations)
    } catch (err: unknown) {
      setAssociationsFailure(messageFor(err, 'Mirrored buckets could not be loaded.'))
    } finally {
      setAssociationsLoading(false)
    }
  }, [canConfigure, tenant?.organizationID, tenant?.projectID, token])

  useEffect(() => {
    setConfig(null)
    setBuckets([])
    setAssociations([])
    setStatus(null)
    setPollAfterReconcile(false)
    if (!tenant || token === '') return
    void loadStatus()
    if (canConfigure) void loadMaintainer()
  }, [canConfigure, loadMaintainer, loadStatus, tenant?.organizationID, tenant?.projectID, token])

  const transient = status?.associations.some(
    (association) => association.sync_status === 'pending' || association.sync_status === 'removing',
  ) ?? false
  useEffect(() => {
    if (!transient && !pollAfterReconcile) return
    const timer = window.setInterval(() => {
      void loadStatus(true).then((loaded) => {
        if (pollAfterReconcile && loaded && !loaded.associations.some(
          (association) => association.sync_status === 'pending' || association.sync_status === 'removing',
        )) setPollAfterReconcile(false)
      })
    }, 10_000)
    return () => window.clearInterval(timer)
  }, [loadStatus, pollAfterReconcile, transient])

  if (!tenant) {
    return (
      <PageSection><Alert variant="info" isInline title="Select a project to manage Bag Drop" /></PageSection>
    )
  }

  const reloadAssociations = async () => {
    setAssociations(await listBagDropAssociations(token, tenant))
    await loadStatus(true)
  }

  return (
    <BagDropView
      callerRole={callerRole} canConfigure={canConfigure}
      config={config} configLoading={configLoading} configFailure={configFailure}
      onSave={async (write) => {
        const stored = await putBagDropConfig(token, tenant, write)
        setConfig(stored)
        await loadMaintainer()
        await loadStatus(true)
        return stored
      }}
      onVerify={async () => {
        const result = await verifyBagDrop(token, tenant)
        await loadMaintainer()
        await loadStatus(true)
        return result
      }}
      onEnable={async () => {
        const result = await enableBagDrop(token, tenant)
        if (result.kind === 'enabled') {
          setConfig(result.config)
          await loadStatus(true)
        }
        return result
      }}
      onDisable={async () => {
        const stored = await disableBagDrop(token, tenant)
        setConfig(stored)
        await loadStatus(true)
        return stored
      }}
      onDelete={async () => {
        await deleteBagDropConfig(token, tenant)
        setConfig(null)
        setAssociations([])
        await loadStatus(true)
      }}
      buckets={buckets} associations={associations}
      associationsLoading={associationsLoading} associationsFailure={associationsFailure}
      onAssociate={async (bucketName) => {
        await setBagDropAssociation(token, tenant, bucketName)
        await reloadAssociations()
      }}
      onUnassociate={async (bucketName) => {
        await deleteBagDropAssociation(token, tenant, bucketName)
        await reloadAssociations()
      }}
      status={status} statusLoading={statusLoading} statusFailure={statusFailure}
      onReconcile={async () => {
        await reconcileBagDrop(token, tenant)
        setPollAfterReconcile(true)
      }}
    />
  )
}

function messageFor(error: unknown, fallback: string): string {
  return error instanceof Error ? error.message : fallback
}

type BagDropViewProps = {
  callerRole: Role | null
  canConfigure: boolean
  config: ApiBagDropConfig | null
  configLoading: boolean
  configFailure: string | null
  onSave: (write: ApiBagDropConfigWrite) => Promise<ApiBagDropConfig>
  onVerify: () => Promise<ApiBagDropVerificationResult>
  onEnable: () => Promise<ApiBagDropEnableResult>
  onDisable: () => Promise<ApiBagDropConfig>
  onDelete: () => Promise<void>
  buckets: ApiBucket[]
  associations: ApiBagDropAssociation[]
  associationsLoading: boolean
  associationsFailure: string | null
  onAssociate: (bucketName: string) => Promise<void>
  onUnassociate: (bucketName: string) => Promise<void>
  status: ApiBagDropStatus | null
  statusLoading: boolean
  statusFailure: string | null
  onReconcile: () => Promise<void>
}

export function BagDropView(props: BagDropViewProps) {
  return (
    <>
      <PageSection variant="default">
        <Title headingLevel="h1" size="2xl">Bag Drop</Title>
        <Content component="p">
          Mirror selected buckets to another registry while keeping this project authoritative.
        </Content>
      </PageSection>
      <PageSection variant="secondary" isFilled hasBodyWrapper={false}>
        {/* MUTATION_ROLE_GATE: configuration is maintainer-only. */}
        {props.canConfigure ? (
          <>
            <DestinationZone {...props} />
            <MirroredBucketsZone {...props} />
          </>
        ) : null}
        <StatusZone
          status={props.status} loading={props.statusLoading} failure={props.statusFailure}
          canConfigure={props.canConfigure} callerRole={props.callerRole}
          onReconcile={props.onReconcile}
        />
      </PageSection>
    </>
  )
}

export function DestinationZone({
  config, configLoading, configFailure, onSave, onVerify, onEnable, onDisable, onDelete,
}: Pick<BagDropViewProps,
  'config' | 'configLoading' | 'configFailure' | 'onSave' | 'onVerify' | 'onEnable' |
  'onDisable' | 'onDelete'>) {
  const [draft, setDraft] = useState<DestinationDraft>(() => draftForBagDropConfig(config))
  const [baseline, setBaseline] = useState<DestinationDraft>(() => draftForBagDropConfig(config))
  const [busy, setBusy] = useState<string | null>(null)
  const [actionFailure, setActionFailure] = useState<string | null>(null)
  const [verification, setVerification] = useState<ApiBagDropVerificationResult | null>(null)

  useEffect(() => {
    const next = draftForBagDropConfig(config)
    setDraft(next)
    setBaseline(next)
  }, [config])

  const dirty = JSON.stringify(draft) !== JSON.stringify(baseline)
  const run = async (name: string, work: () => Promise<void>) => {
    setBusy(name)
    setActionFailure(null)
    setVerification(null)
    try {
      await work()
    } catch (err: unknown) {
      setActionFailure(messageFor(err, 'The action failed.'))
    } finally {
      setBusy(null)
    }
  }

  return (
    <Card aria-label="Destination">
      <CardTitle>Destination</CardTitle>
      <CardBody>
        {configFailure ? (
          <Alert variant="danger" isInline title="Bag Drop configuration could not be loaded">
            <Content component="p">{configFailure}</Content>
          </Alert>
        ) : null}
        {actionFailure ? (
          <DestinationActionFailure message={actionFailure} verification={verification} />
        ) : null}
        {config?.credential_protection === 'env_key' ? <EnvironmentKeyWarning /> : null}
        {configLoading ? (
          <Content component="p">Loading Bag Drop configuration…</Content>
        ) : configFailure ? null : (
          <DestinationFormView
            config={config} draft={draft} dirty={dirty} busy={busy}
            onDraftChange={setDraft}
            onSave={() => run('save', async () => {
              const stored = await onSave(bagDropWrite(draft))
              const saved = draftForBagDropConfig(stored)
              setDraft(saved)
              setBaseline(saved)
            })}
            onVerify={() => run('verify', async () => setVerification(await onVerify()))}
            onEnable={() => run('enable', async () => {
              const result = await onEnable()
              if (result.kind === 'refused') {
                setActionFailure(result.message)
                setVerification(result.verification ?? null)
              }
            })}
            onDisable={() => run('disable', async () => { await onDisable() })}
            onDelete={() => run('delete', onDelete)}
          />
        )}
        {verification && !actionFailure ? <VerificationResultView verification={verification} /> : null}
        {config?.last_verification ? (
          <LastVerificationView verification={config.last_verification} />
        ) : null}
      </CardBody>
    </Card>
  )
}

export function EnvironmentKeyWarning() {
  return (
    <Alert
      variant="warning" isInline title="The destination credential is sealed with an environment key"
      style={{ marginBottom: 16 }}
    >
      <Content component="p">
        It is protected in database dumps, but not against host compromise. Encrypted deployments
        seal this credential in the keyring.
      </Content>
    </Alert>
  )
}

export function DestinationActionFailure({
  message, verification,
}: {
  message: string
  verification: ApiBagDropVerificationResult | null
}) {
  return (
    <Alert variant="danger" isInline title="The action was refused">
      <Content component="p">{message}</Content>
      {verification ? <VerificationResultView verification={verification} /> : null}
    </Alert>
  )
}

export function DestinationFormView({
  config, draft, dirty, busy, onDraftChange, onSave, onVerify, onEnable, onDisable, onDelete,
}: {
  config: ApiBagDropConfig | null
  draft: DestinationDraft
  dirty: boolean
  busy: string | null
  onDraftChange: (draft: DestinationDraft) => void
  onSave: () => void
  onVerify: () => void
  onEnable: () => void
  onDisable: () => void
  onDelete: () => void
}) {
  const update = (fields: Partial<DestinationDraft>) => onDraftChange({ ...draft, ...fields })
  const verify = (
    <Button
      variant="secondary"
      /* MUTATION_VERIFY_DIRTY: verify always resolves the stored configuration. */
      isDisabled={dirty || !config || busy !== null}
      isLoading={busy === 'verify'} onClick={onVerify}
    >
      Verify
    </Button>
  )
  return (
    <Form>
      <FormGroup label="Adapter" isRequired fieldId="bagdrop-adapter">
        <FormSelect
          id="bagdrop-adapter" value={draft.adapter}
          onChange={(_event, value) => update({ adapter: value as DestinationDraft['adapter'] })}
        >
          <FormSelectOption value="hcp-packer" label="HCP Packer" />
          <FormSelectOption value="dufflebag" label="Dufflebag" />
        </FormSelect>
      </FormGroup>
      {draft.adapter === 'dufflebag' ? (
        <>
          <FormGroup label="Endpoint URL" isRequired fieldId="bagdrop-endpoint">
            <TextInput
              id="bagdrop-endpoint" type="url" value={draft.endpoint}
              onChange={(_event, value) => update({ endpoint: value })}
            />
          </FormGroup>
          <FormGroup label="CA chain" fieldId="bagdrop-ca-chain">
            <TextArea
              id="bagdrop-ca-chain" value={draft.caChain} resizeOrientation="vertical"
              onChange={(_event, value) => update({ caChain: value })}
              aria-label="Optional PEM-encoded CA chain"
            />
          </FormGroup>
        </>
      ) : null}
      <FormGroup label="Organization ID" isRequired fieldId="bagdrop-organization-id">
        <TextInput
          id="bagdrop-organization-id" value={draft.organizationID}
          onChange={(_event, value) => update({ organizationID: value })}
        />
      </FormGroup>
      <FormGroup label="Project ID" isRequired fieldId="bagdrop-project-id">
        <TextInput
          id="bagdrop-project-id" value={draft.projectID}
          onChange={(_event, value) => update({ projectID: value })}
        />
      </FormGroup>
      <FormGroup label="Client ID" isRequired fieldId="bagdrop-client-id">
        <TextInput
          id="bagdrop-client-id" value={draft.clientID}
          onChange={(_event, value) => update({ clientID: value })}
        />
      </FormGroup>
      <FormGroup label="Client secret" isRequired={!config?.secret_set} fieldId="bagdrop-client-secret">
        <TextInput
          id="bagdrop-client-secret" type="password" value={draft.clientSecret}
          onChange={(_event, value) => update({ clientSecret: value })}
          placeholder={config?.secret_set ? 'Leave blank to retain the stored secret' : ''}
        />
        {config?.secret_set ? <Label color="green" isCompact>Secret set</Label> : null}
      </FormGroup>
      <ActionGroup>
        <Button
          variant="primary" isDisabled={!dirty || !draftIsValid(draft, config) || busy !== null}
          isLoading={busy === 'save'} onClick={onSave}
        >
          Save
        </Button>
        {dirty ? (
          <Tooltip content="Save these changes before verifying; Verify checks the stored configuration.">
            <span tabIndex={0} aria-label="Save changes before verifying" style={{ display: 'inline-block' }}>
              {verify}
            </span>
          </Tooltip>
        ) : verify}
        {config && !config.enabled ? (
          <Button
            variant="secondary" isDisabled={dirty || busy !== null}
            isLoading={busy === 'enable'} onClick={onEnable}
          >Enable</Button>
        ) : null}
        {config?.enabled ? (
          <Button
            variant="secondary" isDisabled={dirty || busy !== null}
            isLoading={busy === 'disable'} onClick={onDisable}
          >Disable</Button>
        ) : null}
        {config ? (
          <Button
            variant="link" isDanger isDisabled={busy !== null}
            isLoading={busy === 'delete'} onClick={onDelete}
          >Delete configuration</Button>
        ) : null}
      </ActionGroup>
    </Form>
  )
}

function VerificationResultView({ verification }: { verification: ApiBagDropVerificationResult }) {
  return (
    <DescriptionList isHorizontal isCompact aria-label="Verification result">
      <Description label="Outcome" value={verification.outcome} />
      <Description label="Reason" value={verification.reason ?? '—'} />
      <Description label="Message" value={verification.message ?? '—'} />
    </DescriptionList>
  )
}

function LastVerificationView({
  verification,
}: {
  verification: ApiBagDropConfig['last_verification'] & object
}) {
  return (
    <DescriptionList isHorizontal isCompact aria-label="Last verification">
      <Description label="Last verification" value={verification.outcome} />
      <Description label="Reason" value={verification.reason ?? '—'} />
      <Description label="Message" value={verification.message ?? '—'} />
      <Description label="Verified at" value={verification.verified_at} />
    </DescriptionList>
  )
}

function Description({ label, value }: { label: string; value: string }) {
  return (
    <DescriptionListGroup>
      <DescriptionListTerm>{label}</DescriptionListTerm>
      <DescriptionListDescription>{value}</DescriptionListDescription>
    </DescriptionListGroup>
  )
}

export function MirroredBucketsZone({
  config, buckets, associations, associationsLoading, associationsFailure,
  onAssociate, onUnassociate,
}: Pick<BagDropViewProps,
  'config' | 'buckets' | 'associations' | 'associationsLoading' | 'associationsFailure' |
  'onAssociate' | 'onUnassociate'>) {
  const [selectedAvailable, setSelectedAvailable] = useState<string | null>(null)
  const [selectedMirrored, setSelectedMirrored] = useState<string | null>(null)
  const [confirming, setConfirming] = useState<string | null>(null)
  const [actionFailure, setActionFailure] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const run = async (work: () => Promise<void>) => {
    setActionFailure(null)
    setBusy(true)
    try {
      await work()
      setSelectedAvailable(null)
      setSelectedMirrored(null)
    } catch (err: unknown) {
      setActionFailure(messageFor(err, 'The bucket association could not be changed.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card aria-label="Mirrored buckets">
      <CardTitle>Mirrored buckets</CardTitle>
      <CardBody>
        <Content component="p">
          Within an associated bucket, this source is authoritative at the destination,
          including deletes.
        </Content>
        {associationsFailure ? (
          <Alert variant="danger" isInline title="Mirrored buckets could not be loaded">
            <Content component="p">{associationsFailure}</Content>
          </Alert>
        ) : null}
        {actionFailure ? (
          <Alert variant="danger" isInline title="The action was refused">
            <Content component="p">{actionFailure}</Content>
          </Alert>
        ) : null}
        {!config ? (
          <Alert variant="info" isInline title="Configure a destination before mirroring buckets" />
        ) : associationsLoading ? (
          <Content component="p">Loading mirrored buckets…</Content>
        ) : associationsFailure ? null : (
          <AssociationSelectorView
            buckets={buckets} associations={associations}
            selectedAvailable={selectedAvailable} selectedMirrored={selectedMirrored}
            confirming={confirming} busy={busy}
            onSelectAvailable={(name) => {
              setSelectedAvailable(name)
              setSelectedMirrored(null)
            }}
            onSelectMirrored={(name) => {
              setSelectedMirrored(name)
              setSelectedAvailable(null)
            }}
            onAssociate={(name) => { void run(() => onAssociate(name)) }}
            onRequestRemoval={setConfirming}
            onCancelRemoval={() => setConfirming(null)}
            onConfirmRemoval={(name) => {
              setConfirming(null)
              void run(() => onUnassociate(name))
            }}
          />
        )}
      </CardBody>
    </Card>
  )
}

export function AssociationSelectorView({
  buckets, associations, selectedAvailable, selectedMirrored, confirming, busy,
  onSelectAvailable, onSelectMirrored, onAssociate, onRequestRemoval,
  onCancelRemoval, onConfirmRemoval,
}: {
  buckets: ApiBucket[]
  associations: ApiBagDropAssociation[]
  selectedAvailable: string | null
  selectedMirrored: string | null
  confirming: string | null
  busy: boolean
  onSelectAvailable: (name: string) => void
  onSelectMirrored: (name: string) => void
  onAssociate: (name: string) => void
  onRequestRemoval: (name: string) => void
  onCancelRemoval: () => void
  onConfirmRemoval: (name: string) => void
}) {
  const mirroredNames = new Set(associations.map((association) => association.bucket_name))
  const available = buckets.filter((bucket) => !mirroredNames.has(bucket.name))
  const selectedAssociation = associations.find(
    (association) => association.bucket_name === selectedMirrored,
  )
  const canResume = selectedAssociation?.state === 'pending_removal'
  return (
    <>
      {confirming ? (
        <BucketRemovalConfirmation
          bucketName={confirming} onCancel={onCancelRemoval}
          onConfirm={() => onConfirmRemoval(confirming)}
        />
      ) : null}
      {available.length === 0 && associations.length === 0 ? (
        <Content component="p">No local buckets are available to mirror.</Content>
      ) : null}
      <DualListSelector id="bagdrop-buckets">
        <DualListSelectorPane
          title="Local buckets" status={`${available.length} available`} id="bagdrop-local"
        >
          <DualListSelectorList>
            {available.map((bucket) => (
              <DualListSelectorListItem
                key={bucket.name} id={`available-${bucket.name}`}
                isSelected={selectedAvailable === bucket.name}
                onOptionSelect={() => onSelectAvailable(bucket.name)}
              >{bucket.name}</DualListSelectorListItem>
            ))}
          </DualListSelectorList>
        </DualListSelectorPane>
        <DualListSelectorControlsWrapper aria-label="Bucket mirroring controls">
          <DualListSelectorControl
            aria-label={canResume ? 'Resume selected bucket' : 'Mirror selected bucket'}
            isDisabled={busy || (!selectedAvailable && !canResume)}
            onClick={() => {
              const name = selectedAvailable ?? (canResume ? selectedMirrored : null)
              if (name) onAssociate(name)
            }}
            icon={<AngleRightIcon />}
          />
          <DualListSelectorControl
            aria-label="Stop mirroring selected bucket"
            isDisabled={busy || !selectedMirrored || canResume}
            /* MUTATION_UNASSOCIATE_GATE: this control may only open the warning. */
            onClick={() => { if (selectedMirrored) onRequestRemoval(selectedMirrored) }}
            icon={<AngleLeftIcon />}
          />
        </DualListSelectorControlsWrapper>
        <DualListSelectorPane
          title="Mirrored buckets" status={`${associations.length} associated`}
          id="bagdrop-mirrored" isChosen
        >
          <DualListSelectorList>
            {associations.map((association) => (
              <DualListSelectorListItem
                key={association.bucket_name} id={`mirrored-${association.bucket_name}`}
                isSelected={selectedMirrored === association.bucket_name}
                onOptionSelect={() => onSelectMirrored(association.bucket_name)}
              >
                {association.bucket_name}
                {association.state === 'pending_removal' ? (
                  <> <Label color="orange" isCompact>Removing</Label></>
                ) : null}
              </DualListSelectorListItem>
            ))}
          </DualListSelectorList>
        </DualListSelectorPane>
      </DualListSelector>
    </>
  )
}

export function BucketRemovalConfirmation({
  bucketName, onConfirm, onCancel,
}: {
  bucketName: string
  onConfirm: () => void
  onCancel: () => void
}) {
  return (
    <Alert
      variant="warning" isInline title={`Stop mirroring ${bucketName}?`}
      style={{ marginBottom: 16 }}
    >
      <Content component="p">Its copy at the destination will be deleted.</Content>
      <Button variant="danger" onClick={onConfirm}>Stop mirroring {bucketName}</Button>
      <Button variant="link" onClick={onCancel}>Cancel</Button>
    </Alert>
  )
}

export function StatusZone({
  status, loading, failure, canConfigure, callerRole, onReconcile,
}: {
  status: ApiBagDropStatus | null
  loading: boolean
  failure: string | null
  canConfigure: boolean
  callerRole: Role | null
  onReconcile: () => Promise<void>
}) {
  const [actionFailure, setActionFailure] = useState<string | null>(null)
  const [reconciling, setReconciling] = useState(false)
  return (
    <Card aria-label="Status">
      <CardTitle>Status</CardTitle>
      <CardBody>
        {failure ? (
          <Alert variant="danger" isInline title="Bag Drop status could not be loaded">
            <Content component="p">{failure}</Content>
          </Alert>
        ) : null}
        {actionFailure ? (
          <Alert variant="danger" isInline title="Reconciliation could not be requested">
            <Content component="p">{actionFailure}</Content>
          </Alert>
        ) : null}
        {canConfigure && !loading && !failure && status?.configured ? (
          <Button
            variant="secondary" isLoading={reconciling} isDisabled={reconciling}
            onClick={() => {
              setReconciling(true)
              setActionFailure(null)
              void onReconcile()
                .catch((err: unknown) => setActionFailure(
                  messageFor(err, 'Reconciliation could not be requested.'),
                ))
                .finally(() => setReconciling(false))
            }}
          >Reconcile now</Button>
        ) : null}
        {loading ? (
          <Content component="p">Loading Bag Drop status…</Content>
        ) : failure ? null : !status?.configured ? (
          <Alert variant="info" isInline title="Bag Drop is not configured">
            <Content component="p">No buckets are being mirrored from this project.</Content>
          </Alert>
        ) : status.associations.length === 0 ? (
          <Alert variant="info" isInline title="No buckets are mirrored">
            <Content component="p">The destination is configured, but it has no bucket associations.</Content>
          </Alert>
        ) : (
          <BagDropStatusTable associations={status.associations} />
        )}
        {!canConfigure && callerRole ? (
          <Content component="p">Destination configuration is available to maintainers.</Content>
        ) : null}
      </CardBody>
    </Card>
  )
}

export function BagDropStatusTable({ associations }: { associations: ApiBagDropAssociation[] }) {
  const [expanded, setExpanded] = useState<string | null>(null)
  return (
    <BagDropStatusTableView
      associations={associations} expanded={expanded}
      onToggle={(bucketName) => setExpanded(expanded === bucketName ? null : bucketName)}
    />
  )
}

export function BagDropStatusTableView({
  associations, expanded, onToggle,
}: {
  associations: ApiBagDropAssociation[]
  expanded: string | null
  onToggle: (bucketName: string) => void
}) {
  return (
    <Table aria-label="Bag Drop status" variant="compact">
      <Thead>
        <Tr>
          <Th screenReaderText="Expand error" />
          <Th>Bucket</Th><Th>Sync status</Th><Th>Last synced</Th><Th>Last attempt</Th>
        </Tr>
      </Thead>
      {associations.map((association, index) => {
        const hasError = Boolean(association.last_sync_error)
        const isExpanded = hasError && expanded === association.bucket_name
        return (
          <Tbody key={association.bucket_name} isExpanded={isExpanded}>
            <Tr>
              {hasError ? (
                <Td expand={{
                  rowIndex: index, isExpanded,
                  onToggle: () => onToggle(association.bucket_name),
                }} />
              ) : <Td />}
              <Td dataLabel="Bucket">{association.bucket_name}</Td>
              <Td dataLabel="Sync status"><Label isCompact>{association.sync_status}</Label></Td>
              <Td dataLabel="Last synced">{association.last_synced_at ?? 'Never'}</Td>
              <Td dataLabel="Last attempt">{association.last_attempt_at ?? 'Never'}</Td>
            </Tr>
            {hasError ? (
              <Tr isExpanded={isExpanded}>
                <Td colSpan={5}>
                  <ExpandableRowContent>{association.last_sync_error}</ExpandableRowContent>
                </Td>
              </Tr>
            ) : null}
          </Tbody>
        )
      })}
    </Table>
  )
}
