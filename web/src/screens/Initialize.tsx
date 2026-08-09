import { useState } from 'react'
import {
  ActionGroup, Alert, Button, Card, CardBody, CardFooter, CardTitle, Checkbox, ClipboardCopy,
  ClipboardCopyVariant, Content, Form, FormGroup, PageSection, ProgressStep,
  ProgressStepper, TextInput, Title,
} from '@patternfly/react-core'

import {
  ApiError, createOrganization, createProject, initialize, requestToken,
  type ApiOrganization, type InitResponse,
} from '../api/client'

type Credentials = { client_id: string; client_secret: string }
type Step = 'initialize' | 'organization' | 'project'

/** First-run bootstrap through the same public APIs available to automation. */
export function Initialize({ onDone }: { onDone: (credentials: Credentials) => void }) {
  const [step, setStep] = useState<Step>('initialize')
  const [credentials, setCredentials] = useState<InitResponse | null>(null)
  const [token, setToken] = useState<string | null>(null)
  const [organization, setOrganization] = useState<ApiOrganization | null>(null)
  const [organizationName, setOrganizationName] = useState('')
  const [projectName, setProjectName] = useState('')
  const [failure, setFailure] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const [stored, setStored] = useState(false)

  const claim = async () => {
    setSubmitting(true)
    setFailure(null)
    try {
      setCredentials(await initialize())
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

  const submitOrganization = async () => {
    if (!token) return
    setSubmitting(true)
    setFailure(null)
    try {
      setOrganization(await createOrganization(token, organizationName.trim()))
      setStep('project')
    } catch (err) {
      setFailure(err instanceof Error ? err.message : 'The organization could not be created.')
    } finally {
      setSubmitting(false)
    }
  }

  const submitProject = async () => {
    if (!token || !organization || !credentials) return
    setSubmitting(true)
    setFailure(null)
    try {
      await createProject(token, organization.id, projectName.trim())
      onDone(credentials)
    } catch (err) {
      setFailure(err instanceof Error ? err.message : 'The project could not be created.')
      setSubmitting(false)
    }
  }

  return (
    // Full screen, outside the app shell, like the sign-in page beside it: a
    // session mid-bootstrap can use no shell affordance — nav needs a session,
    // the tenant switcher needs claims that do not exist yet — so offering
    // them would advertise controls that cannot work. Top-aligned rather than
    // vertically centred because the wizard grows as steps complete, and
    // centring would push the later steps off a short viewport.
    <PageSection
      variant="default"
      style={{ maxWidth: 760, margin: '0 auto', minHeight: '100vh', paddingBlock: '3rem' }}
    >
      <Title headingLevel="h1" size="2xl">Initialize dufflebag</Title>
      <Content component="p">
        Three steps: mint the administrative principal, create the organization, and create the
        first project. Automation uses these same public APIs.
      </Content>

      {!credentials && (
        <Alert
          variant="warning"
          isInline
          title="This instance is uninitialized"
          style={{ marginTop: 16 }}
        >
          <Content component="p">
            Whoever completes this flow first owns the deployment. Do not expose an uninitialized
            instance publicly.
          </Content>
        </Alert>
      )}

      <Card style={{ marginTop: 16 }}>
        <CardBody>
          <ProgressStepper aria-label="Initialization progress" isCenterAligned>
            <ProgressStep
              id="initialize-step"
              titleId="initialize-step-title"
              variant={step === 'initialize' ? 'info' : 'success'}
              isCurrent={step === 'initialize'}
              aria-label={step === 'initialize' ? 'Initialize, current step' : 'Initialize, completed'}
            >
              Initialize
            </ProgressStep>
            <ProgressStep
              id="organization-step"
              titleId="organization-step-title"
              variant={step === 'organization' ? 'info' : step === 'project' ? 'success' : 'pending'}
              isCurrent={step === 'organization'}
              aria-label={step === 'organization' ? 'Organization, current step' : 'Organization'}
            >
              Organization
            </ProgressStep>
            <ProgressStep
              id="project-step"
              titleId="project-step-title"
              variant={step === 'project' ? 'info' : 'pending'}
              isCurrent={step === 'project'}
              aria-label={step === 'project' ? 'Project, current step' : 'Project'}
            >
              Project
            </ProgressStep>
          </ProgressStepper>
        </CardBody>
      </Card>

      {failure && <Alert variant="danger" isInline title={failure} style={{ marginTop: 16 }} />}

      {step === 'initialize' && !credentials && (
        <Card style={{ marginTop: 16 }}>
          <CardTitle>Before you continue</CardTitle>
          <CardBody>
            <Content component="p">
              Initialization happens once and cannot be repeated or undone. It creates only the
              first root principal; you will name the first tenancy in the next two steps.
            </Content>
            <Button
              variant="primary"
              isLoading={submitting}
              isDisabled={submitting}
              onClick={() => void claim()}
              style={{ marginTop: 16 }}
            >
              Initialize this instance
            </Button>
          </CardBody>
        </Card>
      )}

      {step === 'initialize' && credentials && (
        <Card style={{ marginTop: 16 }}>
          <CardTitle>Administrative credentials</CardTitle>
          <CardBody>
            <Content component="p">
              Returned once in this response. They are never logged or retrievable again.
            </Content>
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
                    isReadOnly
                  >
                    {share}
                  </ClipboardCopy>
                </FormGroup>
              ))}
            </Form>
            <Alert
              variant="danger"
              isInline
              title="Shown once. Store these before continuing."
              style={{ marginTop: 16 }}
            >
              <Content component="p">
                The credentials grant full administrative access and are hashed with argon2id on
                write. If they are lost, presenting the recovery share to POST /sys/recovery
                mints a fresh root principal — store it offline, separately from the
                credentials. If both are lost, only the break-glass database procedure remains.
                Use a client ID that has never authenticated against another registry; clients
                cache tokens by client ID and a collision produces confusing 401s.
              </Content>
            </Alert>
            <Checkbox
              id="init-stored"
              label="I have stored these credentials and the recovery share"
              isChecked={stored}
              onChange={(_e, checked) => setStored(checked)}
              style={{ marginTop: 16 }}
            />
          </CardBody>
          <CardFooter>
            <Button
              variant="primary"
              isLoading={submitting}
              isDisabled={!stored || submitting}
              onClick={() => void continueToOrganization()}
            >
              Continue to organization
            </Button>
          </CardFooter>
        </Card>
      )}

      {step === 'organization' && (
        <Card style={{ marginTop: 16 }}>
          <CardTitle>Name your organization</CardTitle>
          <CardBody>
            <Form
              onSubmit={(event) => {
                event.preventDefault()
                void submitOrganization()
              }}
            >
              <FormGroup label="Organization name" isRequired fieldId="organization-name">
                <TextInput
                  id="organization-name"
                  value={organizationName}
                  onChange={(_event, value) => setOrganizationName(value)}
                  autoFocus
                />
              </FormGroup>
              <ActionGroup>
                <Button
                  type="submit"
                  variant="primary"
                  isLoading={submitting}
                  isDisabled={submitting || organizationName.trim() === ''}
                >
                  Create organization and continue
                </Button>
              </ActionGroup>
            </Form>
          </CardBody>
        </Card>
      )}

      {step === 'project' && organization && (
        <Card style={{ marginTop: 16 }}>
          <CardTitle>Name your first project</CardTitle>
          <CardBody>
            <Content component="p">
              This project will belong to {organization.name}.
            </Content>
            <Form
              style={{ marginTop: 16 }}
              onSubmit={(event) => {
                event.preventDefault()
                void submitProject()
              }}
            >
              <FormGroup label="Project name" isRequired fieldId="project-name">
                <TextInput
                  id="project-name"
                  value={projectName}
                  onChange={(_event, value) => setProjectName(value)}
                  autoFocus
                />
              </FormGroup>
              <ActionGroup>
                <Button
                  type="submit"
                  variant="primary"
                  isLoading={submitting}
                  isDisabled={submitting || projectName.trim() === ''}
                >
                  Create project and open the console
                </Button>
              </ActionGroup>
            </Form>
          </CardBody>
        </Card>
      )}
    </PageSection>
  )
}
