import { useEffect, useState } from 'react'
import {
  ActionGroup,
  Checkbox, Alert, Button, Content, Form, FormGroup, LoginPage,
  Tab, Tabs, TabTitleText, TextInput,
} from '@patternfly/react-core'

import { useAuth } from '../auth/AuthContext'
import { useHumanMethods } from '../auth/methods'
import { instanceHealth, type InstanceHealth } from '../api/client'
import { Initialize } from './Initialize'

/**
 * Sign in.
 *
 * Tabbed by method, with the service principal tab ALWAYS present and human
 * methods added to its left as they are enabled. That ordering is deliberate:
 * the tab that cannot be turned off is the one that is always there, so an
 * instance with no human sign-in still has a visible way in (ADR-0019).
 */
export function Login() {
  const { signIn, sessionEnded } = useAuth()
  const { methods } = useHumanMethods()
  // /sys/health answers claimed-or-not without consuming the instance, so
  // first run is no longer a link an operator must guess to click: a fresh
  // instance LANDS on the wizard, and a claimed one never offers it (duf-2so).
  // null = probing; 'unreachable' = the probe itself could not be read. A 503
  // still has a useful body: database and audit degradation are distinct.
  const [health, setHealth] = useState<InstanceHealth | 'unreachable' | null>(null)
  // Only carried when the wizard's implicit sign-in fails: the operator then
  // signs in by hand with the credentials still on screen, ack-gated.
  const [minted, setMinted] = useState<{ client_id: string; client_secret: string } | null>(null)

  useEffect(() => {
    let cancelled = false
    instanceHealth()
      .then((result) => { if (!cancelled) setHealth(result) })
      .catch(() => { if (!cancelled) setHealth('unreachable') })
    return () => { cancelled = true }
  }, [])

  if (health === null) {
    // A blank page and a broken one are indistinguishable; say what is happening.
    return <LoginPage loginTitle="Dufflebag" loginSubtitle="Checking this instance…" />
  }

  if (health === 'unreachable') {
    return (
      <LoginPage loginTitle="Dufflebag" loginSubtitle={window.location.host}>
        <Alert variant="danger" isInline title="This instance could not be reached">
          <Content component="p">
            The status probe at /sys/health did not answer, so the console cannot tell
            whether this instance needs first-run initialization. Nothing was changed.
          </Content>
          <Button variant="secondary" onClick={() => window.location.reload()} style={{ marginTop: 12 }}>
            Try again
          </Button>
        </Alert>
      </LoginPage>
    )
  }

  const destination = loginDestination(health)

  if (destination === 'database-failure') {
    return (
      <LoginPage loginTitle="Dufflebag" loginSubtitle={window.location.host}>
        <Alert variant="danger" isInline title="The instance database is unavailable">
          <Content component="p">
            The status endpoint answered, but its database check failed. The console cannot safely
            decide whether first-run initialization is needed. Nothing was changed.
          </Content>
          <Button variant="secondary" onClick={() => window.location.reload()} style={{ marginTop: 12 }}>
            Try again
          </Button>
        </Alert>
      </LoginPage>
    )
  }

  if (destination === 'initialize' && minted === null) {
    return (
      <Initialize
        onDone={async (credentials) => {
          // The wizard just proved these credentials against /oauth2/token to
          // create the organization and project, so the operator lands in the
          // console authenticated rather than retyping a secret they were told
          // not to lose. The ceremony is unchanged: the credential card's
          // acknowledgement gate has already been passed to get here.
          try {
            await signIn(credentials.client_id, credentials.client_secret)
          } catch {
            // Honest fallback: show the ack-gated, prefilled form instead of
            // silently looping the wizard.
            setMinted(credentials)
          }
        }}
      />
    )
  }

  return (
    <LoginPage
      loginTitle="Dufflebag"
      loginSubtitle={window.location.host}
    >
      {health.audit === 'degraded' ? (
        <Alert variant="warning" isInline title="Audit recording is degraded" style={{ marginBottom: 16 }}>
          <Content component="p">
            The database answered, but no configured audit target is accepting records. This is an
            audit failure, not a database failure; authenticated requests may be refused until a
            target recovers.
          </Content>
        </Alert>
      ) : null}
      {/*
        Only when the console bounced the operator here itself — an expired
        token mid-use. An explicit sign-out and a fresh arrival say nothing:
        the notice belongs to the transition, not the screen, and it does not
        survive a reload of this screen (duf-1cn).
      */}
      {sessionEnded && (
        <Alert variant="info" isInline title="Your session ended" style={{ marginBottom: 16 }}>
          <Content component="p">The token it was carrying expired. Sign in again to continue.</Content>
        </Alert>
      )}
      <Tabs defaultActiveKey={methods.basic ? 'basic' : methods.oidc ? 'oidc' : 'principal'}>
        {methods.basic && (
          <Tab eventKey="basic" title={<TabTitleText>Password</TabTitleText>}>
            <BasicAuthForm />
          </Tab>
        )}
        {methods.oidc && (
          <Tab eventKey="oidc" title={<TabTitleText>{methods.oidc.label}</TabTitleText>}>
            <Content component="p" style={{ marginTop: 16 }}>
              You will be redirected to {methods.oidc.label} and back. Your role comes from the
              group claim.
            </Content>
            <Button variant="primary" component="a" href="/auth/oidc/start" style={{ marginTop: 12 }}>
              Continue to {methods.oidc.label}
            </Button>
          </Tab>
        )}
        <Tab eventKey="principal" title={<TabTitleText>Service principal</TabTitleText>}>
          <ServicePrincipalForm
            key={minted?.client_id ?? 'empty'}
            initial={minted}
            onSignIn={signIn}
          />
        </Tab>
      </Tabs>
    </LoginPage>
  )
}

export function loginDestination(
  health: InstanceHealth,
): 'initialize' | 'sign-in' | 'database-failure' {
  if (!health.database) return 'database-failure'
  return health.initialized ? 'sign-in' : 'initialize'
}

export function ServicePrincipalForm({
  initial,
  onSignIn,
}: {
  initial: { client_id: string; client_secret: string } | null
  onSignIn: (clientID: string, clientSecret: string) => Promise<void>
}) {
  const [clientID, setClientID] = useState(initial?.client_id ?? '')
  const [clientSecret, setClientSecret] = useState(initial?.client_secret ?? '')
  const [failure, setFailure] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  // Only asked on first run, when this form is carrying a secret the operator
  // has just been shown for the only time.
  const [acknowledged, setAcknowledged] = useState(false)
  const justMinted = initial !== null

  const submit = async () => {
    setSubmitting(true)
    setFailure(null)
    try {
      await onSignIn(clientID.trim(), clientSecret)
    } catch {
      // Deliberately not distinguishing an unknown client id from a wrong
      // secret: telling them apart enumerates valid client ids, which is the
      // same reason the server answers both identically (ADR-0018).
      setFailure('Those credentials were not accepted.')
    } finally {
      setSubmitting(false)
      // Dropped whether or not sign-in succeeded; there is no reason to keep it
      // after the exchange.
      setClientSecret('')
    }
  }

  return (
    <>
      {failure && <Alert variant="danger" isInline title={failure} style={{ marginTop: 12 }} />}
      <Form
        style={{ marginTop: 16 }}
        onSubmit={(e) => {
          e.preventDefault()
          void submit()
        }}
      >
        <FormGroup label="Client ID" isRequired fieldId="client-id">
          <TextInput
            id="client-id"
            value={clientID}
            onChange={(_e, v) => setClientID(v)}
            autoComplete="username"
          />
        </FormGroup>
        <FormGroup label="Client secret" isRequired fieldId="client-secret">
          <TextInput
            id="client-secret"
            type="password"
            value={clientSecret}
            onChange={(_e, v) => setClientSecret(v)}
            autoComplete="current-password"
          />
        </FormGroup>
        {justMinted ? (
          <Checkbox
            id="secret-stored"
            isChecked={acknowledged}
            onChange={(_e, checked) => setAcknowledged(checked)}
            label="I have stored this client secret somewhere safe"
            description={
              'It cannot be retrieved after this screen. If it is lost before a second root ' +
              'principal exists, this instance can only be administered by direct database access.'
            }
          />
        ) : null}
        <ActionGroup>
          <Button
            type="submit"
            variant="primary"
            isDisabled={
              submitting ||
              clientID.trim() === '' ||
              clientSecret === '' ||
              // Signing in is not the dangerous step — reloading afterwards is,
              // because the token is held in memory only and the secret is gone
              // by then. The gate is here because this is the last moment the
              // secret is on screen (duf-9rr).
              (justMinted && !acknowledged)
            }
            isLoading={submitting}
          >
            Log in
          </Button>
        </ActionGroup>
      </Form>
    </>
  )
}

/**
 * Basic authentication is not served yet — the form exists so the tab is real
 * once the method is enabled. It is only reachable when the instance reports
 * basic authentication as available, so it cannot be reached today.
 */
function BasicAuthForm() {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')

  return (
    <Form style={{ marginTop: 16 }} onSubmit={(e) => e.preventDefault()}>
      <FormGroup label="Username" isRequired fieldId="username">
        <TextInput id="username" value={username} onChange={(_e, v) => setUsername(v)} autoComplete="username" />
      </FormGroup>
      <FormGroup label="Password" isRequired fieldId="password">
        <TextInput
          id="password"
          type="password"
          value={password}
          onChange={(_e, v) => setPassword(v)}
          autoComplete="current-password"
        />
      </FormGroup>
      <ActionGroup>
        <Button type="submit" variant="primary" isDisabled>
          Sign in
        </Button>
      </ActionGroup>
      <Content component="small">
        A forgotten password is reset by an administrator — this instance sends no mail.
      </Content>
    </Form>
  )
}
