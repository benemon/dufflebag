import { useCallback, useEffect, useState } from 'react'
import {
  ActionGroup, Alert, Button, Card, CardBody, CardTitle, Content, DescriptionList, DescriptionListDescription,
  DescriptionListGroup, DescriptionListTerm, Flex, FlexItem, Label, NumberInput, PageSection, Switch, TextInput,
} from '@patternfly/react-core'

import { signOutIfUnauthorized } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { permitsAction, type Role } from '../auth/permissions'
import { PluginErrorCard, PluginLoadingCard } from '../components/PluginLoadState'
import { ScreenHeader } from '../components/ScreenHeader'
import {
  cronProblem, describeCron, disablePluginRegistry, enablePluginRegistry, exposePluginRegistry, getDefaultPlatforms, getPluginPrune,
  getPluginRegistry, pruneAges, prunePreview, runPluginPrune, scheduleWords, setDefaultPlatforms, setPluginPrune,
  templateStanza, unexposePluginRegistry, type PluginPrune, type PluginPruneRequest, type PluginRegistry,
} from '../data/pluginRegistry'
import { TemplateStanzaBlock } from './PluginDetail'
import { useTenant } from '../data/tenant'
import {
  DefaultPlatformsEditor, DisablePluginRegistryConfirmation, PluginRegistryConfirmation,
  pluginRegistryErrorMessage,
} from './Plugins'

export function PluginRegistrySettings() {
  const { state, self, selectedOrganization, signOut } = useAuth()
  const { tenant } = useTenant()
  const organizationID = selectedOrganization ?? state?.claims.organizationID ?? null
  const token = state?.token ?? ''
  const [registry, setRegistry] = useState<PluginRegistry | null>(null)
  const [defaultPlatforms, setDefaultPlatformsState] = useState<string[]>([])
  const [prune, setPrune] = useState<PluginPrune | null>(null)
  const [loading, setLoading] = useState(true)
  const [failure, setFailure] = useState<string | null>(null)

  const reload = useCallback(async () => {
    if (!organizationID || token === '') {
      setRegistry(null)
      setLoading(false)
      return
    }
    setLoading(true)
    try {
      const next = await getPluginRegistry(token, organizationID)
      const configures = permitsAction(self?.role ?? null, 'configurePluginRegistry')
      setDefaultPlatformsState(next.enabled ? await getDefaultPlatforms(token, organizationID) : [])
      // The schedule is maintainer-only on the API, like the rest of this page's controls.
      setPrune(next.enabled && configures ? await getPluginPrune(token, organizationID) : null)
      setRegistry(next)
      setFailure(null)
    } catch (error: unknown) {
      if (signOutIfUnauthorized(error, signOut)) return
      setFailure(pluginRegistryErrorMessage(error, 'The plugin registry could not be loaded.'))
    } finally {
      setLoading(false)
    }
  }, [organizationID, self?.role, signOut, token])

  useEffect(() => { void reload() }, [reload])

  if (!organizationID) {
    return <PageSection><Alert variant="info" isInline title="Select an organization to manage its plugin registry" /></PageSection>
  }

  return (
    <PluginRegistrySettingsView
      organizationName={tenant.organization}
      host={window.location.hostname}
      callerRole={self?.role ?? null}
      registry={registry}
      defaultPlatforms={defaultPlatforms}
      prune={prune}
      onSetPrune={async (request) => { setPrune(await setPluginPrune(token, organizationID, request)) }}
      onPruneNow={async () => { setPrune(await runPluginPrune(token, organizationID)) }}
      loading={loading}
      failure={failure}
      onRefresh={reload}
      onEnable={async () => setRegistry(await enablePluginRegistry(token, organizationID))}
      onExpose={async () => setRegistry(await exposePluginRegistry(token, organizationID))}
      onUnexpose={async () => setRegistry(await unexposePluginRegistry(token, organizationID))}
      onDisable={async () => {
        await disablePluginRegistry(token, organizationID)
        setRegistry({ enabled: false, exposed: false })
        setDefaultPlatformsState([])
        setPrune(null)
      }}
      onSetDefaultPlatforms={async (platforms) => {
        setDefaultPlatformsState(await setDefaultPlatforms(token, organizationID, platforms))
      }}
    />
  )
}

export function PluginRegistrySettingsView({
  organizationName, host, callerRole, registry, defaultPlatforms, loading, failure,
  onRefresh, onEnable, onExpose, onUnexpose, onDisable, onSetDefaultPlatforms,
  prune = null, onSetPrune = async () => {}, onPruneNow = async () => {},
}: {
  prune?: PluginPrune | null
  onSetPrune?: (request: PluginPruneRequest) => Promise<void>
  onPruneNow?: () => Promise<void>
  organizationName: string
  host: string
  callerRole: Role | null
  registry: PluginRegistry | null
  defaultPlatforms: string[]
  loading: boolean
  failure: string | null
  onRefresh: () => void | Promise<void>
  onEnable: () => Promise<void>
  onExpose: () => Promise<void>
  onUnexpose: () => Promise<void>
  onDisable: () => Promise<void>
  onSetDefaultPlatforms: (platforms: string[]) => Promise<void>
}) {
  const [actionFailure, setActionFailure] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [confirming, setConfirming] = useState<'expose' | 'unexpose' | 'disable' | null>(null)
  const canConfigure = permitsAction(callerRole, 'configurePluginRegistry')
  const run = async (work: () => Promise<void>) => {
    setActionFailure(null)
    setBusy(true)
    try {
      await work()
    } catch (error: unknown) {
      setActionFailure(pluginRegistryErrorMessage(error, 'The plugin registry action failed.'))
    } finally {
      setBusy(false)
    }
  }
  const confirm = (work: () => Promise<void>) => {
    setConfirming(null)
    void run(work)
  }

  return (
    <>
      <ScreenHeader
        title="Plugin registry"
        onRefresh={onRefresh}
        refreshing={loading}
      />
      <PageSection variant="secondary" isFilled>
        {actionFailure ? (
          <Alert variant="danger" isInline title="The action failed">
            <Content component="p">{actionFailure}</Content>
          </Alert>
        ) : null}
        {loading ? (
          <PluginLoadingCard message="Loading the plugin registry…" />
        ) : failure ? (
          <PluginErrorCard title="The plugin registry could not be loaded" error={failure} onRetry={onRefresh} />
        ) : !registry ? null : (
          <>
            <Card aria-label="Plugin registry">
              <CardTitle>Registry</CardTitle>
              <CardBody>
                <Flex direction={{ default: 'column' }} gap={{ default: 'gapMd' }}>
                <DescriptionList isHorizontal isCompact>
                  <DescriptionListGroup>
                    <DescriptionListTerm>Status</DescriptionListTerm>
                    <DescriptionListDescription>
                      {registry.enabled ? <Label color="green" isCompact>Enabled</Label> : <Label isCompact>Not enabled</Label>}
                    </DescriptionListDescription>
                  </DescriptionListGroup>
                  <DescriptionListGroup>
                    <DescriptionListTerm>Exposure</DescriptionListTerm>
                    <DescriptionListDescription>
                      {registry.exposed ? <Label color="blue" isCompact>Exposed to Packer</Label> : <Label isCompact>Not exposed</Label>}
                    </DescriptionListDescription>
                  </DescriptionListGroup>
                </DescriptionList>
                <Content component="p">
                  {!registry.enabled
                    ? `The plugin registry isn't enabled. Enable it to mirror Packer plugins for ${organizationName}; nothing is stored before then.`
                    : registry.exposed
                      ? 'The registry is enabled and exposed. Plugins are anonymously readable to anyone who can reach dufflebag.'
                      : 'The registry is enabled but not exposed. Plugins are stored and can be managed, but Packer can’t reach them until the registry is exposed.'}
                </Content>
                {canConfigure ? (
                  <ActionGroup>
                    {!registry.enabled ? (
                      <Button variant="primary" isLoading={busy} isDisabled={busy} onClick={() => void run(onEnable)}>
                        Enable the registry
                      </Button>
                    ) : registry.exposed ? (
                      <Button variant="secondary" isDisabled={busy} onClick={() => setConfirming('unexpose')}>
                        Unexpose
                      </Button>
                    ) : (
                      <Button variant="primary" isDisabled={busy} onClick={() => setConfirming('expose')}>
                        Expose
                      </Button>
                    )}
                    {registry.enabled ? (
                      <Button
                        variant="danger" isDisabled={busy || registry.exposed}
                        onClick={() => setConfirming('disable')}
                      >Disable registry</Button>
                    ) : null}
                  </ActionGroup>
                ) : null}
                {registry.exposed ? <Content component="small">Unexpose the registry first before disabling it.</Content> : null}
                </Flex>
              </CardBody>
            </Card>
            {registry.exposed ? (
              <Card>
                <CardTitle>Packer</CardTitle>
                <CardBody>
                  <Content component="p">
                    Packer resolves this organization’s plugins from <code>{host}/plugins/{organizationName}</code>.
                    Name the plugin and pin its version in the template; <code>packer init</code> fetches it from here.
                  </Content>
                  <TemplateStanzaBlock id="registry-stanza" hcl={templateStanza(host, organizationName, 'NAME', '>= 1.0.0')} />
                  <Content component="small">Replace NAME with the plugin as the catalogue lists it; each plugin’s page shows its stanza with the newest available version.</Content>
                </CardBody>
              </Card>
            ) : null}
            {registry.enabled ? (
              <Card>
                <CardTitle>Default platforms</CardTitle>
                <CardBody>
                  {canConfigure ? (
                    <DefaultPlatformsEditor
                      platforms={defaultPlatforms}
                      busy={busy}
                      onSave={(platforms) => void run(() => onSetDefaultPlatforms(platforms))}
                    />
                  ) : (
                    <Content component="p">Default platforms: {defaultPlatforms.join(', ')}</Content>
                  )}
                </CardBody>
              </Card>
            ) : null}
            {registry.enabled && canConfigure && prune ? (
              <Card aria-label="Prune jobs">
                <CardTitle>Prune jobs</CardTitle>
                <CardBody>
                  <PruneJobsEditor prune={prune} busy={busy} onSave={(request) => run(() => onSetPrune(request))} onPruneNow={() => run(onPruneNow)} />
                </CardBody>
              </Card>
            ) : null}
          </>
        )}
        {confirming === 'expose' ? (
          <PluginRegistryConfirmation
            title="Expose the plugin registry?"
            body="Plugins become anonymously readable to anyone who can reach dufflebag."
            verb="Expose registry"
            busy={busy}
            onCancel={() => setConfirming(null)}
            onConfirm={() => confirm(onExpose)}
          />
        ) : null}
        {confirming === 'unexpose' ? (
          <PluginRegistryConfirmation
            title="Unexpose the plugin registry?"
            body="In-flight packer init fails when the registry is unexposed."
            verb="Unexpose registry"
            busy={busy}
            onCancel={() => setConfirming(null)}
            onConfirm={() => confirm(onUnexpose)}
          />
        ) : null}
        {confirming === 'disable' ? (
          <DisablePluginRegistryConfirmation
            organizationName={organizationName}
            busy={busy}
            onCancel={() => setConfirming(null)}
            onConfirm={() => confirm(onDisable)}
          />
        ) : null}
      </PageSection>
    </>
  )
}

// The schedule's labels: Off, or On with its cadence, plus the last run's failure.
export function pruneLabels(prune: PluginPrune): { text: string; color: 'grey' | 'green' | 'red' }[] {
  if (!prune.enabled) return [{ text: 'Off', color: 'grey' }, ...(prune.last_error ? [{ text: 'Last run failed', color: 'red' as const }] : [])]
  return [{ text: 'On', color: 'green' }, ...(prune.last_error ? [{ text: 'Last run failed', color: 'red' as const }] : [])]
}

export function pruneSavedMessage(prune: PluginPrune): string {
  if (!prune.enabled) return 'Pruning is off. Jobs are kept until they are cleared by hand.'
  const next = prune.next_run_at ? ` Next run ${new Date(prune.next_run_at).toLocaleString()}.` : ''
  return `Finished jobs older than ${prune.age_days} days are cleared ${scheduleWords(prune.schedule)}.${next}`
}

export function PruneJobsEditor({ prune, busy, onSave, onPruneNow }: {
  prune: PluginPrune
  busy: boolean
  onSave: (request: PluginPruneRequest) => Promise<void> | void
  onPruneNow: () => Promise<void> | void
}) {
  const [enabled, setEnabled] = useState(prune.enabled)
  const [ageDays, setAgeDays] = useState(prune.age_days)
  const [schedule, setSchedule] = useState(prune.schedule)
  const [saved, setSaved] = useState<string | null>(null)
  useEffect(() => {
    setEnabled(prune.enabled)
    setAgeDays(prune.age_days)
    setSchedule(prune.schedule)
  }, [prune])
  const problem = cronProblem(schedule)
  const dirty = enabled !== prune.enabled || ageDays !== prune.age_days || schedule !== prune.schedule
  const step = pruneAges.indexOf(ageDays)
  const format = (value: string | undefined) => (value ? new Date(value).toLocaleString() : '—')
  const described = describeCron(schedule)
  return (
    <Flex direction={{ default: 'column' }} gap={{ default: 'gapMd' }}>
      {saved ? <Alert variant="success" isInline title="Prune schedule saved" actionLinks={<Button variant="link" isInline onClick={() => setSaved(null)}>Dismiss</Button>}><Content component="p">{saved}</Content></Alert> : null}
      <Flex gap={{ default: 'gapSm' }} alignItems={{ default: 'alignItemsCenter' }}>
        {pruneLabels(prune).map((label) => <FlexItem key={label.text}><Label isCompact color={label.color}>{label.text}</Label></FlexItem>)}
      </Flex>
      <Switch
        id="prune-enabled" label="Clear finished jobs on a schedule" isChecked={enabled} isDisabled={busy}
        onChange={(_event, checked) => setEnabled(checked)}
      />
      <DescriptionList isHorizontal isCompact>
        <DescriptionListGroup>
          <DescriptionListTerm>Age limit</DescriptionListTerm>
          <DescriptionListDescription>
            <NumberInput
              value={ageDays} unit={ageDays === 1 ? 'day' : 'days'} widthChars={4} isDisabled={busy || !enabled}
              inputAriaLabel="Age limit in days" minusBtnAriaLabel="Shorter age limit" plusBtnAriaLabel="Longer age limit"
              onMinus={() => setAgeDays(pruneAges[Math.max(0, step - 1)] ?? ageDays)} onPlus={() => setAgeDays(pruneAges[Math.min(pruneAges.length - 1, step + 1)] ?? ageDays)}
              inputProps={{ readOnly: true }}
            />
            <Content component="small">Counted from when each job finished.</Content>
          </DescriptionListDescription>
        </DescriptionListGroup>
        <DescriptionListGroup>
          <DescriptionListTerm>Schedule</DescriptionListTerm>
          <DescriptionListDescription>
            <TextInput
              id="prune-schedule" aria-label="Cron expression, UTC" value={schedule} isDisabled={busy || !enabled}
              validated={problem ? 'error' : 'default'} onChange={(_event, value) => setSchedule(value)} style={{ fontFamily: 'var(--pf-t--global--font--family--mono)' }}
            />
            <Content component="small">{problem ?? described ?? `Runs ${scheduleWords(schedule)}.`}</Content>
          </DescriptionListDescription>
        </DescriptionListGroup>
        <DescriptionListGroup>
          <DescriptionListTerm>Preview</DescriptionListTerm>
          <DescriptionListDescription>{prunePreview(prune, enabled, ageDays)}</DescriptionListDescription>
        </DescriptionListGroup>
        <DescriptionListGroup>
          <DescriptionListTerm>Next run</DescriptionListTerm>
          <DescriptionListDescription>{prune.enabled ? format(prune.next_run_at) : '—'}</DescriptionListDescription>
        </DescriptionListGroup>
        <DescriptionListGroup>
          <DescriptionListTerm>Last run</DescriptionListTerm>
          <DescriptionListDescription>{prune.last_run_at ? `${format(prune.last_run_at)} · cleared ${prune.last_run_cleared}` : 'Never'}</DescriptionListDescription>
        </DescriptionListGroup>
        <DescriptionListGroup>
          <DescriptionListTerm>Last error</DescriptionListTerm>
          <DescriptionListDescription style={prune.last_error ? { color: 'var(--pf-t--global--color--status--danger--default)' } : undefined}>{prune.last_error || 'None'}</DescriptionListDescription>
        </DescriptionListGroup>
      </DescriptionList>
      <Flex gap={{ default: 'gapSm' }} alignItems={{ default: 'alignItemsCenter' }}>
        <Button
          variant="primary" isLoading={busy} isDisabled={busy || !dirty || problem !== null}
          onClick={() => void Promise.resolve(onSave({ enabled, age_days: ageDays, schedule })).then(() => setSaved(pruneSavedMessage({ ...prune, enabled, age_days: ageDays, schedule })))}
        >
          Save prune schedule
        </Button>
        <Button variant="secondary" isDisabled={busy || !prune.enabled || problem !== null} onClick={() => void onPruneNow()}>Prune now</Button>
        {dirty ? <Button variant="link" isDisabled={busy} onClick={() => { setEnabled(prune.enabled); setAgeDays(prune.age_days); setSchedule(prune.schedule) }}>Reset</Button> : null}
      </Flex>
    </Flex>
  )
}
