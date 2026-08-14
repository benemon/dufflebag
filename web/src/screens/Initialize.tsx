import { useState, type ReactNode } from 'react'
import {
  Alert, Button, Checkbox, ClipboardCopy, ClipboardCopyVariant, Content, Form, FormGroup,
  Login, LoginHeader, LoginMainBody, LoginMainHeader, Title, Wizard,
  WizardFooterWrapper, WizardStep,
} from '@patternfly/react-core'

import {
  ApiError, createOrganization, createProject, initialize, requestToken,
  type ApiOrganization, type InitRequest, type InitResponse,
} from '../api/client'
import { ClaimParametersForm, validateRecoveryParameters } from './ClaimParameters'
import { TenancyForm } from '../components/TenancyForm'

// Re-exported so the first-run tests keep loading every wizard piece from one
// module; the form lives in its own file because tenancy-creation.test.mjs
// guards this one against inline inputs (tenancy fields must be TenancyForm).
export { ClaimParametersForm, validateRecoveryParameters }

type Credentials = { client_id: string; client_secret: string }
type Step = 'initialize' | 'credentials' | 'organization' | 'project'


const stepIndex: Record<Step, number> = {
  initialize: 1,
  credentials: 2,
  organization: 3,
  project: 4,
}

function BootstrapPage({ children, host }: { children: ReactNode; host: string }) {
  return (
    <Login className="dfbg-bootstrap" header={<LoginHeader />}>
      <LoginMainHeader title="dufflebag" subtitle={host} />
      <LoginMainBody>{children}</LoginMainBody>
    </Login>
  )
}

export function credentialsFileContent(credentials: InitResponse) {
  const shares = credentials.recovery_shares.length === 1 ? 'share' : 'shares'
  return [
    '# dufflebag administrative credentials',
    '# Store this file like the secret it contains.',
    `# The recovery ${shares} are deliberately not in this file: download them`,
    '# separately and store them offline, apart from these credentials.',
    `client_id: ${credentials.client_id}`,
    `client_secret: ${credentials.client_secret}`,
    '',
  ].join('\n')
}

export function sharesFileContent(credentials: InitResponse) {
  const count = credentials.recovery_shares.length
  return [
    `# dufflebag recovery shares (${credentials.recovery_threshold}-of-${count})`,
    '# Store offline, separately from the administrative credentials.',
    `# Recovery: POST /sys/recovery with ${credentials.recovery_threshold} ${credentials.recovery_threshold === 1 ? 'share' : 'shares'} mints a fresh root principal.`,
    ...credentials.recovery_shares.map((share, index) => `recovery_share_${index + 1}: ${share}`),
    '',
  ].join('\n')
}

function downloadTextFile(name: string, content: string) {
  const blob = new Blob([content], { type: 'text/plain;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = name
  try {
    link.click()
  } finally {
    URL.revokeObjectURL(url)
  }
}

function downloadCredentials(credentials: InitResponse) {
  downloadTextFile('dufflebag-credentials.txt', credentialsFileContent(credentials))
}

function downloadShares(credentials: InitResponse) {
  downloadTextFile('dufflebag-recovery-shares.txt', sharesFileContent(credentials))
}

export function StoreCredentials({
  credentials,
  stored,
  onStoredChange,
}: {
  credentials: InitResponse
  stored: boolean
  onStoredChange: (stored: boolean) => void
}) {
  return (
    <>
      <Title headingLevel="h2" size="xl">Administrative credentials</Title>
      <Content component="p">
        Returned once in this response. They are never logged or retrievable again.
      </Content>
      <Alert
        variant="danger"
        isInline
        title="Shown once. Store these before continuing."
        style={{ marginTop: 16 }}
      >
        <Content component="p">
          The credentials grant full administrative access and can never be
          retrieved again. If they are lost, presenting the required recovery
          shares to POST /sys/recovery mints a fresh root principal — store them
          offline, separately from the credentials. If both are lost, only the
          break-glass database procedure remains.
          Use a client ID that has never authenticated against another registry; clients
          cache tokens by client ID and a collision produces confusing 401s.
        </Content>
      </Alert>
      <Form style={{ marginTop: 16 }}>
        <FormGroup label="Client ID" fieldId="init-client-id">
          <ClipboardCopy
            id="init-client-id"
            hoverTip="Copy"
            clickTip="Copied"
            variant={ClipboardCopyVariant.inlineCompact}
          >
            {credentials.client_id}
          </ClipboardCopy>
        </FormGroup>
        <FormGroup label="Client secret" fieldId="init-client-secret">
          <ClipboardCopy
            id="init-client-secret"
            hoverTip="Copy"
            clickTip="Copied"
            variant={ClipboardCopyVariant.expansion}
            isReadOnly
          >
            {credentials.client_secret}
          </ClipboardCopy>
        </FormGroup>
        <Button variant="secondary" onClick={() => downloadCredentials(credentials)}>
          Download credentials
        </Button>
        <Content component="small" style={{ display: 'block', marginTop: 8 }}>
          The file contains the secret in plain text — store it like one.
        </Content>
      </Form>

      <Title headingLevel="h3" size="md" style={{ marginTop: 24 }}>Recovery</Title>
      <Content component="p">
        {credentials.recovery_shares.length === 1
          ? 'Store the recovery share offline, separately from the credentials.'
          : 'Store the recovery shares offline, separately from the credentials.'}
      </Content>
      <Form style={{ marginTop: 16 }}>
        {credentials.recovery_shares.map((share, index) => (
          <FormGroup
            key={share}
            label={credentials.recovery_shares.length === 1
              ? 'Recovery share'
              : `Recovery share ${index + 1} of ${credentials.recovery_shares.length}`}
            fieldId={`init-recovery-share-${index}`}
          >
            <ClipboardCopy
              id={`init-recovery-share-${index}`}
              hoverTip="Copy"
              clickTip="Copied"
              variant={ClipboardCopyVariant.expansion}
              isExpanded
              isReadOnly
            >
              {share}
            </ClipboardCopy>
          </FormGroup>
        ))}
        <Button variant="secondary" onClick={() => downloadShares(credentials)}>
          Download all key shares
        </Button>
        <Content component="small" style={{ display: 'block', marginTop: 8 }}>
          The file contains every share — store it offline, apart from the credentials.
        </Content>
      </Form>

      <div style={{ marginTop: 16 }}>
        <Checkbox
          id="init-stored"
          label={credentials.recovery_shares.length === 1
            ? 'I have stored these credentials and the recovery share'
            : 'I have stored these credentials and the recovery shares'}
          isChecked={stored}
          onChange={(_event, checked) => onStoredChange(checked)}
        />
      </div>
    </>
  )
}

export function StoreCredentialsFooter({
  stored,
  submitting,
  onContinue,
}: {
  stored: boolean
  submitting: boolean
  onContinue: () => void
}) {
  return (
    <WizardFooterWrapper>
      <Button
        variant="primary"
        isLoading={submitting}
        isDisabled={!stored || submitting}
        onClick={onContinue}
      >
        Continue to organization
      </Button>
    </WizardFooterWrapper>
  )
}

export function ClaimFooter({
  validation,
  submitting,
}: {
  validation: ReturnType<typeof validateRecoveryParameters>
  submitting: boolean
}) {
  return (
    <WizardFooterWrapper>
      <Button
        type="submit"
        form="initialize-claim"
        variant="primary"
        isLoading={submitting}
        isDisabled={submitting || validation.request === null}
      >
        Initialize this instance
      </Button>
    </WizardFooterWrapper>
  )
}

/** First-run bootstrap through the same public APIs available to automation. */
export function Initialize({
  host,
  onDone,
}: {
  host: string
  onDone: (credentials: Credentials) => void
}) {
  const [step, setStep] = useState<Step>('initialize')
  const [credentials, setCredentials] = useState<InitResponse | null>(null)
  const [token, setToken] = useState<string | null>(null)
  const [organization, setOrganization] = useState<ApiOrganization | null>(null)
  const [failure, setFailure] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const [stored, setStored] = useState(false)
  const [shareCount, setShareCount] = useState('1')
  const [threshold, setThreshold] = useState('1')
  const recoveryParameters = validateRecoveryParameters(shareCount, threshold)

  const claim = async (request: InitRequest) => {
    setSubmitting(true)
    setFailure(null)
    try {
      const minted = await initialize(request)
      setCredentials(minted)
      setStep('credentials')
    } catch (err) {
      setFailure(err instanceof ApiError ? err.message : 'Initialization failed.')
    } finally {
      setSubmitting(false)
    }
  }

  const continueToOrganization = async () => {
    if (!credentials) return
    setSubmitting(true)
    setFailure(null)
    try {
      setToken(await requestToken(credentials.client_id, credentials.client_secret))
      setStep('organization')
    } catch (err) {
      setFailure(err instanceof Error ? err.message : 'The credentials could not be authenticated.')
    } finally {
      setSubmitting(false)
    }
  }

  const submitOrganization = async (name: string) => {
    if (!token) return
    setSubmitting(true)
    setFailure(null)
    try {
      setOrganization(await createOrganization(token, name))
      setStep('project')
    } catch (err) {
      setFailure(err instanceof Error ? err.message : 'The organization could not be created.')
    } finally {
      setSubmitting(false)
    }
  }

  const submitProject = async (name: string) => {
    if (!token || !organization || !credentials) return
    setSubmitting(true)
    setFailure(null)
    try {
      await createProject(token, organization.id, name)
      onDone(credentials)
    } catch (err) {
      setFailure(err instanceof Error ? err.message : 'The project could not be created.')
      setSubmitting(false)
    }
  }

  return (
    <BootstrapPage host={host}>
      <Title headingLevel="h1" size="2xl">Initialize dufflebag</Title>
      <Content component="p">
        Mint the administrative principal, store its credentials, then create the organization
        and first project. Automation uses these same public APIs.
      </Content>

      {failure && <Alert variant="danger" isInline title={failure} style={{ marginTop: 16 }} />}

      <Wizard
        key={step}
        startIndex={stepIndex[step]}
        navAriaLabel="Initialization progress"
        height="auto"
        width="100%"
        style={{ marginTop: 16 }}
      >
        <WizardStep
          id="initialize-step"
          name="Initialize"
          isDisabled={step !== 'initialize'}
          footer={<ClaimFooter validation={recoveryParameters} submitting={submitting} />}
        >
          <ClaimParametersForm
            shareCount={shareCount}
            threshold={threshold}
            validation={recoveryParameters}
            onShareCountChange={setShareCount}
            onThresholdChange={setThreshold}
            onClaim={(request) => void claim(request)}
          />
        </WizardStep>

        <WizardStep
          id="credentials-step"
          name="Store credentials"
          isDisabled={step !== 'credentials'}
          footer={(
            <StoreCredentialsFooter
              stored={stored}
              submitting={submitting}
              onContinue={() => void continueToOrganization()}
            />
          )}
        >
          {credentials && (
            <StoreCredentials
              credentials={credentials}
              stored={stored}
              onStoredChange={setStored}
            />
          )}
        </WizardStep>

        <WizardStep
          id="organization-step"
          name="Organization"
          isDisabled={step !== 'organization'}
          footer={<></>}
        >
          <Title headingLevel="h2" size="xl">Name your organization</Title>
          <TenancyForm
            kind="organization"
            formID="initialize-organization"
            fieldID="organization-name"
            submitLabel="Create organization and continue"
            submitting={submitting}
            footer="wizard"
            onSubmit={submitOrganization}
          />
        </WizardStep>

        <WizardStep
          id="project-step"
          name="Project"
          isDisabled={step !== 'project'}
          footer={<></>}
        >
          <Title headingLevel="h2" size="xl">Name your first project</Title>
          {organization && (
            <Content component="p">
              This project will belong to {organization.name}.
            </Content>
          )}
          <TenancyForm
            kind="project"
            formID="initialize-project"
            fieldID="project-name"
            submitLabel="Create project and open the console"
            submitting={submitting}
            footer="wizard"
            onSubmit={submitProject}
          />
        </WizardStep>
      </Wizard>
    </BootstrapPage>
  )
}
