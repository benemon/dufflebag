import { useState, type ReactNode } from 'react'
import {
  Alert, Card, CardBody, CardTitle, ClipboardCopyButton, CodeBlock, CodeBlockAction,
  CodeBlockCode, Content, DescriptionList, DescriptionListDescription,
  DescriptionListGroup, DescriptionListTerm, PageSection,
} from '@patternfly/react-core'

import type { ApiInstance } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { useInstance } from '../data/instance'
import { ScreenHeader } from '../components/ScreenHeader'
import { When } from '../components/When'

/**
 * Instance — what a client needs to point at this registry.
 *
 * Client settings come from the browser and selected tenancy. Build and
 * backing-service state come from the authenticated instance endpoint.
 */
export function Instance() {
  const { state, selectedOrganization, selectedProject } = useAuth()
  const { instance, loading, failure } = useInstance()
  return (
    <InstanceView
      host={typeof window === 'undefined' ? '' : window.location.host}
      secure={typeof window === 'undefined' ? true : window.location.protocol === 'https:'}
      // The SELECTED organisation, not the token's claim: a platform session
      // has no organisation claim at all (duf-tkw), and the environment block
      // should describe the tenancy being viewed. With nothing selected the
      // identifier is omitted, exactly as the project already is.
      organizationID={selectedOrganization}
      projectID={selectedProject ?? state?.claims.projectID ?? null}
      instance={instance}
      loading={loading}
      failure={failure}
    />
  )
}

export function InstanceView({
  host, secure, organizationID, projectID, instance, loading, failure,
}: {
  host: string
  secure: boolean
  organizationID: string | null
  projectID: string | null
  instance: ApiInstance | null
  loading: boolean
  failure: string | null
}) {
  return (
    <>
      <ScreenHeader title="Instance" description="What a client needs to point at this registry." />

      {/* One section, no body wrapper: the cards sit directly in the section's
          flex column so its row gap spaces them, and a second section would
          draw a second secondary top border mid-page. */}
      <PageSection variant="secondary" isFilled hasBodyWrapper={false}>
        <BuildCard instance={instance} loading={loading} failure={failure} />
        <ScannerCard instance={instance} loading={loading} failure={failure} />
        <Card>
          <CardTitle>Client environment</CardTitle>
          <CardBody>
            {/*
              The SDK rejects any auth URL that is not https, on any network
              (config/hcp.go). Emitting an http one would produce a block that
              cannot work, so say so instead.
            */}
            {secure ? null : (
              <Alert
                variant="warning"
                isInline
                title="This console is not being served over https"
              >
                <Content component="p">
                  The SDK refuses any HCP_AUTH_URL that is not https, including on a private
                  network, so the value below will not work until this instance is served over
                  TLS. The address is shown as it would be with TLS in place.
                </Content>
              </Alert>
            )}
            <EnvironmentBlock value={clientEnvironment({ host, organizationID, projectID })} />
          </CardBody>
        </Card>
      </PageSection>
    </>
  )
}

export function ScannerCard({
  instance, loading, failure,
}: {
  instance: ApiInstance | null
  loading: boolean
  failure: string | null
}) {
  return (
    <Card>
      <CardTitle>Scanner</CardTitle>
      <CardBody>
        {loading ? <Content component="p">Loading scanner information…</Content> : null}
        {failure ? (
          <Alert variant="danger" isInline title="Scanner information could not be loaded">
            <Content component="p">{failure}</Content>
          </Alert>
        ) : null}
        {!loading && !failure ? (
          instance?.scanner?.configured ? (
            <DescriptionList isHorizontal isCompact>
              <BuildField label="Adapter" value={instance.scanner.adapter} />
            </DescriptionList>
          ) : (
            <Content component="p">Scanning is not configured.</Content>
          )
        ) : null}
      </CardBody>
    </Card>
  )
}

export function BuildCard({
  instance, loading, failure,
}: {
  instance: ApiInstance | null
  loading: boolean
  failure: string | null
}) {
  return (
    <Card>
      <CardTitle>Build</CardTitle>
      <CardBody>
        {loading ? <Content component="p">Loading build information…</Content> : null}
        {failure ? (
          <Alert variant="danger" isInline title="Build information could not be loaded">
            <Content component="p">{failure}</Content>
          </Alert>
        ) : null}
        {!loading && !failure ? (
          <DescriptionList isHorizontal isCompact>
            <BuildField label="Version" value={instance?.version} />
            <BuildField label="Commit" value={instance?.commit} />
            <BuildField label="API versions" value={instance?.api_versions?.join(', ')} />
            <BuildField label="Initialized" value={<When iso={instance?.initialized_at} />} />
            <BuildField
              label="Store"
              value={typeof instance?.store === 'boolean'
                ? (instance.store ? 'reachable' : 'unreachable')
                : undefined}
            />
            <BuildField label="Object storage" value={instance?.object_storage} />
            <BuildField label="Encryption" value={instance?.encryption} />
            <BuildField label="Audit" value={instance?.audit} />
          </DescriptionList>
        ) : null}
      </CardBody>
    </Card>
  )
}

function BuildField({ label, value }: { label: string; value?: ReactNode }) {
  return (
    <DescriptionListGroup>
      <DescriptionListTerm>{label}</DescriptionListTerm>
      <DescriptionListDescription>{value || '—'}</DescriptionListDescription>
    </DescriptionListGroup>
  )
}

/**
 * Every line visible, with a copy button.
 *
 * The expansion variant of ClipboardCopy was the obvious fit and was wrong the
 * same way the version page's snippet was (duf-yxa): it renders the value as a
 * truncated single-line input AND again expanded, so the block a reader is
 * meant to copy appears twice, once unreadable. The action slot takes a copy
 * CONTROL; the code block is the one rendering.
 */
function EnvironmentBlock({ value }: { value: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <CodeBlock
      actions={(
        <CodeBlockAction>
          <ClipboardCopyButton
            id="copy-client-environment"
            variant="plain"
            aria-label="Copy the client environment"
            onClick={() => {
              void navigator.clipboard.writeText(value)
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
      <CodeBlockCode>{value}</CodeBlockCode>
    </CodeBlock>
  )
}

/**
 * The environment a client needs.
 *
 * Placeholders are angle-bracketed for the values this page genuinely does not
 * know — the credential — and omitted entirely where the client can resolve them
 * itself. An organization-scoped principal has no project to emit, and inventing
 * one would send the client somewhere it may not be entitled to.
 */
export function clientEnvironment({
  host, organizationID, projectID,
}: {
  host: string
  organizationID: string | null
  projectID: string | null
}): string {
  const lines = [
    `export HCP_API_ADDRESS=${host}`,
    `export HCP_AUTH_URL=https://${host}`,
    'export HCP_CLIENT_ID=<client id>',
    'export HCP_CLIENT_SECRET=<client secret>',
  ]
  if (organizationID) lines.push(`export HCP_ORGANIZATION_ID=${organizationID}`)
  if (projectID) lines.push(`export HCP_PROJECT_ID=${projectID}`)
  return lines.join('\n')
}
