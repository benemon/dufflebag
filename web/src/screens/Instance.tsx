import { useEffect, useState, type ReactNode } from 'react'
import {
  Alert, Card, CardBody, CardTitle, ClipboardCopyButton, CodeBlock, CodeBlockAction,
  CodeBlockCode, Content, DescriptionList, DescriptionListDescription,
  DescriptionListGroup, DescriptionListTerm, PageSection,
} from '@patternfly/react-core'

import { listBuckets, signOutIfUnauthorized, type ApiInstance } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { useInstance } from '../data/instance'
import { ScreenHeader } from '../components/ScreenHeader'
import { When } from '../components/When'
import { SkeletonRows } from '../components/Loading'

/**
 * Instance — what a client needs to point at this registry.
 *
 * Client settings come from the browser and selected tenancy. Build and
 * backing-service state come from the authenticated instance endpoint.
 */
export function Instance() {
  const { state, selectedOrganization, selectedProject, signOut } = useAuth()
  const [refresh, setRefresh] = useState(0)
  const { instance, loading, failure } = useInstance(refresh)
  // A bucket-scoped session's environment block names its bucket: the server
  // only accepts builds into that one, and Packer reads the name from
  // HCP_PACKER_BUCKET_NAME. The claim carries only the id; resolve it against
  // the scoped listing exactly as the landing does (App.tsx). Deliberately
  // bucket-scoped sessions ONLY — for a wider tenancy that happens to be
  // viewing a bucket, emitting one would pin clients to a bucket the
  // credential is not bound to.
  const claims = state?.claims
  const bucketID = claims?.bucketID ?? null
  const [bucketName, setBucketName] = useState<string | null>(null)
  const [bucketNameFailure, setBucketNameFailure] = useState<string | null>(null)
  useEffect(() => {
    // A renewal re-runs this effect (state carries the token); resetting first
    // would blank the row every ~14 minutes for nothing. The claim never
    // changes within a session, so only its absence clears the name. The
    // screen's Refresh counter is a dependency so a failed resolution has a
    // retry path (review finding: a transient failure otherwise left the
    // block silently incomplete until remount).
    if (!bucketID || !state || !claims?.organizationID || !claims.projectID) {
      setBucketName(null)
      setBucketNameFailure(null)
      return
    }
    let cancelled = false
    listBuckets(state.token, {
      organizationID: claims.organizationID,
      projectID: claims.projectID,
    })
      .then((buckets) => {
        if (cancelled) return
        const scoped = buckets.find((bucket) => bucket.id === bucketID)
        setBucketName(scoped?.name ?? null)
        setBucketNameFailure(scoped
          ? null
          : 'The session names a bucket the listing cannot see.')
      })
      .catch((err: unknown) => {
        // The row is additive — the screen keeps working — but its absence is
        // stated, not silent: an operator would otherwise copy a
        // plausible-looking block missing the bucket variable.
        if (cancelled) return
        if (signOutIfUnauthorized(err, signOut)) return
        setBucketNameFailure(err instanceof Error ? err.message : 'The bucket could not be resolved.')
      })
    return () => {
      cancelled = true
    }
  }, [bucketID, state, claims?.organizationID, claims?.projectID, signOut, refresh])
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
      bucketName={bucketName}
      bucketNameFailure={bucketNameFailure}
      instance={instance}
      loading={loading}
      failure={failure}
      onRefresh={() => setRefresh((current) => current + 1)}
    />
  )
}

export function InstanceView({
  host, secure, organizationID, projectID, bucketName = null, bucketNameFailure = null,
  instance, loading, failure, onRefresh = () => {},
}: {
  host: string
  secure: boolean
  organizationID: string | null
  projectID: string | null
  /** The session's bucket, bucket-scoped sessions only — null everywhere else. */
  bucketName?: string | null
  /** Why the session's bucket could not be named, when it could not be. */
  bucketNameFailure?: string | null
  instance: ApiInstance | null
  loading: boolean
  failure: string | null
  onRefresh?: () => void
}) {
  return (
    <>
      <ScreenHeader
        title="Instance"
        description="What a client needs to point at this registry."
        onRefresh={onRefresh}
        refreshing={loading}
      />

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
            {bucketNameFailure ? (
              <Alert
                variant="warning"
                isInline
                title="The session's bucket could not be resolved"
              >
                <Content component="p">
                  {bucketNameFailure} The block below omits HCP_PACKER_BUCKET_NAME;
                  refresh to retry.
                </Content>
              </Alert>
            ) : null}
            <EnvironmentBlock
              value={clientEnvironment({ host, organizationID, projectID, bucketName })}
            />
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
        {loading ? <SkeletonRows screenreaderText="Loading scanner information…" /> : null}
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
        {loading ? <SkeletonRows screenreaderText="Loading build information…" /> : null}
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
              label="Database"
              value={typeof instance?.store === 'boolean'
                ? (instance.store ? 'ok' : 'unreachable')
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
  host, organizationID, projectID, bucketName = null,
}: {
  host: string
  organizationID: string | null
  projectID: string | null
  bucketName?: string | null
}): string {
  const lines = [
    `export HCP_API_ADDRESS=${host}`,
    `export HCP_AUTH_URL=https://${host}`,
    'export HCP_CLIENT_ID=<client id>',
    'export HCP_CLIENT_SECRET=<client secret>',
  ]
  if (organizationID) lines.push(`export HCP_ORGANIZATION_ID=${organizationID}`)
  if (projectID) lines.push(`export HCP_PROJECT_ID=${projectID}`)
  // Packer's fallback bucket name when the template names none — the exact
  // variable the client reads (packer v1.16.0 internal/hcp/env/variables.go,
  // HCPPackerBucket). Emitted only for bucket-scoped sessions, which can
  // publish nowhere else.
  if (bucketName) lines.push(`export HCP_PACKER_BUCKET_NAME=${shellValue(bucketName)}`)
  return lines.join('\n')
}

/**
 * Bucket names are arbitrary strings (the compat plane deliberately imposes no
 * character class), and this block is made to be pasted into a shell — an
 * unquoted name containing spaces or metacharacters exports the wrong value or
 * runs it. Common names stay bare so the block reads clean; anything else is
 * single-quoted with embedded quotes escaped.
 */
export function shellValue(value: string): string {
  if (/^[A-Za-z0-9._-]+$/.test(value)) return value
  return `'${value.replace(/'/g, `'\\''`)}'`
}
