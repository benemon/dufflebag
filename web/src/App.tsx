import { Navigate, Route, Routes } from 'react-router'
import { Alert, Content, PageSection } from '@patternfly/react-core'

import { AuthProvider, useAuth } from './auth/AuthContext'
import { AppShell } from './shell/AppShell'
import { Buckets } from './screens/Buckets'
import { Versions } from './screens/Versions'
import { Version } from './screens/Version'
import { Build } from './screens/Build'
import { Login } from './screens/Login'
import { NotBuilt } from './screens/NotBuilt'
import { Instance } from './screens/Instance'
import { Principals } from './screens/Principals'
import { Audit } from './screens/Audit'
import { Encryption } from './screens/Encryption'
import { BagDrop } from './screens/BagDrop'

export function App() {
  return (
    <AuthProvider>
      <Authenticated />
    </AuthProvider>
  )
}

/**
 * Nothing renders until there is a token.
 *
 * The gate is here rather than per-route so a screen added later is denied by
 * default — an unauthenticated route has to be written deliberately, it cannot
 * be arrived at by forgetting something (ADR-0017).
 */
function Authenticated() {
  const { state, restoring, selectedProject, projectsLoading, projectFailure } = useAuth()
  // While the boot exchange asks whether a session survived the reload,
  // showing the sign-in screen would flash a state that may be about to be
  // untrue. Nothing renders until the answer is in — it is one same-origin
  // round trip.
  if (!state && restoring) return null
  if (!state) return <Login />
  // A platform-scoped session (no organization claim — the bootstrap root,
  // duf-tkw) is gated on nothing: Principals and Instance need no tenancy, and
  // the data screens explain what is missing until the header picker names one.
  // A tenancy-scoped session keeps its gates, because for it a missing project
  // is a settled fact about the token rather than a choice not yet made.
  const platform = state.claims.organizationID === null
  if (!platform && projectsLoading) {
    return (
      <AppShell>
        <PageSection><Content component="p">Loading projects…</Content></PageSection>
      </AppShell>
    )
  }
  if (!platform && projectFailure) {
    return (
      <AppShell>
        <ProjectLoadFailure failure={projectFailure} />
      </AppShell>
    )
  }
  if (!platform && !selectedProject) {
    return (
      <AppShell>
        <PageSection>
          <Alert variant="info" isInline title="No projects are available to this principal" />
        </PageSection>
      </AppShell>
    )
  }

  return (
    <AppShell>
      <Routes>
        <Route path="/" element={<Navigate to="/buckets" replace />} />
        <Route path="/buckets" element={<Buckets />} />
        <Route path="/buckets/:bucket" element={<Versions />} />
        <Route path="/buckets/:bucket/versions/:fingerprint" element={<Version />} />
        <Route path="/buckets/:bucket/versions/:fingerprint/builds/:build" element={<Build />} />
        <Route path="/ancestry" element={<NotBuilt title="Ancestry" />} />
        <Route path="/principals" element={<Principals />} />
        <Route path="/audit" element={<Audit />} />
        <Route path="/encryption" element={<Encryption />} />
        <Route path="/bagdrop" element={<BagDrop />} />
        <Route path="/instance" element={<Instance />} />
      </Routes>
    </AppShell>
  )
}

export function ProjectLoadFailure({ failure }: { failure: string }) {
  return (
    <PageSection>
      <Alert variant="danger" isInline title="Projects could not be loaded">
        <Content component="p">{failure}</Content>
      </Alert>
    </PageSection>
  )
}
