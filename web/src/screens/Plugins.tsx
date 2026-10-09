import { useCallback, useEffect, useState } from 'react'
import {
  ActionGroup, Alert, Button, Card, CardBody, CardTitle, Content, EmptyState,
  EmptyStateActions, EmptyStateBody, EmptyStateFooter, Modal, ModalBody, ModalFooter,
  ModalHeader, PageSection, Spinner,
} from '@patternfly/react-core'

import { signOutIfUnauthorized } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { permitsAction, type Role } from '../auth/permissions'
import { ScreenHeader } from '../components/ScreenHeader'
import { TypedConfirmModal } from '../components/TypedConfirmModal'
import {
  disablePluginRegistry, enablePluginRegistry, exposePluginRegistry, getPluginRegistry,
  unexposePluginRegistry, type PluginRegistry,
} from '../data/pluginRegistry'
import { useTenant } from '../data/tenant'

export function Plugins() {
  const { state, self, selectedOrganization, signOut } = useAuth()
  const { tenant } = useTenant()
  const organizationID = selectedOrganization ?? state?.claims.organizationID ?? null
  const token = state?.token ?? ''
  const [registry, setRegistry] = useState<PluginRegistry | null>(null)
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
      setRegistry(await getPluginRegistry(token, organizationID))
      setFailure(null)
    } catch (error: unknown) {
      if (signOutIfUnauthorized(error, signOut)) return
      setFailure(pluginRegistryErrorMessage(error, 'Plugin registry could not be loaded.'))
    } finally {
      setLoading(false)
    }
  }, [organizationID, signOut, token])

  useEffect(() => { void reload() }, [reload])

  if (!organizationID) {
    return <PageSection><Alert variant="info" isInline title="Select an organization to manage plugins" /></PageSection>
  }

  return (
    <PluginRegistryView
      organizationName={tenant.organization}
      callerRole={self?.role ?? null}
      registry={registry}
      loading={loading}
      failure={failure}
      onRefresh={reload}
      onEnable={async () => {
        const next = await enablePluginRegistry(token, organizationID)
        setRegistry(next)
      }}
      onExpose={async () => {
        const next = await exposePluginRegistry(token, organizationID)
        setRegistry(next)
      }}
      onUnexpose={async () => {
        const next = await unexposePluginRegistry(token, organizationID)
        setRegistry(next)
      }}
      onDisable={async () => {
        await disablePluginRegistry(token, organizationID)
        setRegistry({ enabled: false, exposed: false })
      }}
    />
  )
}

export function pluginRegistryErrorMessage(error: unknown, fallback: string): string {
  return error instanceof Error ? error.message : fallback
}

type PluginRegistryViewProps = {
  organizationName: string
  callerRole: Role | null
  registry: PluginRegistry | null
  loading: boolean
  failure: string | null
  onRefresh: () => void | Promise<void>
  onEnable: () => Promise<void>
  onExpose: () => Promise<void>
  onUnexpose: () => Promise<void>
  onDisable: () => Promise<void>
}

export function PluginRegistryView(props: PluginRegistryViewProps) {
  const [actionFailure, setActionFailure] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const canConfigure = permitsAction(props.callerRole, 'configurePluginRegistry')

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

  return (
    <>
      <ScreenHeader
        title="Plugins"
        description="Mirror Packer plugins through this organization's registry."
        onRefresh={props.onRefresh}
        refreshing={props.loading}
      />
      <PageSection variant="secondary" isFilled>
        {props.failure ? (
          <Alert variant="danger" isInline title="Plugin registry could not be loaded">
            <Content component="p">{props.failure}</Content>
          </Alert>
        ) : null}
        {actionFailure ? (
          <Alert variant="danger" isInline title="The action failed">
            <Content component="p">{actionFailure}</Content>
          </Alert>
        ) : null}
        {props.loading ? (
          <><Spinner aria-label="Loading plugin registry…" /><Content component="p">Loading plugin registry…</Content></>
        ) : props.failure || !props.registry ? null : !props.registry.enabled ? (
          <EmptyState titleText="The plugin registry isn't enabled" headingLevel="h2">
            <EmptyStateBody>
              Enable it to mirror Packer plugins for {props.organizationName}, so packer init resolves
              them from dufflebag instead of the internet.
            </EmptyStateBody>
            {canConfigure ? (
              <EmptyStateFooter><EmptyStateActions>
                <Button variant="primary" isLoading={busy} isDisabled={busy} onClick={() => void run(props.onEnable)}>
                  Enable the registry
                </Button>
              </EmptyStateActions></EmptyStateFooter>
            ) : null}
          </EmptyState>
        ) : (
          <EnabledRegistry
            {...props}
            canConfigure={canConfigure}
            busy={busy}
            run={run}
          />
        )}
      </PageSection>
    </>
  )
}

function EnabledRegistry({
  organizationName, registry, canConfigure, busy, run, onExpose, onUnexpose, onDisable,
}: PluginRegistryViewProps & {
  canConfigure: boolean
  busy: boolean
  run: (work: () => Promise<void>) => Promise<void>
}) {
  const [confirming, setConfirming] = useState<'expose' | 'unexpose' | 'disable' | null>(null)
  const exposed = registry?.exposed ?? false
  const confirm = (work: () => Promise<void>) => {
    setConfirming(null)
    void run(work)
  }

  return (
    <>
      {exposed ? (
        <Alert variant="success" isInline title="The registry is exposed">
          <Content component="p">Plugins are anonymously readable to anyone who can reach dufflebag.</Content>
        </Alert>
      ) : (
        <Alert variant="info" isInline title="The registry is enabled but not exposed">
          <Content component="p">Packer can't reach plugins until the registry is exposed.</Content>
        </Alert>
      )}
      <EmptyState titleText="No plugins yet." headingLevel="h2" />
      {canConfigure ? (
        <Card aria-label="Registry settings">
          <CardTitle>Registry settings</CardTitle>
          <CardBody>
            <ActionGroup>
              {exposed ? (
                <Button variant="secondary" isDisabled={busy} onClick={() => setConfirming('unexpose')}>
                  Unexpose
                </Button>
              ) : (
                <Button variant="primary" isDisabled={busy} onClick={() => setConfirming('expose')}>
                  Expose
                </Button>
              )}
              <Button
                variant="danger" isDisabled={busy || exposed}
                onClick={() => setConfirming('disable')}
              >Disable registry</Button>
            </ActionGroup>
            {exposed ? <Content component="p">Unexpose the registry first before disabling it.</Content> : null}
          </CardBody>
        </Card>
      ) : null}
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
    </>
  )
}

export function DisablePluginRegistryConfirmation({
  organizationName, busy, onConfirm, onCancel,
}: {
  organizationName: string
  busy: boolean
  onConfirm: () => void
  onCancel: () => void
}) {
  return (
    <TypedConfirmModal
      title="Disable the plugin registry?"
      body={<Content component="p">All plugin data is destroyed when the registry is disabled.</Content>}
      expected={organizationName}
      verb="Disable registry"
      busy={busy}
      onConfirm={onConfirm}
      onCancel={onCancel}
    />
  )
}

export function PluginRegistryConfirmation({
  title, body, verb, busy, onConfirm, onCancel,
}: {
  title: string
  body: string
  verb: string
  busy: boolean
  onConfirm: () => void
  onCancel: () => void
}) {
  return (
    <Modal aria-labelledby="plugin-registry-confirm-title" isOpen onClose={onCancel} variant="small">
      <PluginRegistryConfirmationView
        title={title} body={body} verb={verb} busy={busy}
        onConfirm={onConfirm} onCancel={onCancel}
      />
    </Modal>
  )
}

export function PluginRegistryConfirmationView({
  title, body, verb, busy, onConfirm, onCancel,
}: {
  title: string
  body: string
  verb: string
  busy: boolean
  onConfirm: () => void
  onCancel: () => void
}) {
  return (
    <>
      <ModalHeader labelId="plugin-registry-confirm-title" title={title} />
      <ModalBody><Content component="p">{body}</Content></ModalBody>
      <ModalFooter>
        <Button variant="primary" isLoading={busy} isDisabled={busy} onClick={onConfirm}>{verb}</Button>
        <Button variant="link" isDisabled={busy} onClick={onCancel}>Cancel</Button>
      </ModalFooter>
    </>
  )
}
