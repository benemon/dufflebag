import { useCallback, useEffect, useState } from 'react'
import {
  ActionGroup, Alert, Button, Card, CardBody, CardTitle, Content, DescriptionList, DescriptionListDescription,
  DescriptionListGroup, DescriptionListTerm, Label, PageSection,
} from '@patternfly/react-core'

import { signOutIfUnauthorized } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { permitsAction, type Role } from '../auth/permissions'
import { PluginErrorCard, PluginLoadingCard } from '../components/PluginLoadState'
import { ScreenHeader } from '../components/ScreenHeader'
import {
  disablePluginRegistry, enablePluginRegistry, exposePluginRegistry, getDefaultPlatforms,
  getPluginRegistry, setDefaultPlatforms, unexposePluginRegistry, type PluginRegistry,
} from '../data/pluginRegistry'
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
      setDefaultPlatformsState(next.enabled ? await getDefaultPlatforms(token, organizationID) : [])
      setRegistry(next)
      setFailure(null)
    } catch (error: unknown) {
      if (signOutIfUnauthorized(error, signOut)) return
      setFailure(pluginRegistryErrorMessage(error, 'The plugin registry could not be loaded.'))
    } finally {
      setLoading(false)
    }
  }, [organizationID, signOut, token])

  useEffect(() => { void reload() }, [reload])

  if (!organizationID) {
    return <PageSection><Alert variant="info" isInline title="Select an organization to manage its plugin registry" /></PageSection>
  }

  return (
    <PluginRegistrySettingsView
      organizationName={tenant.organization}
      callerRole={self?.role ?? null}
      registry={registry}
      defaultPlatforms={defaultPlatforms}
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
      }}
      onSetDefaultPlatforms={async (platforms) => {
        setDefaultPlatformsState(await setDefaultPlatforms(token, organizationID, platforms))
      }}
    />
  )
}

export function PluginRegistrySettingsView({
  organizationName, callerRole, registry, defaultPlatforms, loading, failure,
  onRefresh, onEnable, onExpose, onUnexpose, onDisable, onSetDefaultPlatforms,
}: {
  organizationName: string
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
        description={(
          <>
            Mirrors Packer plugins for {organizationName}, so packer init resolves them from dufflebag
            instead of the internet. Turn it on, expose it to Packer, and set the platforms a first
            import selects.
          </>
        )}
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
              </CardBody>
            </Card>
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
