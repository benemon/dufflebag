import { useCallback, useEffect, useState } from 'react'
import {
  ActionGroup, Alert, Breadcrumb, BreadcrumbItem, Button, Card, CardBody, CardTitle, Content,
  EmptyState, EmptyStateActions, EmptyStateBody, EmptyStateFooter, PageSection,
} from '@patternfly/react-core'
import { useNavigate } from 'react-router'

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
  const navigate = useNavigate()
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
      setFailure(pluginRegistryErrorMessage(error, 'Registry settings could not be loaded.'))
    } finally {
      setLoading(false)
    }
  }, [organizationID, signOut, token])

  useEffect(() => { void reload() }, [reload])

  if (!organizationID) {
    return <PageSection><Alert variant="info" isInline title="Select an organization to manage registry settings" /></PageSection>
  }

  return (
    <PluginRegistrySettingsView
      organizationName={tenant.organization}
      callerRole={self?.role ?? null}
      registry={registry}
      defaultPlatforms={defaultPlatforms}
      loading={loading}
      failure={failure}
      onBackToRegistry={() => navigate('/buckets')}
      onBackToPlugins={() => navigate('/plugin-registry')}
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
  onBackToRegistry, onBackToPlugins, onRefresh, onEnable, onExpose, onUnexpose, onDisable,
  onSetDefaultPlatforms,
}: {
  organizationName: string
  callerRole: Role | null
  registry: PluginRegistry | null
  defaultPlatforms: string[]
  loading: boolean
  failure: string | null
  onBackToRegistry: () => void
  onBackToPlugins: () => void
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
        breadcrumbs={(
          <Breadcrumb>
            <BreadcrumbItem component="button" onClick={onBackToRegistry}>Registry</BreadcrumbItem>
            <BreadcrumbItem component="button" onClick={onBackToPlugins}>Plugins</BreadcrumbItem>
            <BreadcrumbItem isActive>Registry settings</BreadcrumbItem>
          </Breadcrumb>
        )}
        title="Registry settings"
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
          <PluginLoadingCard message="Loading registry settings…" />
        ) : failure ? (
          <PluginErrorCard title="Registry settings could not be loaded" error={failure} onRetry={onRefresh} />
        ) : !registry ? null : !registry.enabled ? (
          <EmptyState titleText="The plugin registry isn't enabled" headingLevel="h2">
            <EmptyStateBody>
              Enable it to mirror Packer plugins for {organizationName}, so packer init resolves
              them from dufflebag instead of the internet.
            </EmptyStateBody>
            {canConfigure ? (
              <EmptyStateFooter><EmptyStateActions>
                <Button variant="primary" isLoading={busy} isDisabled={busy} onClick={() => void run(onEnable)}>
                  Enable the registry
                </Button>
              </EmptyStateActions></EmptyStateFooter>
            ) : null}
          </EmptyState>
        ) : (
          <Card aria-label="Registry settings">
            <CardTitle>Registry lifecycle</CardTitle>
            <CardBody>
              <Content component="p">
                The registry is {registry.exposed ? 'enabled and exposed' : 'enabled but not exposed'}.
              </Content>
              {canConfigure ? (
                <ActionGroup>
                  {registry.exposed ? (
                    <Button variant="secondary" isDisabled={busy} onClick={() => setConfirming('unexpose')}>
                      Unexpose
                    </Button>
                  ) : (
                    <Button variant="primary" isDisabled={busy} onClick={() => setConfirming('expose')}>
                      Expose
                    </Button>
                  )}
                  <Button
                    variant="danger" isDisabled={busy || registry.exposed}
                    onClick={() => setConfirming('disable')}
                  >Disable registry</Button>
                </ActionGroup>
              ) : null}
              {registry.exposed ? <Content component="p">Unexpose the registry first before disabling it.</Content> : null}
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
